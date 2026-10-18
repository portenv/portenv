// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// newTestHub returns a hub in a short temporary folder (Unix socket paths
// are limited to about 100 bytes), with the peer's uid taken from peer.
// Its agents' run folders are made by addTestAgent, as init makes them
// but without the chown (tests don't run as root), and a socket's group
// reads as the agent's when it's its folder's group (the setgid bit's
// effect); TestTheSocketTakesItsFoldersGroup checks the real thing.
func newTestHub(t *testing.T, peer *int) *teamHub {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h := newTeamHub(dir)
	h.peerUID = func(*net.UnixConn) (int, error) { return *peer, nil }
	h.groupOf = func(p string) int {
		if g, ok := testRunGIDs.Load(filepath.Dir(p)); ok && fileGroup(p) == fileGroup(filepath.Dir(p)) {
			return g.(int)
		}
		return -1
	}
	t.Cleanup(h.close)
	return h
}

// testRunGIDs is each test run folder's agent gid, for newTestHub.
var testRunGIDs sync.Map

// makeTestRunDir makes an agent's run folder in dir as init does
// (ensureRunDir), with chown recorded, not performed.
func makeTestRunDir(t *testing.T, dir, id string, uid int) []chownCall {
	t.Helper()
	var chowns []chownCall
	tu := &TeamUsers{RunDir: dir, fchown: func(f *os.File, uid, gid int) error {
		chowns = append(chowns, chownCall{f.Name(), uid, gid})
		return nil
	}}
	base, err := tu.ensureRunDir(agentUserRecord{ID: id, UID: uid})
	if err != nil {
		t.Fatal(err)
	}
	testRunGIDs.Store(base, uid)
	return chowns
}

// addTestAgent makes the agent's run folder, then has the hub serve it.
func addTestAgent(t *testing.T, h *teamHub, id string, uid int) error {
	t.Helper()
	if agentIDRE.MatchString(id) {
		makeTestRunDir(t, h.dir, id, uid)
	}
	return h.addAgent(id, uid, uid)
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
	h := newTestHub(t, &uid)
	if err := addTestAgent(t, h, "builder", 2001); err != nil {
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
	h := newTestHub(t, &uid)
	if err := addTestAgent(t, h, "builder", 2001); err != nil {
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
	h := newTestHub(t, &uid)
	for id, n := range map[string]int{"builder": 2001, "reviewer": 2002} {
		if err := addTestAgent(t, h, id, n); err != nil {
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
	h := newTestHub(t, &uid)
	if err := addTestAgent(t, h, "builder", 2001); err != nil {
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
	h := newTestHub(t, &uid)
	if err := addTestAgent(t, h, "builder", 2001); err != nil {
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
	h := newTestHub(t, &uid)
	if err := addTestAgent(t, h, "builder", 2001); err != nil {
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

func TestTheHubServesTheSocketInInitsFolder(t *testing.T) {
	uid := 2001
	h := newTestHub(t, &uid)
	if err := addTestAgent(t, h, "builder", 2001); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(h.dir, "builder", "portenv.sock"))
	if err != nil || info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o660 {
		t.Errorf("socket: %v, %v; want a 0660 socket", info, err)
	}
	if got := h.TMPDIR("builder"); got != filepath.Join(h.dir, "builder", "tmp") {
		t.Errorf("TMPDIR = %q", got)
	}
}

// The helper makes no folders: without init's run folder, it serves nothing
// and leaves nothing behind (the owner demo, 2026-10-18: it tried, and had
// no right to, in root's /run/portenv-team).
func TestTheHubMakesNoFolders(t *testing.T) {
	uid := 2001
	h := newTestHub(t, &uid)
	err := h.addAgent("builder", 2001, 2001)
	if err == nil || !strings.Contains(err.Error(), "init makes it") {
		t.Errorf("addAgent without a run folder = %v, want a refusal naming init", err)
	}
	if _, err := os.Lstat(filepath.Join(h.dir, "builder")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the hub made a folder: %v", err)
	}
}

// A socket whose group isn't the agent's is never served.
func TestTheHubRefusesASocketWithAnotherGroup(t *testing.T) {
	uid := 2001
	h := newTestHub(t, &uid)
	makeTestRunDir(t, h.dir, "builder", 2001)
	h.groupOf = func(string) int { return 2002 }
	if err := h.addAgent("builder", 2001, 2001); err == nil || !strings.Contains(err.Error(), "setgid") {
		t.Errorf("addAgent = %v, want a refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(h.dir, "builder", "portenv.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused socket is still there: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.agents) != 0 || len(h.byUID) != 0 {
		t.Errorf("serving %v", h.agents)
	}
}

// The real setgid effect, with a group this test's user may give a
// folder: the socket the hub makes takes the folder's group with no chown,
// and the hub's real group check accepts it.
func TestTheSocketTakesItsFoldersGroup(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	gid := -1
	for _, g := range groups {
		if g != os.Getgid() {
			gid = g
			break
		}
	}
	if gid < 0 {
		t.Skip("this user has no second group to give a folder")
	}
	uid := 2001
	h := newTestHub(t, &uid)
	makeTestRunDir(t, h.dir, "builder", 2001)
	base := filepath.Join(h.dir, "builder")
	if err := os.Lchown(base, -1, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, os.ModeSetgid|0o710); err != nil {
		t.Fatal(err)
	}
	h.groupOf = fileGroup
	if err := h.addAgent("builder", 2001, gid); err != nil {
		t.Fatal(err)
	}
	if g := fileGroup(filepath.Join(base, "portenv.sock")); g != gid {
		t.Errorf("socket group %d, want %d", g, gid)
	}
}

// The API process gives nothing to another uid and makes no folders: its
// team files call no chown, mkdir or lchown at all (init does that work).
func TestTheAPIProcessTeamCodeNeverChowns(t *testing.T) {
	banned := map[string]bool{"Chown": true, "Lchown": true, "Fchown": true, "Mkdir": true, "MkdirAll": true, "Fchownat": true}
	for _, file := range []string{"teamcalls.go", "teamstart.go", "teamstart_linux.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && banned[sel.Sel.Name] {
				t.Errorf("%s uses %s: the API process may not; init makes and owns the agent's folders", file, sel.Sel.Name)
			}
			return true
		})
	}
}

func TestRemovingAnAgentClosesItsSocket(t *testing.T) {
	uid := 2001
	h := newTestHub(t, &uid)
	if err := addTestAgent(t, h, "builder", 2001); err != nil {
		t.Fatal(err)
	}
	h.removeAgent("builder")
	if _, err := net.Dial("unix", filepath.Join(h.dir, "builder", "portenv.sock")); err == nil {
		t.Error("the socket still answers")
	}
	if err := addTestAgent(t, h, "bad/id", 2002); !errors.Is(err, ErrInvalidAgentID) {
		t.Errorf("addAgent(bad/id) = %v, want ErrInvalidAgentID", err)
	}
}
