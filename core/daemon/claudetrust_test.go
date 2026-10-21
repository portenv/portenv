// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"slices"
	"testing"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
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

// Each Claude Code start carries its settings: the agent's own home, and
// the clones its grant lets it read.
func TestStartSendsClaudeSettings(t *testing.T) {
	_, h, _ := startedWithRoutine(t)
	r := h.started[0]
	if r.GetRunnerSettingsFlag() != "--settings" || len(r.GetRunnerSettings()) == 0 {
		t.Fatalf("flag %q, settings %q", r.GetRunnerSettingsFlag(), r.GetRunnerSettings())
	}
	want, err := teamkit.ClaudeSettings("/home/"+r.GetAgentId(), nil)
	if err != nil || string(r.GetRunnerSettings()) != string(want) {
		t.Fatalf("settings %s, want %s (%v)", r.GetRunnerSettings(), want, err)
	}
}

func TestLensPaths(t *testing.T) {
	readers := []*agentv1.CloneReaders{
		{Writer: "builder", Readers: []string{"reviewer", "lead"}},
		{Writer: "design", Readers: []string{"reviewer"}},
		{Writer: "lead"},
	}
	if got := lensPaths(readers, "reviewer", "acme-api"); !slices.Equal(got, []string{"/home/builder/acme-api", "/home/design/acme-api"}) {
		t.Errorf("reviewer: %v", got)
	}
	if got := lensPaths(readers, "builder", "acme-api"); len(got) != 0 {
		t.Errorf("builder: %v", got)
	}
}
