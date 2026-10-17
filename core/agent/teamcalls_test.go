// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// newTestHub returns a hub in a short temporary folder (Unix socket paths
// are limited to about 100 bytes), with chown recorded and the peer's uid
// taken from peer.
func newTestHub(t *testing.T, peer *int) (*teamHub, *[]chownCall) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var mu sync.Mutex
	var chowns []chownCall
	h := newTeamHub(dir)
	h.chown = func(path string, uid, gid int) error {
		mu.Lock()
		defer mu.Unlock()
		chowns = append(chowns, chownCall{path, uid, gid})
		return nil
	}
	h.peerUID = func(*net.UnixConn) (int, error) { return *peer, nil }
	t.Cleanup(h.close)
	return h, &chowns
}

// fakePortenvd attaches to the hub as portenvd's TeamCalls stream would,
// and answers each call with answer.
type fakePortenvd struct {
	calls   chan *agentv1.TeamCallsResponse
	replies chan *agentv1.TeamCallsRequest
}

func attachFake(t *testing.T, ctx context.Context, h *teamHub) *fakePortenvd {
	t.Helper()
	f := &fakePortenvd{calls: make(chan *agentv1.TeamCallsResponse, 8), replies: make(chan *agentv1.TeamCallsRequest, 8)}
	attached := make(chan struct{})
	go func() {
		_ = h.attach(ctx,
			func(c *agentv1.TeamCallsResponse) error { f.calls <- c; return nil },
			func() (*agentv1.TeamCallsRequest, error) {
				select {
				case r := <-f.replies:
					return r, nil
				case <-ctx.Done():
					return nil, io.EOF
				}
			},
			func() { close(attached) })
	}()
	select {
	case <-attached:
	case <-time.After(5 * time.Second):
		t.Fatal("attach didn't start")
	}
	return f
}

// teamCall sends one line to an agent's socket and returns the reply line.
func teamCall(t *testing.T, h *teamHub, id, line string) *agentv1.TeamCallsRequest {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(h.dir, id, "portenv.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		t.Fatal(err)
	}
	// As the CLI does: the line, then the write side closed.
	_ = conn.(*net.UnixConn).CloseWrite()
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	var r agentv1.TeamCallsRequest
	if err := protojson.Unmarshal([]byte(reply), &r); err != nil {
		t.Fatalf("reply %q: %v", reply, err)
	}
	return &r
}

func TestAnAgentsCallReachesPortenvdAsThatAgent(t *testing.T) {
	uid := 2001
	h, _ := newTestHub(t, &uid)
	if err := h.addAgent("builder", 2001, 2001); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pd := attachFake(t, ctx, h)
	go func() {
		c := <-pd.calls
		if c.GetAgentId() != "builder" {
			pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: "wrong caller " + c.GetAgentId()}
			return
		}
		if c.GetMsg().GetText() != "ready for review" || c.GetCallId() == "" || c.GetCallId() == "mine" {
			pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: "wrong call"}
			return
		}
		pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Ok: true}
	}()
	// The request names another agent and its own call id; both are ignored.
	r := teamCall(t, h, "builder", `{"callId":"mine","agentId":"lead","msg":{"to":"reviewer","text":"ready for review"}}`)
	if !r.GetOk() {
		t.Fatalf("reply = %v", r)
	}
}

func TestAnUnknownUIDIsRefused(t *testing.T) {
	uid := 4242 // not builder's
	h, _ := newTestHub(t, &uid)
	if err := h.addAgent("builder", 2001, 2001); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pd := attachFake(t, ctx, h)
	r := teamCall(t, h, "builder", `{"whoami":{}}`)
	if r.GetOk() || !strings.Contains(r.GetLine(), "isn't an agent") {
		t.Errorf("reply = %v, want a refusal", r)
	}
	select {
	case c := <-pd.calls:
		t.Errorf("relayed a call from an unknown uid: %v", c)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAnotherAgentsUIDOnThisSocketIsRefused(t *testing.T) {
	uid := 2002 // reviewer, on builder's socket
	h, _ := newTestHub(t, &uid)
	for id, n := range map[string]int{"builder": 2001, "reviewer": 2002} {
		if err := h.addAgent(id, n, n); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pd := attachFake(t, ctx, h)
	r := teamCall(t, h, "builder", `{"whoami":{}}`)
	if r.GetOk() {
		t.Errorf("reply = %v, want a refusal", r)
	}
	select {
	case c := <-pd.calls:
		t.Errorf("relayed a call on the wrong socket: %v", c)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestACallWithoutPortenvdSaysSo(t *testing.T) {
	uid := 2001
	h, _ := newTestHub(t, &uid)
	if err := h.addAgent("builder", 2001, 2001); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := teamCall(t, h, "builder", `{"whoami":{}}`)
	if r.GetOk() || !strings.Contains(r.GetLine(), "isn't connected") {
		t.Errorf("reply = %v, want \"isn't connected\"", r)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v to say so", time.Since(start))
	}
}

func TestACallInFlightWhenPortenvdGoesAwayIsAnswered(t *testing.T) {
	uid := 2001
	h, _ := newTestHub(t, &uid)
	if err := h.addAgent("builder", 2001, 2001); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pd := attachFake(t, ctx, h)
	go func() {
		<-pd.calls
		cancel() // portenvd's stream ends before it answers
	}()
	r := teamCall(t, h, "builder", `{"whoami":{}}`)
	if r.GetOk() || !strings.Contains(r.GetLine(), "lost the connection") {
		t.Errorf("reply = %v, want \"lost the connection\"", r)
	}
}

func TestBadRequestsAreRefused(t *testing.T) {
	uid := 2001
	h, _ := newTestHub(t, &uid)
	if err := h.addAgent("builder", 2001, 2001); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pd := attachFake(t, ctx, h)
	for name, line := range map[string]string{
		"not JSON":  "{nope",
		"no op":     `{}`,
		"too large": `{"msg":{"text":"` + strings.Repeat("x", maxTeamCallBytes) + `"}}`,
		"unknown":   `{"sudo":{}}`,
	} {
		if r := teamCall(t, h, "builder", line); r.GetOk() || r.GetLine() == "" {
			t.Errorf("%s: reply = %v, want a refusal with a line", name, r)
		}
	}
	select {
	case c := <-pd.calls:
		t.Errorf("relayed a bad request: %v", c)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAnAgentsFoldersAreItsOwn(t *testing.T) {
	uid := 2001
	h, chowns := newTestHub(t, &uid)
	if err := h.addAgent("builder", 2001, 2001); err != nil {
		t.Fatal(err)
	}
	modes := map[string]os.FileMode{
		"builder":              0o710,
		"builder/portenv.sock": 0o660,
		"builder/tmux":         0o700,
		"builder/tmp":          0o700,
	}
	for rel, want := range modes {
		info, err := os.Lstat(filepath.Join(h.dir, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: mode %o, want %o", rel, info.Mode().Perm(), want)
		}
	}
	for _, want := range []chownCall{
		{filepath.Join(h.dir, "builder"), -1, 2001},
		{filepath.Join(h.dir, "builder", "portenv.sock"), -1, 2001},
		{filepath.Join(h.dir, "builder", "tmux"), 2001, 2001},
		{filepath.Join(h.dir, "builder", "tmp"), 2001, 2001},
	} {
		if !slices.Contains(*chowns, want) {
			t.Errorf("missing chown %v in %v", want, *chowns)
		}
	}
	if got := h.TMPDIR("builder"); got != filepath.Join(h.dir, "builder", "tmp") {
		t.Errorf("TMPDIR = %q", got)
	}
}

func TestRemovingAnAgentClosesItsSocket(t *testing.T) {
	uid := 2001
	h, _ := newTestHub(t, &uid)
	if err := h.addAgent("builder", 2001, 2001); err != nil {
		t.Fatal(err)
	}
	h.removeAgent("builder")
	if _, err := net.Dial("unix", filepath.Join(h.dir, "builder", "portenv.sock")); err == nil {
		t.Error("the socket still answers")
	}
	if err := h.addAgent("bad/id", 2002, 2002); !errors.Is(err, ErrInvalidAgentID) {
		t.Errorf("addAgent(bad/id) = %v, want ErrInvalidAgentID", err)
	}
}
