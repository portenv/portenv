// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// work's own socket (R-0391): git-remote-portenv's list and push, taken
// only from work (SO_PEERCRED), relayed to portenvd as the owner's calls;
// the push's bundle is kept for ReadPushBundle, and read once.

// addTestOwner makes work's run folder as init does, then has the hub
// serve it.
func addTestOwner(t *testing.T, h *teamHub, uid int) {
	t.Helper()
	tu := &TeamUsers{RunDir: h.dir, fchown: func(*os.File, int, int) error { return nil }}
	base, err := tu.ensureOwnerRunDir(uid)
	if err != nil {
		t.Fatal(err)
	}
	testRunGIDs.Store(base, uid)
	if err := h.addOwner(uid, uid); err != nil {
		t.Fatal(err)
	}
}

func ownerCall(t *testing.T, h *teamHub, line string, body []byte) *agentv1.TeamCallsRequest {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(h.dir, "work", "portenv.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(body); err != nil {
		t.Fatal(err)
	}
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

func TestOwnersListReachesPortenvdAsTheOwner(t *testing.T) {
	uid := 1000
	h := newTestHub(t, &uid)
	addTestOwner(t, h, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pd := attachFake(t, ctx, h)
	go func() {
		c := <-pd.calls
		if !c.GetOwner() || c.GetAgentId() != "" || c.GetPushList() == nil {
			pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: "wrong call"}
			return
		}
		pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Ok: true}
	}()
	// The request claims to be an agent's; it's the owner's all the same.
	if r := ownerCall(t, h, `{"agentId":"lead","pushList":{}}`, nil); !r.GetOk() {
		t.Fatalf("%v", r)
	}
}

// Only work: an agent's uid on work's socket is refused before anything
// reaches portenvd.
func TestAnAgentCantPush(t *testing.T) {
	uid := 2001
	h := newTestHub(t, &uid)
	addTestOwner(t, h, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pd := attachFake(t, ctx, h)
	r := ownerCall(t, h, `{"pushList":{}}`, nil)
	if r.GetOk() || r.GetLine() != "Only you can push from this workspace. Agents can’t push." {
		t.Fatalf("%v", r)
	}
	select {
	case c := <-pd.calls:
		t.Fatalf("reached portenvd: %v", c)
	default:
	}
}

// Agents' calls aren't taken on work's socket, and the owner's push ops
// aren't taken on an agent's.
func TestOnlyPushOnTheOwnersSocket(t *testing.T) {
	uid := 1000
	h := newTestHub(t, &uid)
	addTestOwner(t, h, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attachFake(t, ctx, h)
	if r := ownerCall(t, h, `{"msg":{"to":"lead","text":"hi"}}`, nil); r.GetOk() {
		t.Fatalf("an agent's call on work's socket: %v", r)
	}
	agentUID := 2001
	h2 := newTestHub(t, &agentUID)
	if err := addTestAgent(t, h2, "builder", 2001); err != nil {
		t.Fatal(err)
	}
	attachFake(t, ctx, h2)
	if r := teamCall(t, h2, "builder", `{"pushList":{}}`); r.GetOk() || !strings.Contains(r.GetLine(), "Agents can’t push") {
		t.Fatalf("a push on an agent's socket: %v", r)
	}
}

// A push's bundle: kept while portenvd reads it, once; capped; gone after.
func TestAPushsBundleIsReadOnce(t *testing.T) {
	uid := 1000
	h := newTestHub(t, &uid)
	addTestOwner(t, h, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pd := attachFake(t, ctx, h)
	s := &apiServer{team: h}
	bundle := bytes.Repeat([]byte("b"), 100_000)
	got := make(chan []byte, 1)
	go func() {
		c := <-pd.calls
		id := c.GetPush().GetBundleId()
		var b []byte
		st := &pushBundleStream{ctx: ctx, send: func(r *agentv1.ReadPushBundleResponse) error { b = append(b, r.GetChunk()...); return nil }}
		if err := s.ReadPushBundle(&agentv1.ReadPushBundleRequest{BundleId: id}, st); err != nil {
			pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: err.Error()}
			return
		}
		// Once: a second read finds nothing.
		if err := s.ReadPushBundle(&agentv1.ReadPushBundleRequest{BundleId: id}, st); err == nil {
			pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Line: "read twice"}
			return
		}
		got <- b
		pd.replies <- &agentv1.TeamCallsRequest{CallId: c.GetCallId(), Ok: true}
	}()
	r := ownerCall(t, h, `{"push":{"updates":[{"dst":"refs/heads/main","newSha":"`+strings.Repeat("a", 40)+`"}],"bundleBytes":"100000","bundleId":"../../etc/passwd"}}`, bundle)
	if !r.GetOk() {
		t.Fatalf("%v", r)
	}
	if b := <-got; !bytes.Equal(b, bundle) {
		t.Fatalf("read %d bytes", len(b))
	}
	if ents, _ := os.ReadDir(h.pushDir()); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
	// Over the cap: refused before portenvd hears of it.
	old := maxPushBundle
	maxPushBundle = 10
	defer func() { maxPushBundle = old }()
	if r := ownerCall(t, h, `{"push":{"updates":[{"dst":"refs/heads/main","newSha":"`+strings.Repeat("a", 40)+`"}],"bundleBytes":"11"}}`, bytes.Repeat([]byte("b"), 11)); r.GetOk() || !strings.Contains(r.GetLine(), "too big") {
		t.Fatalf("over the cap: %v", r)
	}
	// Fewer bytes than it said: refused.
	if r := ownerCall(t, h, `{"push":{"updates":[{"dst":"refs/heads/main","newSha":"`+strings.Repeat("a", 40)+`"}],"bundleBytes":"8"}}`, []byte("abc")); r.GetOk() {
		t.Fatalf("a short bundle: %v", r)
	}
}

func TestInitMakesWorksRunFolder(t *testing.T) {
	dir := t.TempDir()
	var chowns []chownCall
	tu := &TeamUsers{RunDir: dir, fchown: func(f *os.File, uid, gid int) error {
		chowns = append(chowns, chownCall{f.Name(), uid, gid})
		return nil
	}}
	base, err := tu.ensureOwnerRunDir(1000)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(base)
	if err != nil || base != filepath.Join(dir, "work") || fi.Mode().Perm() != 0o710 || fi.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("%s %v %v", base, fi.Mode(), err)
	}
	if len(chowns) != 1 || chowns[0].uid != ServeUID || chowns[0].gid != 1000 {
		t.Fatalf("chowns %v", chowns)
	}
}

// pushBundleStream is ReadPushBundle's stream, collecting what's sent.
type pushBundleStream struct {
	ctx  context.Context
	send func(*agentv1.ReadPushBundleResponse) error
}

func (p *pushBundleStream) Send(r *agentv1.ReadPushBundleResponse) error { return p.send(r) }
func (p *pushBundleStream) Context() context.Context                     { return p.ctx }
func (p *pushBundleStream) SetHeader(metadata.MD) error                  { return nil }
func (p *pushBundleStream) SendHeader(metadata.MD) error                 { return nil }
func (p *pushBundleStream) SetTrailer(metadata.MD)                       {}
func (p *pushBundleStream) SendMsg(any) error                            { return nil }
func (p *pushBundleStream) RecvMsg(any) error                            { return nil }
