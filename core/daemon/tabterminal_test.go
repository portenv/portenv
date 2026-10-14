// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/portenv/portenv/core/local"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// fakeTabHelper is one workspace's helper: it answers GetVersion as boxID
// and records every TabTerminal it's given, what it was sent, and plays
// script back.
type fakeTabHelper struct {
	agentv1.UnimplementedAgentServiceServer
	boxID  string
	script []*agentv1.TabTerminalResponse

	mu       sync.Mutex
	versions int // GetVersion calls
	opens    []*agentv1.TabTerminalOpen
	inputs   []*agentv1.TabTerminalRequest
	done     chan struct{}
}

func (f *fakeTabHelper) GetVersion(context.Context, *agentv1.GetVersionRequest) (*agentv1.GetVersionResponse, error) {
	f.mu.Lock()
	f.versions++
	f.mu.Unlock()
	return &agentv1.GetVersionResponse{BoxId: f.boxID}, nil
}

func (f *fakeTabHelper) TabTerminal(stream agentv1.AgentService_TabTerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.opens = append(f.opens, first.GetOpen())
	f.mu.Unlock()
	go func() {
		for {
			r, err := stream.Recv()
			if err != nil {
				close(f.done)
				return
			}
			f.mu.Lock()
			f.inputs = append(f.inputs, r)
			f.mu.Unlock()
		}
	}()
	for _, m := range f.script {
		if err := stream.Send(m); err != nil {
			return err
		}
	}
	<-f.done
	return nil
}

func (f *fakeTabHelper) seen() ([]*agentv1.TabTerminalOpen, []*agentv1.TabTerminalRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentv1.TabTerminalOpen(nil), f.opens...), append([]*agentv1.TabTerminalRequest(nil), f.inputs...)
}

func (f *fakeTabHelper) contacted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.versions + len(f.opens)
}

func (f *fakeTabHelper) client(t *testing.T) agentv1.AgentServiceClient {
	t.Helper()
	f.done = make(chan struct{})
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///helper",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentv1.NewAgentServiceClient(conn)
}

// tabWorld is a daemon with two workspaces: acme-api (box-a) open on this
// Mac and web (box-b) open on a server, each with its own helper. who is
// the caller every connection gets.
type tabWorld struct {
	a, b *fakeTabHelper
	c    daemonv1.DaemonServiceClient
}

func servingTabTerminals(t *testing.T, who caller, a, b *fakeTabHelper) tabWorld {
	t.Helper()
	t.Setenv("PORTENV_KEYS", "file")
	ac, bc := a.client(t), b.client(t)
	_, c := servingWith(t, func(s *Server) {
		for _, cfg := range []local.BoxConfig{{ID: "box-a", Name: "acme-api", Image: "img"}, {ID: "box-b", Name: "web", Image: "img"}} {
			if err := s.env.SaveBox(cfg); err != nil {
				t.Fatal(err)
			}
		}
		s.open["acme-api"] = &openBox{
			closed: true, // nothing to save at shutdown; tab calls need only the helper
			sess:   &local.Session{Cfg: local.BoxConfig{ID: "box-a", Name: "acme-api"}},
			agent:  ac,
		}
		s.remote["web"] = &remoteBox{host: "dev@server", agent: bc}
		s.identify = func(net.Conn) caller { return who }
	})
	return tabWorld{a: a, b: b, c: c}
}

func openTab(ctx context.Context, t *testing.T, c daemonv1.DaemonServiceClient, open *daemonv1.TabTerminalOpen) daemonv1.DaemonService_TabTerminalClient {
	t.Helper()
	st, err := c.TabTerminal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(&daemonv1.TabTerminalRequest{Msg: &daemonv1.TabTerminalRequest_Open{Open: open}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func resetFirst() []*agentv1.TabTerminalResponse {
	return []*agentv1.TabTerminalResponse{{Msg: &agentv1.TabTerminalResponse_Reset_{Reset_: &agentv1.TabTerminalReset{
		Size: &agentv1.TerminalSize{Cols: 80, Rows: 24}, Seed: []byte("$ "),
	}}}}
}

var agentGrok = caller{kind: callerAgent, id: "grok-bot", box: "acme-api"}

// TestAnAgentsTabTerminalCantClaimTheApp (required, ADR 0020 decision 4,
// R-0172): an agent opens a tab saying it's the app, in request metadata
// and in a stray field 4 of the open (where the helper's own open carries
// author). The helper is told the agent's identity, never "app".
func TestAnAgentsTabTerminalCantClaimTheApp(t *testing.T) {
	w := servingTabTerminals(t, agentGrok, &fakeTabHelper{boxID: "box-a", script: resetFirst()}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("author", "app"))

	open := &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@1", Size: &daemonv1.TerminalSize{Cols: 80, Rows: 24}}
	open.ProtoReflect().SetUnknown(protowire.AppendString(protowire.AppendTag(nil, 4, protowire.BytesType), "app"))
	st := openTab(ctx, t, w.c, open)
	if _, err := st.Recv(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	opens, _ := w.a.seen()
	if len(opens) != 1 {
		t.Fatalf("the helper got %d opens, want 1", len(opens))
	}
	if got := opens[0].GetAuthor(); got != "agent:grok-bot" {
		t.Fatalf("author %q, want agent:grok-bot", got)
	}
	if opens[0].GetSession() != "main" || opens[0].GetTab() != "@1" || opens[0].GetSize().GetCols() != 80 {
		t.Fatalf("open relayed as %v", opens[0])
	}
	_ = st.CloseSend()
}

// The app's own open, with nothing claimed, is the app's.
func TestTheAppsTabTerminalIsTheApps(t *testing.T) {
	w := servingTabTerminals(t, ownerCaller, &fakeTabHelper{boxID: "box-a", script: resetFirst()}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@0"})
	if _, err := st.Recv(); err != nil {
		t.Fatal(err)
	}
	if opens, _ := w.a.seen(); len(opens) != 1 || opens[0].GetAuthor() != "app" {
		t.Fatalf("opens %v, want one with author app", opens)
	}
	_ = st.CloseSend()
}

// TestACallerCantOpenAnotherWorkspacesTab (required, R-0162): a caller
// limited to acme-api asks for web's tab, which is open on a server. It's
// refused with PermissionDenied, and neither helper is contacted at all,
// not even asked its version.
func TestACallerCantOpenAnotherWorkspacesTab(t *testing.T) {
	w := servingTabTerminals(t, agentGrok, &fakeTabHelper{boxID: "box-a"}, &fakeTabHelper{boxID: "box-b"})
	for _, box := range []string{"web", "WEB", "acme-api/../web", ""} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st := openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: box, Session: "main", Tab: "@0"})
		if _, err := st.Recv(); status.Code(err) != codes.PermissionDenied {
			cancel()
			t.Fatalf("box %q: %v, want PermissionDenied", box, err)
		}
		cancel()
	}
	if n := w.b.contacted(); n != 0 {
		t.Fatalf("web's helper was contacted %d times", n)
	}
	if n := w.a.contacted(); n != 0 {
		t.Fatalf("acme-api's helper was contacted %d times", n)
	}
}

// TestAChannelAnsweringAsAnotherBoxIsRefused (R-0172 point 4, R-0173): the
// route to web reaches a helper that answers as another box, or as none.
// The tab is refused and nothing is relayed: the helper never gets a
// TabTerminal.
func TestAChannelAnsweringAsAnotherBoxIsRefused(t *testing.T) {
	for answers, says := range map[string]string{
		"box-a":  "reached another workspace's helper",
		"box-bb": "reached another workspace's helper",
		"":       "didn't say which workspace it runs in",
	} {
		w := servingTabTerminals(t, ownerCaller, &fakeTabHelper{boxID: "box-a"}, &fakeTabHelper{boxID: answers, script: resetFirst()})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st := openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "web", Session: "main", Tab: "@0"})
		_, err := st.Recv()
		cancel()
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), says) {
			t.Fatalf("answering as %q: %v, want FailedPrecondition saying %q", answers, err, says)
		}
		if opens, _ := w.b.seen(); len(opens) != 0 {
			t.Fatalf("answering as %q: the helper got %d opens, want none", answers, len(opens))
		}
	}
	// The right box goes through, on the server's route too.
	w := servingTabTerminals(t, ownerCaller, &fakeTabHelper{boxID: "box-a"}, &fakeTabHelper{boxID: "box-b", script: resetFirst()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "web", Session: "main", Tab: "@0"})
	if _, err := st.Recv(); err != nil {
		t.Fatalf("box-b answering as box-b: %v", err)
	}
	_ = st.CloseSend()
}

// TestTabTerminalRelaysBothWays: every response kind comes back in order,
// with is_you set by portenvd for this caller; input and resize go to the
// helper in order; end ends the stream.
func TestTabTerminalRelaysBothWays(t *testing.T) {
	script := append(resetFirst(),
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Author{Author: &agentv1.CommandAuthor{Line: 3, Name: "Grok Bot", IsYou: true, AgentId: "grok-bot"}}},
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Output{Output: []byte("hello\r\n")}},
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Resized{Resized: &agentv1.TerminalSize{Cols: 100, Rows: 30}}},
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Author{Author: &agentv1.CommandAuthor{Line: 4, Name: "You"}}},
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_End{End: agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED}},
	)
	w := servingTabTerminals(t, ownerCaller, &fakeTabHelper{boxID: "box-a", script: script}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@0"})
	for _, b := range []string{"ls", "\r"} {
		if err := st.Send(&daemonv1.TabTerminalRequest{Msg: &daemonv1.TabTerminalRequest_Input{Input: []byte(b)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Send(&daemonv1.TabTerminalRequest{Msg: &daemonv1.TabTerminalRequest_Resize{Resize: &daemonv1.TerminalSize{Cols: 120, Rows: 40}}}); err != nil {
		t.Fatal(err)
	}

	var got []string
	for {
		r, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("after %v: %v", got, err)
		}
		switch v := r.GetMsg().(type) {
		case *daemonv1.TabTerminalResponse_Reset_:
			got = append(got, "reset "+string(v.Reset_.GetSeed()))
		case *daemonv1.TabTerminalResponse_Output:
			got = append(got, "output "+string(v.Output))
		case *daemonv1.TabTerminalResponse_Resized:
			got = append(got, "resized")
		case *daemonv1.TabTerminalResponse_Author:
			got = append(got, "author "+v.Author.GetName()+map[bool]string{true: " (you)", false: ""}[v.Author.GetIsYou()])
		case *daemonv1.TabTerminalResponse_End:
			got = append(got, "end "+v.End.String())
		}
	}
	want := []string{"reset $ ", "author Grok Bot", "output hello\r\n", "resized", "author You (you)", "end TAB_TERMINAL_END_TAB_CLOSED"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, in := w.a.seen()
		if len(in) == 3 {
			if string(in[0].GetInput()) != "ls" || string(in[1].GetInput()) != "\r" || in[2].GetResize().GetCols() != 120 {
				t.Fatalf("the helper got %v", in)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the helper got %d messages, want 3", len(in))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
