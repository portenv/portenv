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
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeTabHelper is one workspace's helper: it answers GetVersion as boxID
// and records every TabTerminal it's given, what it was sent, and plays
// script back.
type fakeTabHelper struct {
	agentv1.UnimplementedAgentServiceServer
	boxID  string
	script []*agentv1.TabTerminalResponse
	// endAfterInputs holds the script's end until this many input and
	// resize messages have arrived. Nothing follows an end, so the relay
	// stops forwarding input once it passes one on: a test that checks
	// what reached the helper must not let the end overtake it.
	endAfterInputs int

	mu       sync.Mutex
	versions int // GetVersion calls
	opens    []*agentv1.TabTerminalOpen
	inputs   []*agentv1.TabTerminalRequest
	done     chan struct{}
	// arrived is signalled after each input or resize is recorded.
	arrived chan struct{}
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
			select {
			case f.arrived <- struct{}{}:
			default:
			}
		}
	}()
	for _, m := range f.script {
		if _, isEnd := m.GetMsg().(*agentv1.TabTerminalResponse_End); isEnd {
			if err := f.waitForInputs(stream.Context()); err != nil {
				return err
			}
		}
		if err := stream.Send(m); err != nil {
			return err
		}
	}
	<-f.done
	return nil
}

// waitForInputs waits until endAfterInputs messages have arrived, or fails
// after 5 s so a test that never sends them says so.
func (f *fakeTabHelper) waitForInputs(ctx context.Context) error {
	timeout := time.After(5 * time.Second)
	for {
		if _, in := f.seen(); len(in) >= f.endAfterInputs {
			return nil
		}
		select {
		case <-f.arrived:
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			_, in := f.seen()
			return status.Errorf(codes.DeadlineExceeded, "the helper got %d of %d messages before its end", len(in), f.endAfterInputs)
		}
	}
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
	f.arrived = make(chan struct{}, 1)
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

var agentGrok = caller{kind: callerAgent, id: "grok-bot", name: "Grok Bot", box: "acme-api"}

// TestAnAgentsTabTerminalCantClaimTheApp (required, ADR 0020 decision 4,
// R-0172): an agent opens a tab saying it's the app, in request metadata
// and in a stray field 4 of the open (where the helper's own open carries
// author). The helper is told the agent's name and ID (R-0178), never the
// owner's.
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
	if got, id := opens[0].GetAuthor(), opens[0].GetAuthorAgentId(); got != "Grok Bot" || id != "grok-bot" {
		t.Fatalf("author %q (%q), want Grok Bot (grok-bot)", got, id)
	}
	if opens[0].GetSession() != "main" || opens[0].GetTab() != "@1" || opens[0].GetSize().GetCols() != 80 {
		t.Fatalf("open relayed as %v", opens[0])
	}
	_ = st.CloseSend()
}

// The app's own open, with nothing claimed, is the owner's: their name,
// never empty or "You", and no agent ID (R-0178).
func TestTheAppsTabTerminalIsTheApps(t *testing.T) {
	w := servingTabTerminals(t, ownerCaller(), &fakeTabHelper{boxID: "box-a", script: resetFirst()}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@0"})
	if _, err := st.Recv(); err != nil {
		t.Fatal(err)
	}
	opens, _ := w.a.seen()
	if len(opens) != 1 {
		t.Fatalf("opens %v, want one", opens)
	}
	if name := opens[0].GetAuthor(); name == "" || name != ownerName() || strings.EqualFold(name, "you") || opens[0].GetAuthorAgentId() != "" {
		t.Fatalf("the app's open: author %q (%q), want %q with no agent ID", name, opens[0].GetAuthorAgentId(), ownerName())
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
		w := servingTabTerminals(t, ownerCaller(), &fakeTabHelper{boxID: "box-a"}, &fakeTabHelper{boxID: answers, script: resetFirst()})
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
	w := servingTabTerminals(t, ownerCaller(), &fakeTabHelper{boxID: "box-a"}, &fakeTabHelper{boxID: "box-b", script: resetFirst()})
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
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Author{Author: &agentv1.CommandAuthor{Line: 4, Name: "Retnan"}}},
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_End{End: agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED}},
	)
	// The end waits for the three messages below: in a real tab, the
	// program reads them before the tab closes.
	w := servingTabTerminals(t, ownerCaller(), &fakeTabHelper{boxID: "box-a", script: script, endAfterInputs: 3}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	want := []string{"reset $ ", "author Grok Bot", "output hello\r\n", "resized", "author Retnan (you)", "end TAB_TERMINAL_END_TAB_CLOSED"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}

	// The end came after all three arrived, so they're all recorded now.
	_, in := w.a.seen()
	if len(in) != 3 || string(in[0].GetInput()) != "ls" || string(in[1].GetInput()) != "\r" || in[2].GetResize().GetCols() != 120 {
		t.Fatalf("the helper got %v, want ls, \\r, then a resize to 120 columns", in)
	}
}

// relayed collects one stream's responses until it ends.
func relayed(t *testing.T, st daemonv1.DaemonService_TabTerminalClient) []*daemonv1.TabTerminalResponse {
	t.Helper()
	var got []*daemonv1.TabTerminalResponse
	for {
		r, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatalf("after %d messages: %v", len(got), err)
		}
		got = append(got, r)
	}
}

func endsClosed() *agentv1.TabTerminalResponse {
	return &agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_End{End: agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED}}
}

// TestAnAgentsCommandIsYouOnlyToThatAgent (R-0178): a command the agent
// ran comes back is_you=false to the app and true to that agent; the
// owner's command, the other way round. Whatever is_you the helper sent
// is ignored.
func TestAnAgentsCommandIsYouOnlyToThatAgent(t *testing.T) {
	byGrok := &agentv1.CommandAuthor{Line: 7, Name: "Grok Bot", AgentId: "grok-bot", IsYou: true}
	byOwner := &agentv1.CommandAuthor{Line: 8, Name: ownerName(), IsYou: true}
	script := []*agentv1.TabTerminalResponse{
		{Msg: &agentv1.TabTerminalResponse_Reset_{Reset_: &agentv1.TabTerminalReset{Seed: []byte("$ "), Authors: []*agentv1.CommandAuthor{byGrok, byOwner}}}},
		{Msg: &agentv1.TabTerminalResponse_Author{Author: byGrok}},
		{Msg: &agentv1.TabTerminalResponse_Author{Author: byOwner}},
		endsClosed(),
	}
	for _, c := range []struct {
		who             caller
		grokYou, ownYou bool
	}{
		{ownerCaller(), false, true},
		{agentGrok, true, false},
		{caller{kind: callerAgent, id: "other-bot", name: "Other Bot", box: "acme-api"}, false, false},
	} {
		w := servingTabTerminals(t, c.who, &fakeTabHelper{boxID: "box-a", script: script}, &fakeTabHelper{boxID: "box-b"})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got := relayed(t, openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@0"}))
		cancel()
		if len(got) != 4 {
			t.Fatalf("%s: %d messages, want 4", c.who.name, len(got))
		}
		ra := got[0].GetReset_().GetAuthors()
		if len(ra) != 2 || ra[0].GetIsYou() != c.grokYou || ra[1].GetIsYou() != c.ownYou {
			t.Fatalf("%s: reset authors %v", c.who.name, ra)
		}
		if got[1].GetAuthor().GetIsYou() != c.grokYou || got[1].GetAuthor().GetName() != "Grok Bot" || got[1].GetAuthor().GetAgentId() != "grok-bot" {
			t.Fatalf("%s: Grok's command came back as %v", c.who.name, got[1].GetAuthor())
		}
		if got[2].GetAuthor().GetIsYou() != c.ownYou {
			t.Fatalf("%s: the owner's command came back as %v", c.who.name, got[2].GetAuthor())
		}
	}
}

// TestSkippedArrives (R-0178): a reset the helper marks skipped reaches the
// app marked skipped, and one that isn't, isn't.
func TestSkippedArrives(t *testing.T) {
	script := []*agentv1.TabTerminalResponse{
		{Msg: &agentv1.TabTerminalResponse_Reset_{Reset_: &agentv1.TabTerminalReset{Seed: []byte("a")}}},
		{Msg: &agentv1.TabTerminalResponse_Reset_{Reset_: &agentv1.TabTerminalReset{Seed: []byte("b"), Skipped: true}}},
		endsClosed(),
	}
	w := servingTabTerminals(t, ownerCaller(), &fakeTabHelper{boxID: "box-a", script: script}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := relayed(t, openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@0"}))
	if len(got) != 3 || got[0].GetReset_().GetSkipped() || !got[1].GetReset_().GetSkipped() {
		t.Fatalf("got %v, want a plain reset, then one marked skipped", got)
	}
}

// TestABadAuthorFromTheHelperIsDropped (R-0178): the helper lives in the
// workspace, so an author whose name is over 64 characters, or has control
// or bidi characters (here U+202E, which can make "Grok Bot" read as
// someone else), is dropped, in a reset and on its own. It is never
// repaired, and the stream carries on.
func TestABadAuthorFromTheHelperIsDropped(t *testing.T) {
	bad := []*agentv1.CommandAuthor{
		{Line: 1, Name: "Grok\u202eBot", AgentId: "grok-bot"},
		{Line: 2, Name: "\u2066Retnan"},
		{Line: 3, Name: strings.Repeat("a", 65)},
		{Line: 4, Name: "Grok\x1b[31mBot"},
		{Line: 5, Name: "Grok Bot", AgentId: "grok\u202ebot"},
	}
	good := &agentv1.CommandAuthor{Line: 6, Name: strings.Repeat("b", 64)}
	script := []*agentv1.TabTerminalResponse{
		{Msg: &agentv1.TabTerminalResponse_Reset_{Reset_: &agentv1.TabTerminalReset{Seed: []byte("$ "), Authors: append(append([]*agentv1.CommandAuthor{}, bad...), good)}}},
	}
	for _, a := range bad {
		script = append(script, &agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Author{Author: a}})
	}
	script = append(script,
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Author{Author: good}},
		&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Output{Output: []byte("still here")}},
		endsClosed())
	w := servingTabTerminals(t, ownerCaller(), &fakeTabHelper{boxID: "box-a", script: script}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := relayed(t, openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@0"}))
	if len(got) != 4 {
		t.Fatalf("got %d messages (%v), want reset, the good author, output and end", len(got), got)
	}
	if ra := got[0].GetReset_().GetAuthors(); len(ra) != 1 || ra[0].GetLine() != 6 {
		t.Fatalf("reset authors %v, want only line 6", ra)
	}
	if a := got[1].GetAuthor(); a.GetLine() != 6 || a.GetName() != good.GetName() {
		t.Fatalf("author %v, want line 6 unchanged", a)
	}
	if string(got[2].GetOutput()) != "still here" {
		t.Fatalf("then %v, want the output", got[2])
	}
}

// TestTabTerminalEndMapsByValue (R-0178): each end value maps to its own,
// and one this portenvd doesn't know is UNSPECIFIED.
func TestTabTerminalEndMapsByValue(t *testing.T) {
	for in, want := range map[agentv1.TabTerminalEnd]typesv1.TabTerminalEnd{
		agentv1.TabTerminalEnd_TAB_TERMINAL_END_UNSPECIFIED:  typesv1.TabTerminalEnd_TAB_TERMINAL_END_UNSPECIFIED,
		agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED:   typesv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED,
		agentv1.TabTerminalEnd_TAB_TERMINAL_END_BOX_STOPPING: typesv1.TabTerminalEnd_TAB_TERMINAL_END_BOX_STOPPING,
		agentv1.TabTerminalEnd(3):                            typesv1.TabTerminalEnd_TAB_TERMINAL_END_UNSPECIFIED,
		agentv1.TabTerminalEnd(99):                           typesv1.TabTerminalEnd_TAB_TERMINAL_END_UNSPECIFIED,
	} {
		if got := endFor(in); got != want {
			t.Errorf("%v: %v, want %v", in, got, want)
		}
	}
}
