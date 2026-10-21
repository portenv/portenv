// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The in-box side of ADR 0022 §4: each agent's portenv socket, and the
// TeamCalls stream that carries its calls up to portenvd.
//
// On the socket, a call is one line of protojson (a TeamCallsResponse with
// only its op set) and the answer is one line (a TeamCallsRequest). The
// caller is the socket peer's uid, read from the kernel; any call_id or
// agent_id in the request is replaced.

// maxTeamCallBytes caps one call's line: a message's 8 KB, a proposed
// agent file's 64 KB, and room for the JSON around them.
const maxTeamCallBytes = 128 << 10

const (
	// teamCallWait is how long a call waits for portenvd's answer;
	// inbox --wait waits up to a minute in portenvd, so this is longer.
	teamCallWait = 90 * time.Second
	// teamAttachWait is how long a call waits for portenvd's stream to
	// come back after a restart of either side.
	teamAttachWait = time.Second
)

// teamHub holds the agents' sockets and portenvd's TeamCalls stream.
//
// It runs in the unprivileged API process: it makes no folders and gives
// nothing to another uid. init makes each agent's run folder
// (TeamUsers.ensureRunDir) before the hub serves the agent's socket in it.
type teamHub struct {
	dir     string
	peerUID func(*net.UnixConn) (int, error)
	// groupOf reads a path's group without following a link (-1: unknown).
	groupOf func(path string) int

	mu        sync.Mutex
	agents    map[string]*teamAgent
	byUID     map[int]string
	stream    *teamStream
	streamSet chan struct{} // closed and replaced whenever stream changes

	// work's own socket, for pushes (R-0391), and where their bundles wait.
	owner    net.Listener
	ownerUID int
	pushRoot string
}

type teamAgent struct {
	id       string
	uid, gid int
	lis      net.Listener
}

// teamStream is one attached TeamCalls stream.
type teamStream struct {
	send    func(*agentv1.TeamCallsResponse) error
	sendMu  sync.Mutex
	done    chan struct{}
	waitMu  sync.Mutex
	waiting map[string]chan *agentv1.TeamCallsRequest
}

func newTeamHub(dir string) *teamHub {
	return &teamHub{
		dir:       dir,
		peerUID:   socketPeerUID,
		groupOf:   fileGroup,
		agents:    map[string]*teamAgent{},
		byUID:     map[int]string{},
		streamSet: make(chan struct{}),
	}
}

// TMPDIR is the agent's own temporary folder (R-0245 item 1).
func (h *teamHub) TMPDIR(id string) string { return filepath.Join(h.dir, id, "tmp") }

// addAgent serves the agent's socket in the run folder init made for it:
//
//	<dir>/<id>/              the helper's, the agent's group, setgid, 0710 (init)
//	<dir>/<id>/portenv.sock  the helper's, the agent's group, 0660
//
// The socket takes the agent's group from the folder's setgid bit as it's
// made; the hub checks it, and serves nothing if it's any other group.
func (h *teamHub) addAgent(id string, uid, gid int) error {
	if !agentIDRE.MatchString(id) {
		return fmt.Errorf("%q %w", id, ErrInvalidAgentID)
	}
	h.removeAgent(id)
	base := filepath.Join(h.dir, id)
	if err := checkRealDir(base); err != nil {
		return fmt.Errorf("%s's run folder isn't ready (init makes it): %w", id, err)
	}
	sock := filepath.Join(base, "portenv.sock")
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove a stale socket: %w", err)
	}
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sock, err)
	}
	fail := func(err error) error {
		_ = lis.Close()
		_ = os.Remove(sock)
		return err
	}
	if err := os.Chmod(sock, 0o660); err != nil { // #nosec G302 -- the helper and this agent only
		return fail(err)
	}
	if g := h.groupOf(sock); g != gid {
		return fail(fmt.Errorf("%s has group %d, not %s's %d; is its folder's setgid bit missing?", sock, g, id, gid))
	}
	a := &teamAgent{id: id, uid: uid, gid: gid, lis: lis}
	h.mu.Lock()
	h.agents[id] = a
	h.byUID[uid] = id
	h.mu.Unlock()
	go h.serve(a)
	return nil
}

// removeAgent stops serving the agent's socket. Its folders stay until the
// box stops (/run is not saved).
func (h *teamHub) removeAgent(id string) {
	h.mu.Lock()
	a := h.agents[id]
	delete(h.agents, id)
	if a != nil && h.byUID[a.uid] == id {
		delete(h.byUID, a.uid)
	}
	h.mu.Unlock()
	if a != nil {
		_ = a.lis.Close()
		_ = os.Remove(filepath.Join(h.dir, id, "portenv.sock"))
	}
}

func (h *teamHub) close() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.agents))
	for id := range h.agents {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.removeAgent(id)
	}
	h.mu.Lock()
	if h.owner != nil {
		_ = h.owner.Close()
		h.owner = nil
	}
	h.mu.Unlock()
}

func (h *teamHub) serve(a *teamAgent) {
	for {
		conn, err := a.lis.Accept()
		if err != nil {
			return
		}
		go h.handle(a, conn.(*net.UnixConn))
	}
}

// handle answers one call on an agent's socket.
func (h *teamHub) handle(a *teamAgent, conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	reply := func(r *agentv1.TeamCallsRequest) {
		b, err := protojson.Marshal(r)
		if err != nil {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, _ = conn.Write(append(b, '\n'))
	}
	refuse := func(line string) { reply(&agentv1.TeamCallsRequest{Line: line}) }

	// Read the request before anything else, so a refusal always reaches
	// the caller as a line: refusing first could close the connection under
	// its write (EPIPE), and it would never see why.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReaderSize(io.LimitReader(conn, maxTeamCallBytes+1), 64<<10).ReadBytes('\n')
	tooLarge := len(line) > maxTeamCallBytes
	if tooLarge {
		// Let the rest of the write land, up to a bound, before answering.
		// The CLI half-closes after its line, so this ends at once.
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, io.LimitReader(conn, 8*maxTeamCallBytes))
	}
	_ = conn.CloseRead()

	uid, perr := h.peerUID(conn)
	if perr != nil {
		refuse("Portenv couldn't tell who is calling.")
		return
	}
	// The socket's modes let only this agent in; the uid check holds even
	// if a mode is ever wrong.
	h.mu.Lock()
	id, ok := h.byUID[uid]
	h.mu.Unlock()
	if !ok || id != a.id {
		refuse(fmt.Sprintf("uid %d isn't an agent on this socket.", uid))
		return
	}
	if tooLarge {
		refuse("That request is too large.")
		return
	}
	if err != nil && !errors.Is(err, io.EOF) {
		refuse("Portenv couldn't read the request.")
		return
	}
	var c agentv1.TeamCallsResponse
	if err := protojson.Unmarshal(line, &c); err != nil {
		refuse("Portenv couldn't read the request: " + err.Error())
		return
	}
	if c.GetOp() == nil {
		refuse("The request names no command.")
		return
	}
	if c.GetPushList() != nil || c.GetPush() != nil {
		refuse(notTheOwner)
		return
	}
	c.CallId, c.AgentId = newCallID(), a.id
	reply(h.relay(&c))
}

// relay sends a call up the attached stream and waits for its answer.
func (h *teamHub) relay(c *agentv1.TeamCallsResponse) *agentv1.TeamCallsRequest {
	return h.relayWait(c, teamCallWait)
}

// relayWait is relay, waiting up to wait for the answer.
func (h *teamHub) relayWait(c *agentv1.TeamCallsResponse, wait time.Duration) *agentv1.TeamCallsRequest {
	s := h.waitStream(teamAttachWait)
	if s == nil {
		return &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: "Portenv isn't connected to this workspace right now. Try again in a moment."}
	}
	ch := make(chan *agentv1.TeamCallsRequest, 1)
	s.waitMu.Lock()
	s.waiting[c.GetCallId()] = ch
	s.waitMu.Unlock()
	defer func() {
		s.waitMu.Lock()
		delete(s.waiting, c.GetCallId())
		s.waitMu.Unlock()
	}()
	s.sendMu.Lock()
	err := s.send(c)
	s.sendMu.Unlock()
	lost := &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: "Portenv lost the connection to this workspace. Try again in a moment."}
	if err != nil {
		return lost
	}
	select {
	case r := <-ch:
		return r
	case <-s.done:
		return lost
	case <-time.After(wait):
		return &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: "Portenv didn't answer in time. Try again."}
	}
}

func (h *teamHub) waitStream(d time.Duration) *teamStream {
	deadline := time.After(d)
	for {
		h.mu.Lock()
		s, changed := h.stream, h.streamSet
		h.mu.Unlock()
		if s != nil {
			return s
		}
		select {
		case <-changed:
		case <-deadline:
			return nil
		}
	}
}

// attach serves one TeamCalls stream from portenvd until ctx ends or recv
// fails. A newer stream replaces an older one. started, if not nil, is
// called once the stream is attached.
func (h *teamHub) attach(ctx context.Context, send func(*agentv1.TeamCallsResponse) error, recv func() (*agentv1.TeamCallsRequest, error), started func()) error {
	s := &teamStream{send: send, done: make(chan struct{}), waiting: map[string]chan *agentv1.TeamCallsRequest{}}
	h.mu.Lock()
	h.stream = s
	close(h.streamSet)
	h.streamSet = make(chan struct{})
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.stream == s {
			h.stream = nil
			close(h.streamSet)
			h.streamSet = make(chan struct{})
		}
		h.mu.Unlock()
		close(s.done)
	}()
	if started != nil {
		started()
	}
	errc := make(chan error, 1)
	go func() {
		for {
			r, err := recv()
			if err != nil {
				errc <- err
				return
			}
			s.waitMu.Lock()
			ch := s.waiting[r.GetCallId()]
			s.waitMu.Unlock()
			if ch != nil {
				ch <- r
			}
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errc:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
}

func newCallID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TeamCalls carries agents' in-box portenv calls up to portenvd (ADR 0022
// §4): portenvd opens it, the helper sends each call with its caller, and
// portenvd answers after its grant check.
func (s *apiServer) TeamCalls(stream agentv1.AgentService_TeamCallsServer) error {
	if s.team == nil {
		return status.Error(codes.Unimplemented, "no team in this workspace")
	}
	return s.team.attach(stream.Context(), stream.Send, stream.Recv, nil)
}
