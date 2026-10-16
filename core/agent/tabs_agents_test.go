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

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
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
	return s
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
	ok := []string{"2001", "2001", "/home/builder", "--", "tmux", "-S", "/run/portenv-team/builder/tmux/default", "list-sessions"}
	if _, _, _, argv, err := parseAsAgent(ok); err != nil || argv[0] != "tmux" {
		t.Fatalf("an agent was refused: %v", err)
	}
	for name, args := range map[string][]string{
		"root":                 {"0", "0", "/home/builder", "--", "tmux"},
		"work":                 {"1000", "1000", "/home/builder", "--", "tmux"},
		"portenv-sync":         {"990", "990", "/home/builder", "--", "tmux"},
		"the team group's id":  {"2000", "2000", "/home/builder", "--", "tmux"},
		"past the last agent":  {"2009", "2009", "/home/builder", "--", "tmux"},
		"another group":        {"2001", "1000", "/home/builder", "--", "tmux"},
		"work's home":          {"2001", "2001", "/home/work", "--", "tmux"},
		"a home outside /home": {"2001", "2001", "/root", "--", "tmux"},
		"a home that climbs":   {"2001", "2001", "/home/../root", "--", "tmux"},
		"a program but tmux":   {"2001", "2001", "/home/builder", "--", "sh", "-c", "id"},
		"no program":           {"2001", "2001", "/home/builder", "--"},
		"no separator":         {"2001", "2001", "/home/builder", "tmux", "x"},
		"a uid that isn't one": {"x", "2001", "/home/builder", "--", "tmux"},
	} {
		if _, _, _, _, err := parseAsAgent(args); err == nil {
			t.Errorf("%s: allowed", name)
		}
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
