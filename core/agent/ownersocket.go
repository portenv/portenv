// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// work's own socket (R-0391): git-remote-portenv's list for-push and push.
// init makes work's run folder at every box start:
//
//	<RunDir>/work/              the helper's, group work, setgid, 0710
//	<RunDir>/work/portenv.sock  the helper's, group work, 0660
//
// Agents aren't in group work, so they can't reach it; and whatever the
// modes, the caller's uid (SO_PEERCRED) must be work's. A push's bundle is
// streamed after its line and kept in the API process's own 0700 folder
// until portenvd reads it (ReadPushBundle), once.

// ownerFolder is work's run folder's name: "work" is never an agent id.
const ownerFolder = "work"

// notTheOwner is the refusal for anyone but work.
const notTheOwner = "Only you can push from this workspace. Agents can’t push."

// maxPushBundle is GitHub's limit for one push.
var maxPushBundle int64 = 2 << 30

// ownerCallWait is how long a push may wait: the owner's answer in
// Portenv, then GitHub.
const ownerCallWait = 10 * time.Minute

var pushBundleIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ownerIdle is how long work's socket waits for the next bytes of a call
// (its line, then its bundle): an idle limit, so a big push that keeps
// streaming is never cut off, and one that stops is.
var ownerIdle = 10 * time.Second

// ensureOwnerRunDir makes work's run folder, as root.
func (t *TeamUsers) ensureOwnerRunDir(uid int) (string, error) {
	if err := t.ensureRunRoot(); err != nil {
		return "", err
	}
	fchown := t.fchown
	if fchown == nil {
		fchown = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
	}
	base := filepath.Join(t.RunDir, ownerFolder)
	if err := ownedDir(base, os.ModeSetgid|0o710, func(f *os.File) error { return fchown(f, ServeUID, uid) }); err != nil {
		return "", err
	}
	return base, nil
}

// addOwner serves work's socket in the folder init made.
func (h *teamHub) addOwner(uid, gid int) error {
	base := filepath.Join(h.dir, ownerFolder)
	if err := checkRealDir(base); err != nil {
		return fmt.Errorf("work's run folder isn't ready (init makes it): %w", err)
	}
	sock := filepath.Join(base, "portenv.sock")
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove a stale socket: %w", err)
	}
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sock, err)
	}
	if err := os.Chmod(sock, 0o660); err != nil { // #nosec G302 -- the helper and work only
		_ = lis.Close()
		return err
	}
	if g := h.groupOf(sock); g != gid {
		_ = lis.Close()
		_ = os.Remove(sock)
		return fmt.Errorf("%s has group %d, not work's %d", sock, g, gid)
	}
	h.mu.Lock()
	if h.owner != nil {
		_ = h.owner.Close()
	}
	h.owner, h.ownerUID = lis, uid
	h.mu.Unlock()
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go h.handleOwner(conn.(*net.UnixConn))
		}
	}()
	return nil
}

// serveOwnerWhenReady serves work's socket once init has made its folder,
// trying for a while after the helper starts.
func (h *teamHub) serveOwnerWhenReady(uid int) {
	for i := 0; i < 60; i++ {
		if err := h.addOwner(uid, uid); err == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// pushDir is the API process's own folder for push bundles (0700).
func (h *teamHub) pushDir() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pushRoot == "" {
		if d, err := os.MkdirTemp("", "portenv-push-"); err == nil {
			h.pushRoot = d
		}
	}
	return h.pushRoot
}

// handleOwner answers one call on work's socket.
func (h *teamHub) handleOwner(conn *net.UnixConn) {
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

	br := bufio.NewReaderSize(idleReader{conn}, 64<<10)
	line, err := br.ReadSlice('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		refuse("Portenv couldn’t read the request.")
		return
	}
	uid, perr := h.peerUID(conn)
	h.mu.Lock()
	owner := h.ownerUID
	h.mu.Unlock()
	if perr != nil || uid != owner {
		refuse(notTheOwner)
		return
	}
	var c agentv1.TeamCallsResponse
	if err := protojson.Unmarshal(line, &c); err != nil {
		refuse("Portenv couldn’t read the request: " + err.Error())
		return
	}
	if c.GetPushList() == nil && c.GetPush() == nil && c.GetFetch() == nil {
		refuse("Only pushes and fetches are taken here.")
		return
	}
	c.CallId, c.AgentId, c.Owner = newCallID(), "", true
	if p := c.GetPush(); p != nil {
		p.BundleId = ""
		if n := p.GetBundleBytes(); n > 0 {
			if n > maxPushBundle {
				refuse(fmt.Sprintf("This push is too big for GitHub (over %s).", humanBytes(maxPushBundle)))
				return
			}
			id, err := h.keepBundle(br, n)
			if err != nil {
				refuse(err.Error())
				return
			}
			defer func() { _ = os.Remove(filepath.Join(h.pushDir(), id)) }()
			p.BundleId = id
		}
	}
	_ = conn.CloseRead()
	r := h.relayWait(&c, ownerCallWait)
	// A fetch's bundle follows its answer, once (R-0392).
	if id := r.GetFetch().GetBundleId(); c.GetFetch() != nil && id != "" {
		path := filepath.Join(h.pushDir(), id)
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- a checked name in the API process's own folder
		if !pushBundleIDRE.MatchString(id) || err != nil {
			refuse("Portenv lost the fetch\u2019s bundle. Fetch again.")
			return
		}
		_ = os.Remove(path)
		defer func() { _ = f.Close() }()
		r.GetFetch().BundleId = ""
		reply(r)
		_ = conn.SetWriteDeadline(time.Time{})
		_, _ = io.Copy(idleWriter{conn}, f)
		return
	}
	reply(r)
}

// idleWriter writes conn with a deadline per write: each waits at most
// ownerIdle for git-remote-portenv to take the bytes.
type idleWriter struct{ conn *net.UnixConn }

func (w idleWriter) Write(p []byte) (int, error) {
	_ = w.conn.SetWriteDeadline(time.Now().Add(ownerIdle))
	return w.conn.Write(p)
}

// WriteFetchBundle keeps the bundle portenvd fetched on the Mac until the
// owner's socket hands it to git-remote-portenv.
func (s *apiServer) WriteFetchBundle(stream agentv1.AgentService_WriteFetchBundleServer) error {
	dir := s.team.pushDir()
	if dir == "" {
		return status.Error(codes.Internal, "no folder for the fetch")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(raw[:])
	path := filepath.Join(dir, id)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- a fresh name in the API process's own folder
	if err != nil {
		return status.Errorf(codes.Internal, "keep the fetch: %v", err)
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	var n int64
	for {
		m, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(err)
		}
		n += int64(len(m.GetChunk()))
		if n > maxPushBundle {
			return fail(status.Errorf(codes.ResourceExhausted, "the fetch is over %s", humanBytes(maxPushBundle)))
		}
		if _, err := f.Write(m.GetChunk()); err != nil {
			return fail(err)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return stream.SendAndClose(&agentv1.WriteFetchBundleResponse{BundleId: id})
}

// keepBundle writes exactly n bytes from r to a new 0600 file in pushDir.
func (h *teamHub) keepBundle(r io.Reader, n int64) (string, error) {
	dir := h.pushDir()
	if dir == "" {
		return "", errors.New("Portenv couldn’t keep the push’s bundle") //nolint:staticcheck // ST1005: a plain line
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	f, err := os.OpenFile(filepath.Join(dir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- a fresh name in the API process's own folder
	if err != nil {
		return "", err
	}
	got, err := io.Copy(f, io.LimitReader(r, n))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || got != n {
		_ = os.Remove(filepath.Join(dir, id))
		return "", errors.New("The push’s bundle came short. Push again.") //nolint:staticcheck // ST1005: a plain line
	}
	return id, nil
}

// ReadPushBundle hands portenvd a push's bundle, once.
func (s *apiServer) ReadPushBundle(req *agentv1.ReadPushBundleRequest, stream agentv1.AgentService_ReadPushBundleServer) error {
	id := req.GetBundleId()
	if !pushBundleIDRE.MatchString(id) {
		return status.Error(codes.InvalidArgument, "no such push")
	}
	path := filepath.Join(s.team.pushDir(), id)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- a checked name in the API process's own folder
	if err != nil {
		return status.Error(codes.NotFound, "no such push")
	}
	_ = os.Remove(path) // once: the open file is still read
	defer func() { _ = f.Close() }()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if err := stream.Send(&agentv1.ReadPushBundleResponse{Chunk: buf[:n]}); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return status.Errorf(codes.Internal, "read the push: %v", rerr)
		}
	}
}

// idleReader reads conn with a deadline per read: each read waits at most
// ownerIdle for bytes.
type idleReader struct{ conn *net.UnixConn }

func (r idleReader) Read(p []byte) (int, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(ownerIdle))
	return r.conn.Read(p)
}
