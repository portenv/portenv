// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/portenv/portenv/core/local"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeTabsAgent keeps tabs in memory and records which session each call
// named.
type fakeTabsAgent struct {
	agentv1.UnimplementedAgentServiceServer
	mu       sync.Mutex
	tabs     []*typesv1.Tab
	sessions []string
	next     int
	updates  chan []*typesv1.Tab // pushed to watchers
	watchErr error               // ends a watch after the updates
}

func (f *fakeTabsAgent) saw(session string, viewer ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(viewer) > 0 && viewer[0] != "" {
		session += "/" + viewer[0]
	}
	f.sessions = append(f.sessions, session)
}

func (f *fakeTabsAgent) ListTabs(_ context.Context, r *agentv1.ListTabsRequest) (*agentv1.ListTabsResponse, error) {
	f.saw(r.GetSession(), r.GetViewer())
	f.mu.Lock()
	defer f.mu.Unlock()
	return &agentv1.ListTabsResponse{Tabs: f.tabs}, nil
}

func (f *fakeTabsAgent) NewTab(_ context.Context, r *agentv1.NewTabRequest) (*agentv1.NewTabResponse, error) {
	f.saw(r.GetSession(), r.GetViewer())
	if r.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "tab names are 1 to 32 characters")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	t := &typesv1.Tab{Id: fmt.Sprintf("@%d", f.next), Name: r.GetName(), Active: true}
	f.tabs = append(f.tabs, t)
	return &agentv1.NewTabResponse{Tab: t}, nil
}

func (f *fakeTabsAgent) CloseTab(_ context.Context, r *agentv1.CloseTabRequest) (*agentv1.CloseTabResponse, error) {
	f.saw(r.GetSession(), r.GetViewer())
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range f.tabs {
		if t.GetId() == r.GetId() {
			f.tabs = append(f.tabs[:i], f.tabs[i+1:]...)
			return &agentv1.CloseTabResponse{}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "no tab")
}

func (f *fakeTabsAgent) RenameTab(_ context.Context, r *agentv1.RenameTabRequest) (*agentv1.RenameTabResponse, error) {
	f.saw(r.GetSession(), r.GetViewer())
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tabs {
		if t.GetId() == r.GetId() {
			t.Name = r.GetName()
			return &agentv1.RenameTabResponse{}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "no tab")
}

func (f *fakeTabsAgent) SelectTab(_ context.Context, r *agentv1.SelectTabRequest) (*agentv1.SelectTabResponse, error) {
	f.saw(r.GetSession(), r.GetViewer())
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tabs {
		t.Active = t.GetId() == r.GetId()
	}
	return &agentv1.SelectTabResponse{}, nil
}

func (f *fakeTabsAgent) WatchTabs(r *agentv1.WatchTabsRequest, stream agentv1.AgentService_WatchTabsServer) error {
	f.saw(r.GetSession(), r.GetViewer())
	for {
		select {
		case ts, ok := <-f.updates:
			if !ok {
				return f.watchErr
			}
			if err := stream.Send(&agentv1.WatchTabsResponse{Tabs: ts}); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// servingTabs runs a daemon with acme-api open on this Mac, its agent the
// fake.
func servingTabs(t *testing.T) (*fakeTabsAgent, daemonv1.DaemonServiceClient) {
	t.Helper()
	t.Setenv("PORTENV_KEYS", "file")
	fake := &fakeTabsAgent{tabs: []*typesv1.Tab{{Id: "@0", Name: "shell", Active: true}}, updates: make(chan []*typesv1.Tab, 4)}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, c := servingWith(t, func(s *Server) {
		boxWithState(t, s, "")
		// closed: the daemon's shutdown has nothing to save (no sync
		// engine here); tab calls only need the agent.
		s.open["acme-api"] = &openBox{
			closed: true,
			sess:   &local.Session{Cfg: local.BoxConfig{ID: "box-1", Name: "acme-api"}},
			agent:  agentv1.NewAgentServiceClient(conn),
		}
	})
	return fake, c
}

func tabNames(ts []*typesv1.Tab) string {
	s := ""
	for i, t := range ts {
		if i > 0 {
			s += ","
		}
		s += t.GetName()
		if t.GetActive() {
			s += "*"
		}
	}
	return s
}

// TestTabsPassThrough: portenvd relays each tab call to the box's agent,
// for the "main" session unless another is named.
func TestTabsPassThrough(t *testing.T) {
	fake, c := servingTabs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nt, err := c.NewTab(ctx, &daemonv1.NewTabRequest{Box: "acme-api", Name: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenameTab(ctx, &daemonv1.RenameTabRequest{Box: "acme-api", Id: nt.GetTab().GetId(), Name: "tests"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SelectTab(ctx, &daemonv1.SelectTabRequest{Box: "acme-api", Id: "@0", Viewer: "app"}); err != nil {
		t.Fatal(err)
	}
	l, err := c.ListTabs(ctx, &daemonv1.ListTabsRequest{Box: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if got := tabNames(l.GetTabs()); got != "shell*,tests" {
		t.Fatalf("tabs %q, want shell*,tests", got)
	}
	if _, err := c.CloseTab(ctx, &daemonv1.CloseTabRequest{Box: "acme-api", Session: "other", Id: nt.GetTab().GetId()}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fmt.Sprint(fake.sessions); got != "[main main main/app main other]" {
		t.Fatalf("sessions %s", got)
	}
}

// TestTabErrorsKeepTheirCode: the agent's answer (a bad name, an unknown tab)
// reaches the app with its code, so it can say what's wrong.
func TestTabErrorsKeepTheirCode(t *testing.T) {
	_, c := servingTabs(t)
	ctx := context.Background()
	_, err := c.NewTab(ctx, &daemonv1.NewTabRequest{Box: "acme-api", Name: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("new tab with no name: %v", err)
	}
	_, err = c.CloseTab(ctx, &daemonv1.CloseTabRequest{Box: "acme-api", Id: "@9"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("close an unknown tab: %v", err)
	}
}

// TestTabsOfABoxThatIsntOpen: no agent, no tabs; the app is told so.
func TestTabsOfABoxThatIsntOpen(t *testing.T) {
	_, c := servingTabs(t)
	_, err := c.ListTabs(context.Background(), &daemonv1.ListTabsRequest{Box: "other-box"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("tabs of a box that isn't open: %v", err)
	}
}

// TestWatchTabsRelays: each update from the agent reaches the app, and the
// app's stream ends when the agent's does (the app then watches again).
func TestWatchTabsRelays(t *testing.T) {
	fake, c := servingTabs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := c.WatchTabs(ctx, &daemonv1.WatchTabsRequest{Box: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	fake.updates <- []*typesv1.Tab{{Id: "@0", Name: "shell", Active: true}}
	fake.updates <- []*typesv1.Tab{{Id: "@0", Name: "shell"}, {Id: "@1", Name: "sneaky", Active: true}}
	for _, want := range []string{"shell*", "shell,sneaky*"} {
		m, err := w.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if got := tabNames(m.GetTabs()); got != want {
			t.Fatalf("update %q, want %q", got, want)
		}
	}
	fake.watchErr = status.Error(codes.Unavailable, "agent went away")
	close(fake.updates)
	if _, err := w.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("after the agent's watch ended: %v, want Unavailable", err)
	}
}
