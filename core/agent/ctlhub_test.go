// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// encodeControlOutput escapes bytes as tmux's control mode does in %output:
// below 32 and the backslash as \ooo.
func encodeControlOutput(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		if c < 32 || c == '\\' {
			fmt.Fprintf(&s, "\\%03o", c)
		} else {
			s.WriteByte(c)
		}
	}
	return s.String()
}

func TestCheckTmuxVersion(t *testing.T) {
	for out, ok := range map[string]bool{
		"tmux 3.4\n": true, "tmux 3.7c\n": true, "tmux 4.0\n": true, "tmux next-3.6\n": true,
		"tmux 3.3a\n": false, "tmux 3.2\n": false, "tmux 2.9\n": false,
	} {
		err := checkTmuxVersion(context.Background(), func(context.Context, ...string) (string, error) { return out, nil })
		if (err == nil) != ok {
			t.Errorf("%q: %v, want ok=%v", out, err, ok)
		}
		if !ok && !errors.Is(err, errTmuxTooOld) {
			t.Errorf("%q: %v, want errTmuxTooOld", out, err)
		}
	}
	if err := checkTmuxVersion(context.Background(), func(context.Context, ...string) (string, error) { return "nonsense", nil }); err == nil {
		t.Error("an unreadable version was accepted")
	}
}

func TestParseMoreControlLines(t *testing.T) {
	e := parseControlLine(`%extended-output %3 120 : hi\015\012`)
	if e.kind != ctlOutput || e.pane != "%3" || string(e.data) != "hi\r\n" {
		t.Errorf("extended-output: %+v %q", e, e.data)
	}
	e = parseControlLine(`%extended-output %3 120 extra : a : b`)
	if e.kind != ctlOutput || string(e.data) != "a : b" {
		t.Errorf("extended-output with more fields: %+v %q", e, e.data)
	}
	if e := parseControlLine(`%extended-output %3 120 :`); e.kind != ctlOutput || len(e.data) != 0 {
		t.Errorf("empty extended-output: %+v", e)
	}
	if e := parseControlLine(`%extended-output %3 120`); e.kind != ctlOther {
		t.Errorf("extended-output without data: %+v", e)
	}
	for _, l := range []string{"%session-window-changed $1 @2", "%unlinked-window-add @5", "%unlinked-window-renamed @5 x", "%sessions-changed"} {
		if e := parseControlLine(l); e.kind != ctlTabsChanged {
			t.Errorf("%q: %+v", l, e)
		}
	}
}

// Data is bytes: a UTF-8 character and an escape split across two %output
// lines decode to the original stream when put together (R-0133 item 8).
func TestControlOutputSplitAcrossLines(t *testing.T) {
	want := []byte("é\x1b]133;A\x07日本")
	for cut := 1; cut < len(want); cut++ {
		a := parseControlLine("%output %1 " + encodeControlOutput(want[:cut]))
		b := parseControlLine("%output %1 " + encodeControlOutput(want[cut:]))
		if got := append(a.data, b.data...); !bytes.Equal(got, want) {
			t.Fatalf("cut at %d: %q", cut, got)
		}
	}
}

func TestEveryOctalEscape(t *testing.T) {
	var b []byte
	for c := 0; c < 32; c++ {
		b = append(b, byte(c))
	}
	b = append(b, '\\', 'x', 0x7f, 0xff)
	if got := decodeControlOutput(encodeControlOutput(b)); !bytes.Equal(got, b) {
		t.Fatalf("got %q", got)
	}
	// A backslash not followed by three octal digits is kept as it is.
	if got := string(decodeControlOutput(`a\9b\01`)); got != `a\9b\01` {
		t.Errorf("got %q", got)
	}
}

// Lines longer than the cap are dropped whole, reported, and reading goes on.
func TestReadControlLinesCapsLength(t *testing.T) {
	long := strings.Repeat("x", 300)
	in := "%window-add @1\r\n" + "%output %1 " + long + "\n%window-add @2\n%output %1 tail"
	var got []string
	tooLong := 0
	err := readControlLines(strings.NewReader(in), 100, func(l []byte, err error) {
		if err != nil {
			tooLong++
			return
		}
		if len(l) > 100 {
			t.Errorf("a %d-byte line got through", len(l))
		}
		got = append(got, string(l))
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"%window-add @1", "%window-add @2", "%output %1 tail"}; strings.Join(got, "|") != strings.Join(want, "|") || tooLong != 1 {
		t.Errorf("got %q, %d too long", got, tooLong)
	}
	// A line far longer than the reader's own buffer.
	tooLong = 0
	_ = readControlLines(strings.NewReader(strings.Repeat("y", 1<<20)+"\nok\n"), 1000, func(l []byte, err error) {
		if err != nil {
			tooLong++
		} else if string(l) != "ok" {
			t.Errorf("got %d bytes", len(l))
		}
	})
	if tooLong != 1 {
		t.Errorf("%d too long", tooLong)
	}
}

func TestSlowSubscriberGetsOverflow(t *testing.T) {
	s := &ctlSub{kick: make(chan struct{}, 1)}
	for i := 0; i < ctlSubCap+10; i++ {
		s.push(ctlEvent{kind: ctlOutput})
	}
	if es := s.take(); len(es) != 1 || es[0].kind != ctlOverflow {
		t.Fatalf("got %d events, first %+v", len(es), es)
	}
	s.push(ctlEvent{kind: ctlWindowAdd})
	if es := s.take(); len(es) != 1 || es[0].kind != ctlWindowAdd {
		t.Fatalf("after overflow: %+v", es)
	}
}

// eventsClient serves the agent API with control-mode clients on the test
// tmux, with polling and the safety list both an hour away, so anything the
// watch sees comes from events.
func eventsClient(t *testing.T) (agentv1.AgentServiceClient, func(args ...string) string, *apiServer) {
	t.Helper()
	tb, raw, cmdf := testTabsCmd(t)
	oldPoll, oldSafety := tabsPoll, tabsSafety
	tabsPoll, tabsSafety = time.Hour, time.Hour
	t.Cleanup(func() { tabsPoll, tabsSafety = oldPoll, oldSafety })
	s := &apiServer{
		tmux:    func(Config) (tmuxFunc, error) { return tb.tmux, nil },
		tmuxCmd: func(Config) (tmuxCmdFunc, error) { return cmdf, nil },
	}
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
	return agentv1.NewAgentServiceClient(conn), raw, s
}

// watchUntil reads the watch until want(names) holds, or fails after 3 s.
func watchUntil(t *testing.T, w agentv1.AgentService_WatchTabsClient, what string, want func(string) bool) {
	t.Helper()
	got := make(chan string)
	go func() {
		for {
			r, err := w.Recv()
			if err != nil {
				close(got)
				return
			}
			got <- names(r.GetTabs())
			if want(names(r.GetTabs())) {
				return
			}
		}
	}()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case n, ok := <-got:
			if !ok {
				t.Fatalf("%s: the watch ended", what)
			}
			if want(n) {
				return
			}
		case <-deadline:
			t.Fatalf("%s: not seen within 3 s", what)
		}
	}
}

// The watch follows tmux's own events (ADR 0020): a window added, renamed,
// selected by the viewer and closed are each seen at once, with no polling.
func TestWatchTabsFollowsControlModeEvents(t *testing.T) {
	c, raw, _ := eventsClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := c.WatchTabs(ctx, &agentv1.WatchTabsRequest{Session: "main", Viewer: "app"})
	if err != nil {
		t.Fatal(err)
	}
	watchUntil(t, w, "the first list", func(n string) bool { return n == "shell*" })
	// Something in the workspace opens a window (not through the API).
	raw("new-window", "-d", "-t", "=main:", "-n", "build")
	watchUntil(t, w, "a new window", func(n string) bool { return n == "shell*,build" })
	id := strings.TrimSpace(raw("list-windows", "-t", "=main:", "-F", "#{window_id} #{window_name}"))
	id = strings.Fields(strings.Split(id, "\n")[1])[0]
	if _, err := c.RenameTab(ctx, &agentv1.RenameTabRequest{Session: "main", Viewer: "app", Id: id, Name: "tests"}); err != nil {
		t.Fatal(err)
	}
	watchUntil(t, w, "a rename", func(n string) bool { return n == "shell*,tests" })
	if _, err := c.SelectTab(ctx, &agentv1.SelectTabRequest{Session: "main", Viewer: "app", Id: id}); err != nil {
		t.Fatal(err)
	}
	watchUntil(t, w, "the viewer's selection", func(n string) bool { return n == "shell,tests*" })
	raw("kill-window", "-t", id)
	watchUntil(t, w, "a closed window", func(n string) bool { return n == "shell*" })
}

// A control client that exits is started again; the watch lists afresh and
// keeps following events. tmux's clipboard and passthrough are set off
// again at each start.
func TestControlClientRestarts(t *testing.T) {
	c, raw, s := eventsClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := c.WatchTabs(ctx, &agentv1.WatchTabsRequest{Session: "main", Viewer: "app"})
	if err != nil {
		t.Fatal(err)
	}
	watchUntil(t, w, "the first list", func(n string) bool { return n == "shell*" })
	if got := raw("show-options", "-gv", "set-clipboard"); got != "off" {
		t.Fatalf("set-clipboard %q", got)
	}
	raw("set-option", "-g", "set-clipboard", "external")
	hub, _ := s.ctlHub()
	hub.restartAfter = 10 * time.Millisecond
	// Kill the control client from outside.
	pids := strings.Fields(raw("list-clients", "-F", "#{client_control_mode} #{client_pid}"))
	killed := false
	for i := 0; i+1 < len(pids); i += 2 {
		if pids[i] == "1" {
			if err := exec.Command("kill", pids[i+1]).Run(); err != nil { // #nosec G204 -- test
				t.Fatal(err)
			}
			killed = true
		}
	}
	if !killed {
		t.Fatalf("no control client in %q", pids)
	}
	deadline := time.Now().Add(3 * time.Second)
	for raw("show-options", "-gv", "set-clipboard") != "off" {
		if time.Now().After(deadline) {
			t.Fatal("the client didn't start again (set-clipboard wasn't set off)")
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw("new-window", "-d", "-t", "=main:", "-n", "after")
	watchUntil(t, w, "a window after the restart", func(n string) bool { return n == "shell*,after" })
}

// The last watch ending stops the session's client.
func TestControlClientStopsWithItsLastSubscriber(t *testing.T) {
	c, raw, _ := eventsClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, err := c.WatchTabs(ctx, &agentv1.WatchTabsRequest{Session: "main"})
	if err != nil {
		t.Fatal(err)
	}
	watchUntil(t, w, "the first list", func(n string) bool { return n == "shell*" })
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for strings.Contains(raw("list-clients", "-F", "#{client_control_mode}"), "1") {
		if time.Now().After(deadline) {
			t.Fatal("the control client is still attached")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
