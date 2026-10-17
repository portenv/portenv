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

// The RPC path runs the same checks as deliverRoutine (R-0261, R-0285):
// not asking (Waiting), 5 s quiet, not the alternate screen, the idle
// pattern matching, and no secret prompt. Each stand-in fails one, is
// refused as busy over the RPC, and gets nothing typed; one that passes
// all five gets the routine in its main pane.
func TestDeliverRoutineRPCRunsTheRoutineChecks(t *testing.T) {
	s := testAgentServers(t)
	ctx := context.Background()
	shortQuiet(t)
	read := `IFS= read -r line; exec sleep 100`
	dir := t.TempDir()
	cases := []struct {
		id, script, pattern, why string
	}{
		{"asks", idleBox + `printf 'Do you want to proceed? (y/n) '; ` + read, idleBoxPattern, "is asking something"},
		{"prints", idleBox + `while :; do printf .; sleep 0.3; done`, idleBoxPattern, "printed something"},
		{"fullscreen", `printf '\033[?1049h'; ` + idleBox + read, idleBoxPattern, "full-screen"},
		{"notidle", `printf 'Working… (esc to interrupt)\n'; ` + read, idleBoxPattern, "isn't waiting at its input"},
		{"nopattern", idleBox + read, "", "no idle-input pattern"},
		{"password", idleBox + `printf 'Password: '; ` + read, idleBoxPattern, "asks for a secret"},
	}
	for _, c := range cases {
		if _, err := s.StartAgentTerminals(ctx, testSpec(t, c.id, standIn(c.script, "x")...)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAgentIdleInput(c.id, c.pattern); err != nil {
			t.Fatal(err)
		}
	}
	ready := idleBox + `IFS= read -r line; printf '%s' "$line" > "$0"; exec sleep 100`
	if _, err := s.StartAgentTerminals(ctx, testSpec(t, "ready", standIn(ready, filepath.Join(dir, "ready"))...)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentIdleInput("ready", idleBoxPattern); err != nil {
		t.Fatal(err)
	}
	waitQuiet()
	for _, c := range cases {
		_, err := s.DeliverRoutine(ctx, &agentv1.DeliverRoutineRequest{AgentId: c.id, Text: "check"})
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), c.why) {
			t.Errorf("%s: %v, want busy: %s", c.id, err, c.why)
		}
		a := s.agents.by[c.id]
		if screen, _ := a.tmux(ctx, "capture-pane", "-p", "-t", a.mainPane); strings.Contains(screen, "Portenv routine") {
			t.Errorf("%s: typed into it:\n%s", c.id, screen)
		}
	}
	if _, err := s.DeliverRoutine(ctx, &agentv1.DeliverRoutineRequest{AgentId: "ready", Text: "x\ny"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("two lines: %v", err)
	}
	if _, err := s.DeliverRoutine(ctx, &agentv1.DeliverRoutineRequest{AgentId: "ready", Text: "check the build"}); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, filepath.Join(dir, "ready")); got != routinePrefix+"check the build" {
		t.Errorf("the main pane read %q", got)
	}
}
