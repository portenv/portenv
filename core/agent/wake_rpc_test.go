// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// CC-0498: a wake-up types only the fixed Portenv line, and only through
// the routine's checks. A busy agent (not at its input, or a runner with
// no idle pattern) gets nothing typed; a sender that isn't an agent id or
// "owner" is refused before anything is looked at; the idle one reads
// exactly the fixed line, never any message text.
func TestWakeAgentTypesOnlyTheFixedLineWhenIdle(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	shortQuiet(t)
	read := `IFS= read -r line; exec sleep 100`
	dir := t.TempDir()
	busy := []struct{ id, script, pattern, why string }{
		{"notidle", `printf 'Working… (esc to interrupt)\n'; ` + read, idleBoxPattern, "isn't waiting at its input"},
		{"nopattern", idleBox + read, "", "no idle-input pattern"},
	}
	for _, c := range busy {
		if _, err := s.StartAgentTerminals(ctx, testSpec(t, c.id, standIn(c.script, "x")...)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAgentIdleInput(c.id, c.pattern); err != nil {
			t.Fatal(err)
		}
	}
	ready := idleBox + `IFS= read -r line; printf '%s' "$line" > "$0"; exec sleep 100`
	for _, id := range []string{"ready", "batch"} {
		if _, err := s.StartAgentTerminals(ctx, testSpec(t, id, standIn(ready, filepath.Join(dir, id))...)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAgentIdleInput(id, idleBoxPattern); err != nil {
			t.Fatal(err)
		}
	}
	waitQuiet()
	for _, c := range busy {
		_, err := s.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: c.id, From: "lead"})
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), c.why) {
			t.Errorf("%s: %v, want busy: %s", c.id, err, c.why)
		}
		a := s.agents.by[c.id]
		if screen, _ := a.tmux(ctx, "capture-pane", "-p", "-t", a.mainPane); strings.Contains(screen, "[Portenv]") {
			t.Errorf("%s: typed into it:\n%s", c.id, screen)
		}
	}
	for _, from := range []string{"", "Lead", "lead\x1b[2J", "lead\nrm -rf", "lead\u202e", "the owner", strings.Repeat("a", 40)} {
		if _, err := s.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: "ready", From: from}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("sender %q: %v, want refused", from, err)
		}
	}
	if _, err := s.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: "gone", From: "lead"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("an agent that isn't running: %v", err)
	}
	if _, err := s.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: "ready", From: "owner", Count: 1}); err != nil {
		t.Fatal(err)
	}
	if got, want := waitFile(t, filepath.Join(dir, "ready")), "[Portenv] new message from the owner; run portenv inbox"; got != want {
		t.Errorf("the main pane read %q, want %q", got, want)
	}
	if _, err := s.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: "batch", From: "reviewer", Count: 3}); err != nil {
		t.Fatal(err)
	}
	if got, want := waitFile(t, filepath.Join(dir, "batch")), "[Portenv] new messages, the latest from reviewer; run portenv inbox"; got != want {
		t.Errorf("a batch: the main pane read %q, want %q", got, want)
	}
}
