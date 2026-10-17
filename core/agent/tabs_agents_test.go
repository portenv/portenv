// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
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

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// testAgentServers is an apiServer whose agents' tmux servers are plain
// tmux on a private socket each, as this test's user (the switch to an
// agent's user runs only in the box: tests/team-boundary).
func testAgentServers(t *testing.T) *apiServer {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("tmux is required in CI")
		}
		t.Skip("tmux not installed")
	}
	s := &apiServer{}
	withAgentServers(t, s)
	return s
}

// withAgentServers gives s agents' servers on plain tmux (testAgentServers).
func withAgentServers(t *testing.T, s *apiServer) {
	t.Helper()
	s.agentTmux = func(spec AgentServerSpec) (tmuxFunc, tmuxCmdFunc, error) {
		sock := agentSocket(spec)
		command := func(ctx context.Context, args ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", sock, "-f", "/dev/null"}, args...)...) // #nosec G204 -- test
			cmd.Env = withUTF8(append(os.Environ(), spec.Env...))
			return cmd
		}
		run := func(ctx context.Context, args ...string) (string, error) {
			out, err := command(ctx, args...).CombinedOutput()
			if err != nil {
				return string(out), fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
			}
			return string(out), nil
		}
		t.Cleanup(func() { _, _ = run(context.Background(), "kill-server") })
		return run, command, nil
	}
}

// testSpec is an agent whose runner is argv, with a short socket folder
// (a Unix socket's path is limited to about 100 bytes).
func testSpec(t *testing.T, id string, argv ...string) AgentServerSpec {
	t.Helper()
	dir, err := os.MkdirTemp("", "pa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return AgentServerSpec{ID: id, UID: 2001, GID: 2001, Home: "/home/" + id, SocketDir: dir, Argv: argv,
		Env: []string{"PORTENV_AGENT=" + id}}
}

// waitFile waits for a runner to write path.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 { // #nosec G304 -- test
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never written", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func paneAction(t *testing.T, s *apiServer, req *agentv1.AgentPaneActionRequest) *agentv1.PaneRef {
	t.Helper()
	r, err := s.AgentPaneAction(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return r.GetPane()
}

func TestAnAgentsTerminalRunsItsRunnerInItsFirstTab(t *testing.T) {
	s := testAgentServers(t)
	out := filepath.Join(t.TempDir(), "out")
	spec := testSpec(t, "builder", "sh", "-c", `echo "$PORTENV_AGENT" > "$0"; exec sleep 100`, out)
	tabs, err := s.StartAgentTerminals(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(waitFile(t, out)); got != "builder" {
		t.Errorf("the runner saw PORTENV_AGENT=%q", got)
	}
	if len(tabs) != 1 {
		t.Fatalf("tabs = %v, want one", tabs)
	}
	tab := tabs[0]
	if !strings.HasPrefix(tab.GetId(), "builder:@") || tab.GetAgentId() != "builder" || tab.GetName() != "builder" || len(tab.GetPaneIds()) != 1 {
		t.Errorf("tab = %v", tab)
	}
	// Sealed as the owner's server is.
	run, _, _ := s.agentTmux(spec)
	for opt, want := range map[string]string{"status": "off", "set-clipboard": "off", "allow-passthrough": "off", "allow-rename": "off"} {
		if got, _ := run(context.Background(), "show-options", "-gv", opt); strings.TrimSpace(got) != want {
			t.Errorf("%s = %q, want %s", opt, strings.TrimSpace(got), want)
		}
	}

	// Starting it again starts nothing new.
	again, err := s.StartAgentTerminals(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].GetId() != tab.GetId() {
		t.Errorf("a second start gave %v", again)
	}
}

func TestAgentPaneActionsActOnTheAgentsOwnServer(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	tabs, err := s.StartAgentTerminals(ctx, testSpec(t, "builder", "sleep", "100"))
	if err != nil {
		t.Fatal(err)
	}
	first := tabs[0]
	split := paneAction(t, s, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Split{
		Split: &agentv1.Split{PaneId: first.GetPaneIds()[0], Down: true, Argv: []string{"sleep", "100"}}}})
	if split.GetTabId() != first.GetId() || split.GetPaneId() == first.GetPaneIds()[0] {
		t.Errorf("split made %v in %v", split, first)
	}
	tab := paneAction(t, s, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Tab{
		Tab: &agentv1.NewTeamTab{Name: "tests", Argv: []string{"sleep", "100"}}}})
	if tab.GetTabId() == first.GetId() || !strings.HasPrefix(tab.GetTabId(), "builder:@") {
		t.Errorf("new tab is %v", tab)
	}
	paneAction(t, s, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Focus{
		Focus: &agentv1.PaneRef{PaneId: split.GetPaneId()}}})
	tabs, err = s.agents.by["builder"].tabs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%s %d %d", names(tabs), len(tabs[0].GetPaneIds()), len(tabs[1].GetPaneIds())); got != "builder*,tests 2 1" {
		t.Errorf("after split, tab and focus: %s", got)
	}
	paneAction(t, s, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Close{
		Close: &agentv1.PaneRef{PaneId: split.GetPaneId()}}})
	tabs, _ = s.agents.by["builder"].tabs(ctx)
	if len(tabs[0].GetPaneIds()) != 1 {
		t.Errorf("the closed pane is still there: %v", tabs[0])
	}

	for name, req := range map[string]*agentv1.AgentPaneActionRequest{
		"an agent that isn't running": {AgentId: "reviewer", Action: &agentv1.AgentPaneActionRequest_Focus{Focus: &agentv1.PaneRef{PaneId: "%0"}}},
		"a pane it doesn't have":      {AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Close{Close: &agentv1.PaneRef{PaneId: "%99"}}},
		"a pane that isn't a pane id": {AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Close{Close: &agentv1.PaneRef{PaneId: "=main:"}}},
		"no program":                  {AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Tab{Tab: &agentv1.NewTeamTab{Name: "x"}}},
		"no action":                   {AgentId: "builder"},
	} {
		if _, err := s.AgentPaneAction(ctx, req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A program's arguments and a tab's name reach tmux as plain text: an
// argument ending in ";" doesn't end the tmux command and start another,
// and a name's "#{…}" isn't a format.
func TestAnAgentsArgumentsNeverBecomeTmuxCommands(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "out")
	spec := testSpec(t, "builder", "sh", "-c", `printf '%s|' "$@" > "$0"; exec sleep 100`, out, "a;", ";", "kill-server", "#{pane_id}")
	if _, err := s.StartAgentTerminals(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, out); got != "a;|;|kill-server|#{pane_id}|" {
		t.Errorf("the runner got %q", got)
	}
	tab := paneAction(t, s, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Tab{
		Tab: &agentv1.NewTeamTab{Name: "#{pane_id} x;", Argv: []string{"sh", "-c", "exec sleep 100", ";", "kill-server"}}}})
	tabs, err := s.agents.by["builder"].tabs(ctx)
	if err != nil {
		t.Fatalf("the server is gone: %v", err)
	}
	if len(tabs) != 2 || tabs[1].GetId() != tab.GetTabId() || tabs[1].GetName() != "#{pane_id} x;" {
		t.Errorf("tabs = %v", tabs)
	}
	_, err = s.AgentPaneAction(ctx, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Tab{
		Tab: &agentv1.NewTeamTab{Argv: []string{"sh", `x\;`}}}})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf(`an argument ending in \; gave %v`, err)
	}
}

func TestStoppingAnAgentEndsItsServer(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	spec := testSpec(t, "builder", "sleep", "100")
	if _, err := s.StartAgentTerminals(ctx, spec); err != nil {
		t.Fatal(err)
	}
	run, _, _ := s.agentTmux(spec)
	if err := s.StopAgentTerminals(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, "list-sessions"); err == nil {
		t.Error("the agent's tmux server is still running")
	}
	if _, err := s.AgentPaneAction(ctx, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Focus{Focus: &agentv1.PaneRef{PaneId: "%0"}}}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("a stopped agent's pane action gave %v", err)
	}
	if err := s.StopAgentTerminals(ctx, "builder"); err != nil {
		t.Errorf("stopping it again: %v", err)
	}
}

// The switch is to an agent's user only, and only to run tmux.
func TestAsAgentRefusesAnyoneButAnAgent(t *testing.T) {
	ok := func() []string {
		return []string{"2001", "2001", "/home/builder", "--", "tmux", "-S", "/run/portenv-team/builder/tmux/default", "list-sessions"}
	}
	if _, _, _, argv, err := parseAsAgent(ok()); err != nil || argv[0] != "tmux" {
		t.Fatalf("an agent was refused: %v", err)
	}
	// Each case changes one thing in ok.
	for name, change := range map[string]func(a []string) []string{
		"root":                 func(a []string) []string { a[0], a[1] = "0", "0"; return a },
		"work":                 func(a []string) []string { a[0], a[1] = "1000", "1000"; return a },
		"portenv-sync":         func(a []string) []string { a[0], a[1] = "990", "990"; return a },
		"the team group's id":  func(a []string) []string { a[0], a[1] = "2000", "2000"; return a },
		"past the last agent":  func(a []string) []string { a[0], a[1] = "2009", "2009"; return a },
		"another group":        func(a []string) []string { a[1] = "1000"; return a },
		"work's home":          func(a []string) []string { a[2] = "/home/work"; return a },
		"a home outside /home": func(a []string) []string { a[2] = "/root"; return a },
		"a home that climbs":   func(a []string) []string { a[2] = "/home/../root"; return a },
		"a home that's no id":  func(a []string) []string { a[2] = "/home/Builder"; return a },
		"a program but tmux":   func(a []string) []string { a[4] = "sh"; return a },
		"tmux by a path":       func(a []string) []string { a[4] = "/home/builder/.local/bin/tmux"; return a },
		"no separator":         func(a []string) []string { a[3] = "tmux"; return a },
		"a uid that isn't one": func(a []string) []string { a[0] = "x"; return a },
		"no socket":            func(a []string) []string { a[5] = "-L"; return a },
		"another agent's":      func(a []string) []string { a[6] = "/run/portenv-team/reviewer/tmux/default"; return a },
		"the main user's":      func(a []string) []string { a[6] = "/tmp/tmux-1000/default"; return a },
		"a socket that climbs": func(a []string) []string {
			a[6] = "/run/portenv-team/builder/tmux/../../reviewer/tmux/default"
			return a
		},
		"too short": func(a []string) []string { return a[:6] },
	} {
		if _, _, _, _, err := parseAsAgent(change(ok())); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

// A start is for an agent's own home and tmux folder only.
func TestAnAgentsSpecIsItsOwn(t *testing.T) {
	ok := func() AgentServerSpec {
		return AgentServerSpec{ID: "builder", UID: 2001, GID: 2001, Home: "/home/builder", SocketDir: "/run/portenv-team/builder/tmux"}
	}
	if err := checkAgentSpec(ok()); err != nil {
		t.Fatalf("an agent was refused: %v", err)
	}
	for name, change := range map[string]func(s *AgentServerSpec){
		"another agent's home":         func(s *AgentServerSpec) { s.Home = "/home/reviewer" },
		"another agent's tmux folder":  func(s *AgentServerSpec) { s.SocketDir = "/run/portenv-team/reviewer/tmux" },
		"a tmux folder elsewhere":      func(s *AgentServerSpec) { s.SocketDir = "/home/builder/tmux" },
		"the agent's folder, not tmux": func(s *AgentServerSpec) { s.SocketDir = "/run/portenv-team/builder" },
		"work": func(s *AgentServerSpec) {
			s.ID, s.Home, s.SocketDir = "work", "/home/work", "/run/portenv-team/work/tmux"
		},
		"root's uid": func(s *AgentServerSpec) { s.UID, s.GID = 0, 0 },
	} {
		s := ok()
		change(&s)
		if err := checkAgentSpec(s); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

// The switch to an agent runs tmux by its absolute path only, and only a
// trusted file: an agent's own "tmux" early in its PATH is never run.
func TestAPlantedTmuxIsNeverRun(t *testing.T) {
	// The source: no PATH lookup anywhere in the switch.
	for _, f := range []string{"tabs_agents.go", "tabs_agents_linux.go"} {
		src, err := os.ReadFile(f) // #nosec G304 -- this package's own source
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "LookPath") {
			t.Errorf("%s looks tmux up in PATH", f)
		}
	}
	if !filepath.IsAbs(agentTmuxPath) {
		t.Fatalf("agentTmuxPath %q isn't absolute", agentTmuxPath)
	}

	// The trust check, on files this test owns.
	me := uint32(os.Getuid()) // #nosec G115 -- a uid
	dir, err := os.MkdirTemp("", "tt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	tmux := filepath.Join(dir, "tmux")
	if err := os.WriteFile(tmux, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a test program
		t.Fatal(err)
	}
	// Folders above the temp folder belong to root, not this test, so the
	// owner check stops there for this user: refused, as it should be.
	if err := checkTrustedFile(tmux, me); err == nil {
		t.Error("trusted a file under folders it doesn't own")
	}
	if err := checkTrustedFile(tmux, 0); err == nil {
		t.Error("trusted a file root doesn't own")
	}
	entry := func(path string, program bool) error {
		t.Helper()
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return checkTrustedEntry(path, fi, me, program)
	}
	if err := entry(tmux, true); err != nil {
		t.Errorf("its own 0755 file: %v", err)
	}
	for mode, why := range map[os.FileMode]string{0o775: "group-writable", 0o757: "writable by others"} {
		if err := os.Chmod(tmux, mode); err != nil {
			t.Fatal(err)
		}
		if err := entry(tmux, true); err == nil {
			t.Errorf("trusted a %s program", why)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/usr/bin/env", link); err != nil {
		t.Fatal(err)
	}
	if err := entry(link, true); err == nil {
		t.Error("trusted a link")
	}
	if err := entry(link, false); err == nil {
		t.Error("trusted a link as a folder")
	}
	if err := entry(dir, true); err == nil {
		t.Error("trusted a folder as the program")
	}
	if err := os.Chmod(dir, 0o777); err != nil { // #nosec G302 -- the test
		t.Fatal(err)
	}
	if err := entry(dir, false); err == nil {
		t.Error("trusted a folder anyone can write to")
	}
	_ = os.Chmod(dir, 0o700)
	for name, p := range map[string]string{"a relative path": "tmux", "a path that climbs": "/usr/bin/../bin/env"} {
		if err := checkTrustedFile(p, 0); err == nil {
			t.Errorf("%s: trusted", name)
		}
	}
	if err := checkTrustedFile("/usr/bin/env", 0); err != nil {
		t.Errorf("a root-owned program was refused: %v", err)
	}
}

func TestAgentTabIDsCarryTheAgent(t *testing.T) {
	for id, want := range map[string]string{
		"@3":           "|@3|true",
		"builder:@3":   "builder|@3|true",
		"builder:3":    "builder|3|false",
		"Builder:@3":   "Builder|@3|false",
		"builder:@3;x": "builder|@3;x|false",
		":@3":          "|@3|false",
	} {
		a, w, ok := SplitTabID(id)
		if got := fmt.Sprintf("%s|%s|%v", a, w, ok); got != want {
			t.Errorf("SplitTabID(%q) = %s, want %s", id, got, want)
		}
	}
}

// The owner's window shows the agents' tabs after its own, routes each
// call on an agent's tab to that agent's server, and keeps the owner's
// choice of an agent's tab.
func TestTheOwnersWindowShowsAndRoutesAgentsTabs(t *testing.T) {
	// No safety list: only a kick can show the agent's tab.
	oldSafety := tabsSafety
	tabsSafety = time.Hour
	t.Cleanup(func() { tabsSafety = oldSafety })
	// The owner's tab prints nothing, so no output relists either.
	h := newTabTermHarness(t, "sleep 600")
	withAgentServers(t, h.srv)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	watch, err := h.c.WatchTabs(ctx, &agentv1.WatchTabsRequest{Session: "main"})
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan *agentv1.WatchTabsResponse, 100)
	go func() {
		for {
			m, err := watch.Recv()
			if err != nil {
				return
			}
			sent <- m
		}
	}()
	if m := <-sent; len(m.GetTabs()) != 1 {
		t.Fatalf("first watch list: %v", m)
	}
	// The control client's own first events relist once; let them pass.
	for quiet := time.After(500 * time.Millisecond); ; {
		select {
		case <-sent:
			continue
		case <-quiet:
		}
		break
	}
	if _, err := h.srv.StartAgentTerminals(ctx, testSpec(t, "builder", "sh", "-c", "echo agent-marker; exec sleep 100")); err != nil {
		t.Fatal(err)
	}
	// A start reaches the watch at once.
	select {
	case m := <-sent:
		if len(m.GetTabs()) != 2 {
			t.Fatalf("watch after the start: %v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never saw the start")
	}

	list := func() []*typesv1.Tab {
		t.Helper()
		r, err := h.c.ListTabs(ctx, &agentv1.ListTabsRequest{Session: "main"})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetTabs()
	}
	ts := list()
	own, agent := ts[0], ts[1]
	if own.GetAgentId() != "" || len(own.GetPaneIds()) != 1 || !own.GetActive() {
		t.Errorf("the owner's tab: %v", own)
	}
	if agent.GetAgentId() != "builder" || !strings.HasPrefix(agent.GetId(), "builder:@") || len(agent.GetPaneIds()) != 1 || agent.GetActive() {
		t.Errorf("the agent's tab: %v", agent)
	}

	// Choosing the agent's tab makes it the active one; choosing the
	// owner's goes back.
	if _, err := h.c.SelectTab(ctx, &agentv1.SelectTabRequest{Session: "main", Id: agent.GetId()}); err != nil {
		t.Fatal(err)
	}
	if ts := list(); ts[0].GetActive() || !ts[1].GetActive() {
		t.Errorf("after choosing the agent's tab: %v", ts)
	}
	if _, err := h.c.SelectTab(ctx, &agentv1.SelectTabRequest{Session: "main", Id: own.GetId()}); err != nil {
		t.Fatal(err)
	}
	if ts := list(); !ts[0].GetActive() || ts[1].GetActive() {
		t.Errorf("after choosing the owner's tab again: %v", ts)
	}

	// Rename and close reach the agent's server; its last tab stays.
	if _, err := h.c.RenameTab(ctx, &agentv1.RenameTabRequest{Session: "main", Id: agent.GetId(), Name: "build #{pane_id}"}); err != nil {
		t.Fatal(err)
	}
	if ts := list(); ts[1].GetName() != "build #{pane_id}" || ts[0].GetName() == ts[1].GetName() {
		t.Errorf("after rename: %v", ts)
	}
	if _, err := h.c.CloseTab(ctx, &agentv1.CloseTabRequest{Session: "main", Id: agent.GetId()}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("closing the agent's last tab: %v", err)
	}
	for _, id := range []string{"reviewer:@0", "builder:@99", "builder:3"} {
		if _, err := h.c.SelectTab(ctx, &agentv1.SelectTabRequest{Session: "main", Id: id}); err == nil {
			t.Errorf("chose %s", id)
		}
	}

	// The agent's tab's terminal is its own server's.
	tt := h.open(ctx, agent.GetId(), "", 80, 24)
	if m := tt.next(5 * time.Second); bytes.Contains(m.GetReset_().GetSeed(), []byte("agent-marker")) {
		return
	}
	tt.until(10*time.Second, "the agent's output", func(out []byte) bool { return bytes.Contains(out, []byte("agent-marker")) })
}

// A routine is typed, with its prefix and Enter, into the agent's main
// pane only, whichever pane is current; anything but one line of plain
// text is refused, and so is a main pane that's gone.
func TestARoutineIsTypedIntoTheAgentsMainPaneOnly(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	dir := t.TempDir()
	shortQuiet(t)
	reader := func(name string) []string {
		return standIn(idleBox+`IFS= read -r line; printf '%s' "$line" > "$0"; exec sleep 100`, filepath.Join(dir, name))
	}
	tabs, err := s.StartAgentTerminals(ctx, testSpec(t, "builder", reader("main")...))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentIdleInput("builder", idleBoxPattern); err != nil {
		t.Fatal(err)
	}
	// Another pane, current, also waiting for a line.
	other := paneAction(t, s, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Split{
		Split: &agentv1.Split{PaneId: tabs[0].GetPaneIds()[0], Argv: reader("other")}}})
	paneAction(t, s, &agentv1.AgentPaneActionRequest{AgentId: "builder", Action: &agentv1.AgentPaneActionRequest_Focus{
		Focus: &agentv1.PaneRef{PaneId: other.GetPaneId()}}})

	for name, text := range map[string]string{
		"empty":                    " ",
		"two lines":                "one\ntwo",
		"an escape":                "x\x1b[2J",
		"a carriage return":        "x\ry",
		"a right-to-left override": "x\u202Ey",
		"a right-to-left isolate":  "x\u2067y",
		"a left-to-right mark":     "x\u200Ey",
		"a right-to-left mark":     "x\u200Fy",
		"an Arabic letter mark":    "x\u061Cy",
		"invalid UTF-8":            "x\xffy",
		"too long":                 strings.Repeat("x", maxRoutine),
		`ends in \;`:               `x\;`,
	} {
		if err := s.deliverRoutine(ctx, "builder", text); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.deliverRoutine(ctx, "reviewer", "check"); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("an agent that isn't running: %v", err)
	}

	waitQuiet()
	text := "check the build #{pane_id} -l x;"
	if err := s.deliverRoutine(ctx, "builder", text); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, filepath.Join(dir, "main")); got != routinePrefix+text {
		t.Errorf("the main pane read %q", got)
	}
	time.Sleep(200 * time.Millisecond)
	if b, err := os.ReadFile(filepath.Join(dir, "other")); err == nil { // #nosec G304 -- test
		t.Errorf("the other pane read %q", b)
	}

	// The main pane gone (its tab lives on in the split): refused.
	run, _, _ := s.agentTmux(s.agents.by["builder"].user)
	if _, err := run(ctx, "kill-pane", "-t", s.agents.by["builder"].mainPane); err != nil {
		t.Fatal(err)
	}
	if err := s.deliverRoutine(ctx, "builder", "check"); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("with the main pane gone: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "other")); err == nil { // #nosec G304 -- test
		t.Error("the routine went to another pane")
	}
}

// A stand-in for an agent CLI at its empty input box, and the kit's
// pattern for it.
const (
	idleBox        = `printf '╭──────╮\n│ >    │\n╰──────╯\n  ? for shortcuts\n'; `
	idleBoxPattern = `^│ > *│$`
)

// standIn runs script with sh, $0 as arg.
func standIn(script, arg string) []string { return []string{"sh", "-c", script, arg} }

// shortQuiet makes routineQuiet 2 s for a test.
func shortQuiet(t *testing.T) {
	old := routineQuiet
	routineQuiet = 2 * time.Second
	t.Cleanup(func() { routineQuiet = old })
}

// waitQuiet waits out routineQuiet, with tmux's whole seconds.
func waitQuiet() { time.Sleep(routineQuiet + 1200*time.Millisecond) }

// A routine waits until the agent is at its empty input, quiet, and not
// asking anything: each stand-in below fails one rule, is refused as
// busy, and gets nothing typed.
func TestARoutineIsNeverTypedIntoABusyAgent(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	shortQuiet(t)
	read := `IFS= read -r line; exec sleep 100`
	cases := []struct {
		id, script, pattern, why string
	}{
		{"asks", idleBox + `printf 'Do you want to proceed? (y/n) '; ` + read, idleBoxPattern, "is asking something"},
		{"asks-yn", `printf 'Overwrite .env? [y/n] '; ` + read, `.`, "is asking something"},
		{"prints", idleBox + `while :; do printf .; sleep 0.3; done`, idleBoxPattern, "printed something"},
		{"fullscreen", `printf '\033[?1049h'; ` + idleBox + read, idleBoxPattern, "full-screen"},
		{"password", idleBox + `printf 'Password: '; ` + read, idleBoxPattern, "asks for a secret"},
		{"passphrase", `printf 'Enter your passphrase to unlock the key\n'; ` + idleBox + read, idleBoxPattern, "asks for a secret"},
		{"nopattern", idleBox + read, "", "no idle-input pattern"},
		{"notidle", `printf 'Working… (esc to interrupt)\n'; ` + read, idleBoxPattern, "isn't waiting at its input"},
	}
	for _, c := range cases {
		if _, err := s.StartAgentTerminals(ctx, testSpec(t, c.id, standIn(c.script, "x")...)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAgentIdleInput(c.id, c.pattern); err != nil {
			t.Fatal(err)
		}
	}
	waitQuiet()
	for _, c := range cases {
		err := s.deliverRoutine(ctx, c.id, "check")
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), c.why) {
			t.Errorf("%s: %v, want busy: %s", c.id, err, c.why)
		}
		a := s.agents.by[c.id]
		if screen, _ := a.tmux(ctx, "capture-pane", "-p", "-t", a.mainPane); strings.Contains(screen, "Portenv routine") {
			t.Errorf("%s: typed into it:\n%s", c.id, screen)
		}
	}
	for _, bad := range []string{"(", strings.Repeat("x", maxIdleInput+1)} {
		if err := s.SetAgentIdleInput("asks", bad); status.Code(err) != codes.InvalidArgument {
			t.Errorf("pattern %.10q: %v", bad, err)
		}
	}
}
