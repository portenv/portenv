// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/portenv/portenv/core/internal/teamkit"
)

// R-0327: the runner table names where each known runner reads user
// instructions, and init writes only files it knows.
func TestTheRunnerTablesInstructionsFiles(t *testing.T) {
	for argv, want := range map[string]string{
		"claude":                              ".claude/CLAUDE.md",
		"/usr/local/bin/claude --model x":     ".claude/CLAUDE.md",
		"codex":                               ".codex/AGENTS.md",
		"bash /home/work/acme/first-start.sh": "",
		"aider":                               "",
	} {
		if got := runnerInstructionsFile(strings.Fields(argv)); got != want {
			t.Errorf("%s: %q, want %q", argv, got, want)
		}
	}
	if runnerInstructionsFile(nil) != "" {
		t.Error("no runner has a file")
	}
	for runner, f := range runnerInstructionsFiles {
		if !teamkit.IsPointerFile(f) {
			t.Errorf("%s's %q isn't one init writes (teamkit.PointerFiles)", runner, f)
		}
	}
}

// The start passes the file to the helper; a kept file is told to the
// owner, in the agent's column, and the start still succeeds.
func TestAStartAsksForThePointerAndSaysWhenItsKept(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	approveFirstStart(t, e)
	h.pointerKept = "~/.claude/CLAUDE.md is the agent's own file, so Portenv left it and didn't add its pointer."
	ctx := context.Background()
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if f := h.started[0].GetInstructionsFile(); f != ".claude/CLAUDE.md" {
		t.Errorf("instructions file sent: %q", f)
	}
	var found bool
	h.mu.Lock()
	for _, l := range h.board {
		if m := l.GetMessage(); m != nil && m.GetTo() == boardOwner && m.GetAbout() == "lead" && strings.Contains(m.GetText(), h.pointerKept) {
			found = true
		}
	}
	h.mu.Unlock()
	if !found {
		t.Error("the owner wasn't told the pointer was kept")
	}
}
