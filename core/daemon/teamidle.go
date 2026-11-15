// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"time"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Never "busy forever" in silence (R-0411). Wake-ups and routines wait
// for the runner's idle prompt; if Portenv stops recognising it (a new
// runner version draws its input differently), they would wait for good
// with nothing on screen to say why. So portenvd looks at each running
// agent with an idle rule on the routines' tick (CheckAgentIdle, which
// types nothing), and when its pane has stayed quiet, not asking, with no
// prompt recognised for idleUnknownAfter, its column says so.

// idleUnknownAfter is how long a quiet pane may show no recognised prompt
// before the owner is told. 3 minutes is six checks at the 30 s tick in a
// row, so a passing frame (a redraw, a dialog closing, the owner halfway
// through typing a line) never raises it; a runner at work prints (Claude
// Code's spinner animates the whole time it works), which resets the
// count; and it's well under the shortest routine period (10 minutes), so
// a routine that can't fire is flagged before its first round is missed.
const idleUnknownAfter = 3 * time.Minute

// idleUnknownLine is the agent's column line.
func idleUnknownLine(name string) string {
	return "Can't tell if " + name + " is idle: Portenv doesn't recognise its prompt, so new messages and routines wait."
}

// tickIdleChecks checks each running agent whose runner has an idle rule,
// and announces its column when "can't tell" starts or ends.
func (e *teamEngine) tickIdleChecks(ctx context.Context) {
	e.mu.Lock()
	names := make([]string, 0, len(e.boxes))
	for n := range e.boxes {
		names = append(names, n)
	}
	e.mu.Unlock()
	for _, name := range names {
		team, err := e.loadTeam(ctx, name)
		if err != nil {
			continue
		}
		b, err := e.lockBox(ctx, name)
		if err != nil {
			continue
		}
		now := e.now()
		for _, a := range team.Agents {
			if !b.wakeable[a.ID] || b.states[a.ID] != typesv1.AgentState_AGENT_STATE_RUNNING {
				continue
			}
			h, err := e.helper(ctx, name)
			if err != nil {
				break
			}
			r, err := h.CheckAgentIdle(ctx, &agentv1.CheckAgentIdleRequest{AgentId: a.ID})
			if err != nil {
				continue // not seen: neither counts nor clears
			}
			was := b.idleUnknown[a.ID]
			if r.GetCheck() == agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT {
				if b.noPromptSince == nil {
					b.noPromptSince = map[string]time.Time{}
				}
				since, ok := b.noPromptSince[a.ID]
				if !ok {
					since = now
					b.noPromptSince[a.ID] = now
				}
				if now.Sub(since) >= idleUnknownAfter {
					if b.idleUnknown == nil {
						b.idleUnknown = map[string]bool{}
					}
					b.idleUnknown[a.ID] = true
				}
			} else {
				delete(b.noPromptSince, a.ID)
				delete(b.idleUnknown, a.ID)
			}
			if b.idleUnknown[a.ID] != was {
				b.events = append(b.events, teamEvent{agent: e.infoLocked(b, &a)})
				b.changedLocked()
			}
		}
		b.mu.Unlock()
	}
}

// clearIdleUnknownLocked forgets an agent's count, at its start or stop.
func (b *teamBox) clearIdleUnknownLocked(agent string) {
	delete(b.noPromptSince, agent)
	delete(b.idleUnknown, agent)
}
