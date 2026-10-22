// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"time"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// permissionWait is an agent's runner waiting at its permission prompt
// (R-0412 item 18): Claude Code's hook told portenvd, through `portenv
// notify-permission`. The owner answers in the agent's terminal; the app
// shows it in Needs you, with Show.
type permissionWait struct {
	summary string
	since   time.Time
}

// permissionLocked records or ends the caller's wait. Only a running
// agent waits; the summary is the agent's, so it's made one plain short
// line again here. A row goes to the app only when something changed:
// every PostToolUse says "not waiting".
func (e *teamEngine) permissionLocked(b *teamBox, me *TeamAgent, p *agentv1.PermissionWait, id string) *agentv1.TeamCallsRequest {
	old := b.permission[me.ID]
	if !p.GetWaiting() {
		if old == nil {
			return &agentv1.TeamCallsRequest{CallId: id, Ok: true}
		}
		delete(b.permission, me.ID)
	} else {
		if s := b.states[me.ID]; s != typesv1.AgentState_AGENT_STATE_RUNNING && s != typesv1.AgentState_AGENT_STATE_NEEDS_YOU {
			return refuse(id, "%s isn't running, so it has no prompt to wait at.", me.ID)
		}
		summary := teamkit.OneLine(p.GetSummary(), teamkit.PermissionSummaryMax)
		if summary == "" {
			summary = "a tool"
		}
		if old != nil && old.summary == summary {
			return &agentv1.TeamCallsRequest{CallId: id, Ok: true}
		}
		b.permission[me.ID] = &permissionWait{summary: summary, since: e.now()}
	}
	b.events = append(b.events, teamEvent{agent: e.infoLocked(b, me)})
	b.changedLocked()
	return &agentv1.TeamCallsRequest{CallId: id, Ok: true}
}
