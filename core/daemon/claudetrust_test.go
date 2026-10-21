// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"
)

// The registry's trust setting goes with each start: Claude Code's for
// claude (by its program's name, wherever it's installed), none for a
// runner Portenv hasn't checked.
func TestRunnerTrustSetting(t *testing.T) {
	for argv, want := range map[string][]string{
		"claude": {"claude"}, "/usr/bin/claude": {"/usr/bin/claude", "--model", "opus"},
	} {
		if got := runnerTrustSetting(want); got != "claude" {
			t.Errorf("%s: %q", argv, got)
		}
	}
	for _, argv := range [][]string{nil, {"codex"}, {"/usr/local/bin/aider"}, {"bash", "-c", "claude"}} {
		if got := runnerTrustSetting(argv); got != "" {
			t.Errorf("%v: %q", argv, got)
		}
	}
}

func TestStartSendsTheTrustSetting(t *testing.T) {
	_, h, _ := startedWithRoutine(t)
	if got := h.started[0].GetTrustSetting(); got != "claude" {
		t.Fatalf("claude's start sent trust setting %q", got)
	}
	team := routineTeam()
	team[0].Runner, team[0].RunnerLine = []string{"/usr/local/bin/aider"}, "aider"
	team[0].Spec.Runner = team[0].Runner
	e, h, _ := newTestEngine(t, team...)
	approveFirstStart(t, e)
	if _, err := e.startAgent(context.Background(), "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if got := h.started[0].GetTrustSetting(); got != "" {
		t.Fatalf("aider's start sent trust setting %q", got)
	}
}
