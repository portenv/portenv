// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// tabsClient serves the agent API (without the channel's TLS and token,
// which channel_test covers for every call) on the test tmux.
func tabsClient(t *testing.T) agentv1.AgentServiceClient {
	t.Helper()
	tb, _ := testTabs(t)
	s := &apiServer{tmux: func(Config) (tmuxFunc, error) { return tb.tmux, nil }}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, s)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///tabs",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentv1.NewAgentServiceClient(conn)
}

// TestTabsAPI: the calls the app's tab bar makes, end to end.
func TestTabsAPI(t *testing.T) {
	c := tabsClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l, err := c.ListTabs(ctx, &agentv1.ListTabsRequest{Session: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l.GetTabs()); got != "shell*" {
		t.Fatalf("tabs %q", got)
	}
	nt, err := c.NewTab(ctx, &agentv1.NewTabRequest{Session: "main", Name: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenameTab(ctx, &agentv1.RenameTabRequest{Session: "main", Id: nt.GetTab().GetId(), Name: "tests"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SelectTab(ctx, &agentv1.SelectTabRequest{Session: "main", Id: l.GetTabs()[0].GetId()}); err != nil {
		t.Fatal(err)
	}
	w, err := c.WatchTabs(ctx, &agentv1.WatchTabsRequest{Session: "main"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := names(m.GetTabs()); got != "shell*,tests" {
		t.Fatalf("watched tabs %q, want shell*,tests", got)
	}
	if _, err := c.CloseTab(ctx, &agentv1.CloseTabRequest{Session: "main", Id: nt.GetTab().GetId()}); err != nil {
		t.Fatal(err)
	}
	if m, err = w.Recv(); err != nil {
		t.Fatal(err)
	}
	if got := names(m.GetTabs()); got != "shell*" {
		t.Fatalf("watched tabs after close %q, want shell*", got)
	}
}

// TestTabsAPIChecksTheSession: only plain session names, as for terminals.
func TestTabsAPIChecksTheSession(t *testing.T) {
	c := tabsClient(t)
	ctx := context.Background()
	for _, bad := range []string{"", "=main", "main:1", "a b", "main;kill-server"} {
		_, err := c.ListTabs(ctx, &agentv1.ListTabsRequest{Session: bad})
		wantCode(t, err, codes.InvalidArgument)
		w, err := c.WatchTabs(ctx, &agentv1.WatchTabsRequest{Session: bad})
		if err == nil {
			_, err = w.Recv()
		}
		wantCode(t, err, codes.InvalidArgument)
	}
}
