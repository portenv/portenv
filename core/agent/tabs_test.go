// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// testTabs runs the tab logic against a real tmux server on a private
// socket, so it never touches the developer's own tmux. In CI tmux must be
// there: a missing tmux fails instead of skipping. The server reads a user
// config that lets programs rename windows, as a user's ~/.tmux.conf may:
// Portenv's own options must still win.
func testTabs(t *testing.T) (tabs, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("tmux is required in CI")
		}
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("portenv-tabs-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	conf := filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(conf, []byte("set -g allow-rename on\nsetw -g automatic-rename on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "tmux", append([]string{"-L", sock, "-f", conf}, args...)...).CombinedOutput() // #nosec G204 -- test
		if err != nil {
			return string(out), fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	t.Cleanup(func() { _, _ = run(context.Background(), "kill-server") })
	raw := func(args ...string) string {
		t.Helper()
		out, err := run(context.Background(), args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	return tabs{tmux: run, session: "main"}, raw
}

func names(ts []*typesv1.Tab) string {
	var n []string
	for _, t := range ts {
		s := t.GetName()
		if t.GetActive() {
			s += "*"
		}
		n = append(n, s)
	}
	return strings.Join(n, ",")
}

func mustList(t *testing.T, tb tabs) []*typesv1.Tab {
	t.Helper()
	ts, err := tb.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func wantCode(t *testing.T, err error, c codes.Code) {
	t.Helper()
	if status.Code(err) != c {
		t.Fatalf("error %v, want code %v", err, c)
	}
}

// TestTabsStartWithOneShell: listing the tabs of a box whose session
// doesn't exist yet creates it, with one tab named "shell".
func TestTabsStartWithOneShell(t *testing.T) {
	tb, _ := testTabs(t)
	ts := mustList(t, tb)
	if got := names(ts); got != "shell*" {
		t.Fatalf("tabs %q, want shell*", got)
	}
	if !windowIDRE.MatchString(ts[0].GetId()) {
		t.Fatalf("tab id %q is not a tmux window id", ts[0].GetId())
	}
}

// TestNewTabGoesAtTheEndAndShows: a new tab is appended and becomes the
// one the terminal shows; its returned ID matches the list.
func TestNewTabGoesAtTheEndAndShows(t *testing.T) {
	tb, _ := testTabs(t)
	mustList(t, tb)
	nt, err := tb.open(context.Background(), "build")
	if err != nil {
		t.Fatal(err)
	}
	ts := mustList(t, tb)
	if got := names(ts); got != "shell,build*" {
		t.Fatalf("tabs %q, want shell,build*", got)
	}
	if nt.GetId() != ts[1].GetId() || nt.GetName() != "build" {
		t.Fatalf("new tab %v, list has %v", nt, ts[1])
	}
}

// TestTheAppOwnsTabNames: a program running in a tab can't change its name
// (no automatic rename, no rename escape sequence), even when the user's
// tmux config allows both; and a rename sticks.
func TestTheAppOwnsTabNames(t *testing.T) {
	tb, raw := testTabs(t)
	ts := mustList(t, tb)
	raw("send-keys", "-t", ts[0].GetId(), `printf '\033ksneaky\033\\'; sleep 3`, "Enter")
	time.Sleep(1500 * time.Millisecond)
	if got := names(mustList(t, tb)); got != "shell*" {
		t.Fatalf("tabs %q after a program ran, want shell*", got)
	}
	if err := tb.rename(context.Background(), ts[0].GetId(), "server"); err != nil {
		t.Fatal(err)
	}
	// A long-running command (automatic-rename would name the tab after it)
	// and the escape sequence programs use to set a window's name.
	raw("send-keys", "-t", ts[0].GetId(), `printf '\033ksneaky\033\\'; sleep 3`, "Enter")
	time.Sleep(1500 * time.Millisecond)
	if got := names(mustList(t, tb)); got != "server*" {
		t.Fatalf("tabs %q after a program ran, want server*", got)
	}
}

// TestTabNamesAreChecked: names are 1 to 32 characters, without control
// characters or surrounding spaces; a leading dash is just a character.
func TestTabNamesAreChecked(t *testing.T) {
	tb, _ := testTabs(t)
	ts := mustList(t, tb)
	for _, bad := range []string{"", " lead", "trail ", "tab\there", "bell\a", strings.Repeat("x", 33)} {
		_, err := tb.open(context.Background(), bad)
		wantCode(t, err, codes.InvalidArgument)
		wantCode(t, tb.rename(context.Background(), ts[0].GetId(), bad), codes.InvalidArgument)
	}
	if err := tb.rename(context.Background(), ts[0].GetId(), "-n dev server ✓"); err != nil {
		t.Fatal(err)
	}
	if got := names(mustList(t, tb)); got != "-n dev server ✓*" {
		t.Fatalf("tabs %q", got)
	}
}

// TestCloseTab: closing removes the tab; the last tab can't be closed (the
// session, and with it the terminal, would end).
func TestCloseTab(t *testing.T) {
	tb, _ := testTabs(t)
	ts := mustList(t, tb)
	nt, err := tb.open(context.Background(), "build")
	if err != nil {
		t.Fatal(err)
	}
	if err := tb.close(context.Background(), nt.GetId()); err != nil {
		t.Fatal(err)
	}
	if got := names(mustList(t, tb)); got != "shell*" {
		t.Fatalf("tabs %q after close, want shell*", got)
	}
	wantCode(t, tb.close(context.Background(), ts[0].GetId()), codes.FailedPrecondition)
	if got := names(mustList(t, tb)); got != "shell*" {
		t.Fatalf("tabs %q after refusing to close the last, want shell*", got)
	}
}

// TestSelectTab shows another tab.
func TestSelectTab(t *testing.T) {
	tb, _ := testTabs(t)
	ts := mustList(t, tb)
	if _, err := tb.open(context.Background(), "build"); err != nil {
		t.Fatal(err)
	}
	if err := tb.selectTab(context.Background(), ts[0].GetId()); err != nil {
		t.Fatal(err)
	}
	if got := names(mustList(t, tb)); got != "shell*,build" {
		t.Fatalf("tabs %q, want shell*,build", got)
	}
}

// TestTabIDsStayInTheirSession: a window of another tmux session, or an ID
// that isn't a window ID, is not a tab of this box.
func TestTabIDsStayInTheirSession(t *testing.T) {
	tb, raw := testTabs(t)
	mustList(t, tb)
	raw("new-session", "-d", "-s", "other")
	other := raw("display-message", "-p", "-t", "=other:", "#{window_id}")
	ctx := context.Background()
	wantCode(t, tb.close(ctx, other), codes.NotFound)
	wantCode(t, tb.rename(ctx, other, "x"), codes.NotFound)
	wantCode(t, tb.selectTab(ctx, other), codes.NotFound)
	for _, bad := range []string{"", "1", "@", "@1;kill-server", "=main:"} {
		wantCode(t, tb.selectTab(ctx, bad), codes.InvalidArgument)
	}
	if raw("display-message", "-p", "-t", other, "#{window_name}") == "x" {
		t.Fatal("renamed another session's window")
	}
}

// TestEachViewerKeepsItsOwnTab: two viewers of the same box (the app and an
// agent) see the same tabs, but choosing one moves only that viewer's
// terminal: an agent switching tabs never changes the owner's.
func TestEachViewerKeepsItsOwnTab(t *testing.T) {
	base, raw := testTabs(t)
	app := tabs{tmux: base.tmux, session: "main", viewer: "app"}
	agt := tabs{tmux: base.tmux, session: "main", viewer: "grok"}
	ctx := context.Background()
	ts := mustList(t, app)
	b, err := app.open(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(mustList(t, app)); got != "shell,build*" {
		t.Fatalf("app %q, want shell,build*", got)
	}
	if err := agt.selectTab(ctx, ts[0].GetId()); err != nil {
		t.Fatal(err)
	}
	if got := names(mustList(t, agt)); got != "shell*,build" {
		t.Fatalf("agent %q, want shell*,build", got)
	}
	if got := names(mustList(t, app)); got != "shell,build*" {
		t.Fatalf("the agent's switch moved the app: %q, want shell,build*", got)
	}
	// A tab the agent opens shows for the app too, without moving the app.
	if _, err := agt.open(ctx, "agent-work"); err != nil {
		t.Fatal(err)
	}
	if got := names(mustList(t, app)); got != "shell,build*,agent-work" {
		t.Fatalf("app after the agent's new tab %q, want shell,build*,agent-work", got)
	}
	if err := app.selectTab(ctx, ts[0].GetId()); err != nil {
		t.Fatal(err)
	}
	if got := names(mustList(t, agt)); got != "shell,build,agent-work*" {
		t.Fatalf("the app's switch moved the agent: %q", got)
	}
	_ = b
	// Each viewer is its own grouped session over the box's session.
	if got := raw("display-message", "-p", "-t", "=main-viewer-app:", "#{session_group}"); got != "main" {
		t.Fatalf("the app's session group %q, want main", got)
	}
}

// TestAViewersSessionIsCleanedUp: when no client is attached any more, the
// viewer's grouped session goes; the box's session and its tabs stay. While
// a client is attached, it stays.
func TestAViewersSessionIsCleanedUp(t *testing.T) {
	base, raw := testTabs(t)
	app := tabs{tmux: base.tmux, session: "main", viewer: "app"}
	ctx := context.Background()
	mustList(t, app)
	if _, err := app.open(ctx, "build"); err != nil {
		t.Fatal(err)
	}
	// A client on the app's session: tmux in control mode, kept open.
	sock := strings.TrimPrefix(raw("display-message", "-p", "#{socket_path}"), "")
	client := exec.Command("tmux", "-S", sock, "-C", "attach-session", "-t", "=main-viewer-app") // #nosec G204 -- test
	stdin, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		if raw("list-clients", "-t", "=main-viewer-app", "-F", "x") != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	app.cleanup(ctx)
	if _, err := base.tmux(ctx, "has-session", "-t", "=main-viewer-app"); err != nil {
		t.Fatal("cleaned up a viewer's session while a client was attached")
	}
	_ = stdin.Close()
	_ = client.Wait()
	app.cleanup(ctx)
	if _, err := base.tmux(ctx, "has-session", "-t", "=main-viewer-app"); err == nil {
		t.Fatal("the viewer's session is still there with no client")
	}
	if got := names(mustList(t, base)); got != "shell,build*" && got != "shell*,build" {
		t.Fatalf("the box's tabs after cleanup %q, want shell and build", got)
	}
}

// TestTabsSayWhatIsRunning: a tab reports its foreground program, and
// nothing when only the shell is there (closing asks first otherwise).
func TestTabsSayWhatIsRunning(t *testing.T) {
	tb, raw := testTabs(t)
	ts := mustList(t, tb)
	if p := ts[0].GetProgram(); p != "" {
		t.Fatalf("an idle shell reports program %q, want none", p)
	}
	raw("send-keys", "-t", ts[0].GetId(), "sleep 30", "Enter")
	var p string
	for range 50 {
		if p = mustList(t, tb)[0].GetProgram(); p != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if p != "sleep" {
		t.Fatalf("program %q, want sleep", p)
	}
}

// TestViewerNamesAreChecked: plain short names only.
func TestViewerNamesAreChecked(t *testing.T) {
	base, _ := testTabs(t)
	for _, bad := range []string{"App", "a b", "a:b", "a.b", "-a", strings.Repeat("a", 17)} {
		_, err := tabs{tmux: base.tmux, session: "main", viewer: bad}.list(context.Background())
		wantCode(t, err, codes.InvalidArgument)
	}
}

// TestWatchTabsSeesEveryWindow:the watch sends the tabs at once and again
// on each change, including a window a program in the box created itself,
// so no window is ever out of the user's sight.
func TestWatchTabsSeesEveryWindow(t *testing.T) {
	tb, raw := testTabs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan string, 10)
	go func() {
		_ = tb.watch(ctx, 50*time.Millisecond, func(ts []*typesv1.Tab) error {
			got <- names(ts)
			return nil
		})
	}()
	next := func() string {
		select {
		case s := <-got:
			return s
		case <-ctx.Done():
			t.Fatal("no tab update")
			return ""
		}
	}
	if s := next(); s != "shell*" {
		t.Fatalf("first update %q", s)
	}
	raw("new-window", "-t", "=main:", "-n", "sneaky")
	if s := next(); s != "shell,sneaky*" {
		t.Fatalf("after a program's new-window %q", s)
	}
	select {
	case s := <-got:
		t.Fatalf("an update with no change: %q", s)
	case <-time.After(300 * time.Millisecond):
	}
}
