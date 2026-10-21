// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/idleinput"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The bottom of Claude Code 2.1.304's screen at its input (the owner's
// live test), with what's above it and what's in the input.
func claudeScreen(above, input string) string {
	return above +
		"\n────────────────────────────────────────────────────────────\n" +
		input +
		"\n────────────────────────────────────────────────────────────\n" +
		"  ▸▸ auto mode on (shift+tab to cycle)            Update available! Run: your package manager update command\n"
}

// The whole routine and wake-up decision on Claude Code's real screens:
// idle at an empty input even when its last reply ends in a question
// (that's the transcript, not a prompt), and busy for anything typed, a
// question under the input or in place of it, a secret anywhere, output
// in the last 5 s or the alternate screen.
func TestRoutineVerdictOnClaudeScreens(t *testing.T) {
	m, err := idleinput.Compile(idleinput.Claude)
	if err != nil {
		t.Fatal(err)
	}
	reply := "⏺ Done. I updated the handler in acme-api. Want me to run the tests too?\n\n✻ Brewed for 42s\n"
	listed := "⏺ Here's what changed:\n  - the handler\n  - its test\n"
	quiet, loud := routineQuiet+time.Second, time.Second
	cases := []struct {
		name, screen string
		quiet        time.Duration
		alt          bool
		why          string // "" is ready
	}{
		{"idle, placeholder", claudeScreen(reply, `❯ Try "refactor <filepath>"`), quiet, false, ""},
		{"idle, question placeholder", claudeScreen(reply, `❯ Try "how do I log an error?"`), quiet, false, ""},
		{"idle, edit placeholder", claudeScreen(listed, `❯ Try "edit <filepath> to..."`), quiet, false, ""},
		{"idle, empty", claudeScreen(listed, `❯ `), quiet, false, ""},
		{"typed", claudeScreen("⏺ Updated the handler.\n", `❯ fix the bug`), quiet, false, "isn't waiting at its input"},
		{"typed under a question", claudeScreen(reply, `❯ fix the bug`), quiet, false, "busy: "},
		{"spinner", "⏺ Bash(go test ./...)\n  ⎿  Running…\n\n✻ Brewing… (12s · esc to interrupt)\n", quiet, false, "isn't waiting at its input"},
		{"permission", " Bash command\n\n   rm -rf build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)\n", quiet, false, "is asking something"},
		{"question under the input", claudeScreen(listed, `❯ `) + "Overwrite .env? [y/n]\n", quiet, false, "is asking something"},
		{"secret above the input", claudeScreen("Enter your API token to continue\n", `❯ `), quiet, false, "asks for a secret"},
		{"printing", claudeScreen(listed, `❯ `), loud, false, "printed something"},
		{"full-screen", claudeScreen(listed, `❯ `), quiet, true, "full-screen"},
		{"old box", "╭──────╮\n│ >    │\n╰──────╯\n  ? for shortcuts\n", quiet, false, ""},
		{"old bare", "Anything else?\n> \n", quiet, false, ""},
	}
	for _, c := range cases {
		_, got := routineVerdict("writer", lastLines(c.screen, screenLines), c.quiet, c.alt, m)
		if c.why == "" && got != "" || c.why != "" && !strings.Contains(got, c.why) {
			t.Errorf("%s: %q, want %q", c.name, got, c.why)
		}
	}
	if _, got := routineVerdict("writer", lastLines(claudeScreen("⏺ Updated the handler.\n", `❯ `), screenLines), quiet, false, nil); !strings.Contains(got, "no idle-input pattern") {
		t.Errorf("no rule: %q", got)
	}
}

// End to end through the helper with a fake pane: a stand-in draws Claude
// Code 2.1.304's idle screen and reads a line; WakeAgent with the runner
// table's own Claude rule types the wake line into it. The same screen
// with text typed gets nothing.
func TestWakeReachesAnIdleClaude2_1_304Pane(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	shortQuiet(t)
	dir := t.TempDir()
	// The screen comes from a file: a program's arguments hold no newline.
	draw := func(name, input string) string {
		path := filepath.Join(dir, name+".screen")
		if err := os.WriteFile(path, []byte(claudeScreen("⏺ Done. Want me to run the tests too?\n", input)), 0o600); err != nil {
			t.Fatal(err)
		}
		return `cat '` + path + `'; `
	}
	ready := draw("writer", `❯ Try "refactor <filepath>"`) + `IFS= read -r line; printf '%s' "$line" > "$0"; exec sleep 100`
	typed := draw("typing", `❯ fix the bug`) + `IFS= read -r line; exec sleep 100`
	if _, err := s.StartAgentTerminals(ctx, testSpec(t, "writer", standIn(ready, filepath.Join(dir, "writer"))...)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAgentTerminals(ctx, testSpec(t, "typing", standIn(typed, "x")...)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"writer", "typing"} {
		if err := s.SetAgentIdleInput(id, idleinput.ForRunner([]string{"/usr/bin/claude", "--model", "opus"})); err != nil {
			t.Fatal(err)
		}
	}
	waitQuiet()
	_, err := s.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: "typing", From: "lead", Count: 1})
	if status.Code(err) != codes.FailedPrecondition || !strings.HasPrefix(status.Convert(err).Message(), "busy: ") {
		t.Errorf("typed text: %v, want busy", err)
	}
	a := s.agents.by["typing"]
	if screen, _ := a.tmux(ctx, "capture-pane", "-p", "-t", a.mainPane); strings.Contains(screen, "[Portenv]") {
		t.Errorf("typed after the owner's text:\n%s", screen)
	}
	if _, err := s.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: "writer", From: "lead", Count: 1}); err != nil {
		w := s.agents.by["writer"]
		screen, _ := w.tmux(ctx, "capture-pane", "-p", "-t", w.mainPane)
		t.Fatalf("%v\n%s", err, screen)
	}
	if got, want := waitFile(t, filepath.Join(dir, "writer")), "[Portenv] new message from lead; run portenv inbox"; got != want {
		t.Errorf("the writer's pane read %q, want %q", got, want)
	}
}

// The previous Claude Code's idle screen: "> " in a box with sides, and
// its footer under the box.
const claudeOldScreen = "⏺ Done.\n\n" +
	"╭──────────────────────────────────────────────────────╮\n" +
	"│ >                                                    │\n" +
	"╰──────────────────────────────────────────────────────╯\n" +
	"  ? for shortcuts\n"

// R-0411: CheckAgentIdle answers portenvd's question without typing
// anything: idle at Claude Code 2.1.304's prompt and at the previous
// version's box, no prompt for a quiet pane with text typed, busy while
// it prints, and no rule for a runner without one.
func TestCheckAgentIdleTypesNothing(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	shortQuiet(t)
	dir := t.TempDir()
	draw := func(name, screen string) string {
		path := filepath.Join(dir, name+".screen")
		if err := os.WriteFile(path, []byte(screen), 0o600); err != nil {
			t.Fatal(err)
		}
		return `cat '` + path + `'; `
	}
	read := `IFS= read -r line; printf '%s' "$line" > "$0"; exec sleep 100`
	claude := idleinput.ForRunner([]string{"claude"})
	cases := []struct {
		id, script string
		rule       idleinput.Input
		want       agentv1.AgentIdleCheck
	}{
		{"current", draw("current", claudeScreen("⏺ Done. Want me to run the tests too?\n", `❯ Try "how do I log an error?"`)) + read, claude, agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_IDLE},
		{"previous", draw("previous", claudeOldScreen) + read, claude, agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_IDLE},
		{"typed", draw("typed", claudeScreen("⏺ Done.\n", `❯ fix the bug`)) + read, claude, agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT},
		{"prints", draw("prints", claudeScreen("⏺ Done.\n", `❯ `)) + `while :; do printf .; sleep 0.3; done`, claude, agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_BUSY},
		{"norule", draw("norule", claudeScreen("⏺ Done.\n", `❯ `)) + read, idleinput.Input{}, agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_RULE},
	}
	for _, c := range cases {
		if _, err := s.StartAgentTerminals(ctx, testSpec(t, c.id, standIn(c.script, filepath.Join(dir, c.id))...)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAgentIdleInput(c.id, c.rule); err != nil {
			t.Fatal(err)
		}
	}
	waitQuiet()
	for _, c := range cases {
		r, err := s.CheckAgentIdle(ctx, &agentv1.CheckAgentIdleRequest{AgentId: c.id})
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if r.GetCheck() != c.want {
			t.Errorf("%s: %s, want %s", c.id, r.GetCheck(), c.want)
		}
	}
	time.Sleep(300 * time.Millisecond)
	for _, c := range cases {
		if b, err := os.ReadFile(filepath.Join(dir, c.id)); err == nil { // #nosec G304 -- test
			t.Errorf("%s: typed %q", c.id, b)
		}
	}
	if _, err := s.CheckAgentIdle(ctx, &agentv1.CheckAgentIdleRequest{AgentId: "gone"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("an agent that isn't running: %v", err)
	}
}
