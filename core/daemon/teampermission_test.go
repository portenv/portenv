// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

func permissionCall(from string, waiting bool, summary string) *agentv1.TeamCallsResponse {
	return &agentv1.TeamCallsResponse{AgentId: from, Op: &agentv1.TeamCallsResponse_Permission{Permission: &agentv1.PermissionWait{Waiting: waiting, Summary: summary}}}
}

// lastAgentRow is an agent's newest row on the stream, and how many rows
// for it the stream has.
func lastAgentRow(e *teamEngine, id string) (*daemonv1.AgentInfo, int) {
	b := e.box("w")
	b.mu.Lock()
	defer b.mu.Unlock()
	var last *daemonv1.AgentInfo
	n := 0
	for _, ev := range b.events {
		if ev.agent != nil && ev.agent.GetId() == id {
			last, n = ev.agent, n+1
		}
	}
	return last, n
}

func startedBuilder(t *testing.T) *teamEngine {
	t.Helper()
	e, _, _ := newTestEngine(t, defaultTeam()...)
	approveFirstStart(t, e)
	if _, err := e.startAgent(context.Background(), "w", "builder"); err != nil {
		t.Fatal(err)
	}
	return e
}

// R-0412 item 18: Claude Code's permission prompt, told by its hook, is
// "needs you" on the agent's row, with a one-line summary and the tab to
// show, until a hook says it's over.
func TestAPermissionPromptNeedsTheOwnerUntilItsOver(t *testing.T) {
	e := startedBuilder(t)
	ctx := context.Background()
	if r := e.handleCall(ctx, "w", permissionCall("builder", true, "npm install left-pad")); !r.GetOk() {
		t.Fatalf("waiting: %s", r.GetLine())
	}
	row, _ := lastAgentRow(e, "builder")
	if row.GetState() != typesv1.AgentState_AGENT_STATE_NEEDS_YOU || row.GetPermission().GetSummary() != "npm install left-pad" ||
		row.GetPermission().GetTabId() != "builder:@1" || row.GetPermission().GetSince() == nil {
		t.Fatalf("row = %v", row)
	}
	// The same prompt again sends nothing new.
	_, n := lastAgentRow(e, "builder")
	e.handleCall(ctx, "w", permissionCall("builder", true, "npm install left-pad"))
	if _, m := lastAgentRow(e, "builder"); m != n {
		t.Errorf("the same wait sent %d more rows", m-n)
	}
	if r := e.handleCall(ctx, "w", permissionCall("builder", false, "")); !r.GetOk() {
		t.Fatalf("over: %s", r.GetLine())
	}
	row, n = lastAgentRow(e, "builder")
	if row.GetState() != typesv1.AgentState_AGENT_STATE_RUNNING || row.GetPermission() != nil {
		t.Fatalf("after: %v", row)
	}
	// Over again (every PostToolUse says so): nothing sent.
	e.handleCall(ctx, "w", permissionCall("builder", false, ""))
	if _, m := lastAgentRow(e, "builder"); m != n {
		t.Errorf("an end with no wait sent %d rows", m-n)
	}
}

// What the hook sends is the agent's: portenvd keeps one plain line of at
// most 80 characters, whatever arrives.
func TestAPermissionSummaryIsCheckedAgain(t *testing.T) {
	e := startedBuilder(t)
	e.handleCall(context.Background(), "w", permissionCall("builder", true, "rm -rf /tmp/x\n\x1b[2J\u202eexe "+strings.Repeat("z", 500)))
	row, _ := lastAgentRow(e, "builder")
	s := row.GetPermission().GetSummary()
	if utf8.RuneCountInString(s) > 80 || strings.ContainsAny(s, "\n\x1b\u202e") || !strings.HasPrefix(s, "rm -rf /tmp/x [2Jexe z") {
		t.Errorf("summary = %q", s)
	}
	e.handleCall(context.Background(), "w", permissionCall("builder", true, " \n\t"))
	if row, _ := lastAgentRow(e, "builder"); row.GetPermission().GetSummary() != "a tool" {
		t.Errorf("empty summary = %q", row.GetPermission().GetSummary())
	}
}

// Stopping the agent ends its wait; an agent that isn't running has no
// prompt to wait at.
func TestStoppingEndsAPermissionWait(t *testing.T) {
	e := startedBuilder(t)
	ctx := context.Background()
	e.handleCall(ctx, "w", permissionCall("builder", true, "git push"))
	info, err := e.stopAgent(ctx, "w", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if info.GetPermission() != nil {
		t.Errorf("stopped, still waiting: %v", info)
	}
	if r := e.handleCall(ctx, "w", permissionCall("lead", true, "ls")); r.GetOk() {
		t.Error("lead isn't running, and its wait was taken")
	}
	if row := e.agentInfos(ctx, "w")["lead"]; row.GetPermission() != nil {
		t.Errorf("lead = %v", row)
	}
}
