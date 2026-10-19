// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"time"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Message wake-ups (CC-0498, R-0311). Messages wait for portenv inbox, and
// an interactive runner like Claude Code never polls by itself, so an idle
// agent given a task would sit idle. When a message or a decision's answer
// arrives for a running agent, portenvd asks the helper to type a fixed
// Portenv line into the agent's own main pane (WakeAgent), under exactly
// DeliverRoutine's checks: never the message's text, and never into a
// runner with no idle pattern.

// wakeEvery is the least time between two wake-ups of one agent. 30 s is
// the routines' retry cadence (ADR 0022 §4b), so a runner is typed into
// at most twice a minute by either path. Each wake-up costs the runner a
// turn; without the limit the 60-messages-a-minute rate of one chatty
// teammate could become 60 turns. Within it, an agent woken, reading and
// replying hears the answer to its reply within half a minute.
const wakeEvery = 30 * time.Second

// wakeTick is how often portenvd looks for agents to wake: the helper's
// own quiet rule is 5 s, so a shorter tick would only be refused as busy.
const wakeTick = 5 * time.Second

// wakeState is one agent's unread batch.
type wakeState struct {
	from  string    // who wrote the newest unread line: an agent id or owner
	count int       // lines since the agent last read its inbox
	typed bool      // this batch's line is typed; no other until it reads
	last  time.Time // the last line typed, for wakeEvery
	// firstStart: the first-step line is owed (R-0330), once per start,
	// because the runner's start pointer isn't in place.
	firstStart bool
}

// noteWakeLocked counts a board line just written toward its recipient's
// batch: a message to an agent from another agent or the owner, or the
// owner's answer to an agent's decision. Portenv's own lines don't wake
// anyone (a routine is typed by its own path), so the sender is always a
// team agent's id, checked when the line was made, or owner.
func (b *teamBox) noteWakeLocked(line *agentv1.AppendBoardRequest) {
	var to, from string
	switch {
	case line.GetMessage() != nil:
		m := line.GetMessage()
		if m.GetType() == typesv1.MessageType_MESSAGE_TYPE_ROUTINE || m.GetFrom() == boardFrom ||
			m.GetTo() == boardOwner || m.GetTo() == boardFrom || m.GetTo() == m.GetFrom() {
			return
		}
		to, from = m.GetTo(), m.GetFrom()
	case line.GetDecision() != nil:
		d := line.GetDecision()
		if s := d.GetState(); s != typesv1.DecisionState_DECISION_STATE_APPROVED && s != typesv1.DecisionState_DECISION_STATE_DECLINED {
			return
		}
		if d.GetFrom() == boardFrom || d.GetFrom() == boardOwner {
			return
		}
		to, from = d.GetFrom(), boardOwner
	default:
		return
	}
	if b.wakes == nil {
		b.wakes = map[string]*wakeState{}
	}
	w := b.wakes[to]
	if w == nil {
		w = &wakeState{}
		b.wakes[to] = w
	}
	w.count++
	if !w.typed {
		w.from = from
	}
}

// readWakeLocked ends an agent's batch: it has read its inbox.
func (b *teamBox) readWakeLocked(agent string) {
	if w := b.wakes[agent]; w != nil {
		w.from, w.count, w.typed = "", 0, false
	}
}

// setFirstStartLocked records, at an agent's start, whether it's owed the
// first-step line: its runner has an idle pattern (wakeable), but its
// start pointer isn't in place (the agent's own file holds it, or the
// runner table names no file for it).
func (b *teamBox) setFirstStartLocked(agent string, owed bool) {
	if b.wakes == nil {
		b.wakes = map[string]*wakeState{}
	}
	w := b.wakes[agent]
	if w == nil {
		w = &wakeState{}
		b.wakes[agent] = w
	}
	w.firstStart = owed
}

// firstStepDoneLocked drops an owed first-step line: the agent ran
// portenv whoami, so it found Portenv by itself.
func (b *teamBox) firstStepDoneLocked(agent string) {
	if w := b.wakes[agent]; w != nil {
		w.firstStart = false
	}
}

// setWakeableLocked records, at an agent's start, whether its runner has
// an idle pattern: without one it's never woken.
func (b *teamBox) setWakeableLocked(agent string, wakeable bool) {
	if b.wakeable == nil {
		b.wakeable = map[string]bool{}
	}
	b.wakeable[agent] = wakeable
}

// tickWakes wakes each running, wakeable agent with an unread batch whose
// line isn't typed yet, at most once per wakeEvery. A busy agent is
// refused by the helper and tried again on a later tick.
func (e *teamEngine) tickWakes(ctx context.Context) {
	e.mu.Lock()
	names := make([]string, 0, len(e.boxes))
	for n := range e.boxes {
		names = append(names, n)
	}
	e.mu.Unlock()
	for _, name := range names {
		b, err := e.lockBox(ctx, name)
		if err != nil {
			continue
		}
		now := e.now()
		for agent, w := range b.wakes {
			if !b.wakeable[agent] || b.states[agent] != typesv1.AgentState_AGENT_STATE_RUNNING {
				continue
			}
			if !w.firstStart && (w.count == 0 || w.typed) {
				continue
			}
			if !w.last.IsZero() && now.Sub(w.last) < wakeEvery {
				continue
			}
			h, err := e.helper(ctx, name)
			if err != nil {
				break
			}
			// The first-step line goes first (R-0330); the batch waits
			// out wakeEvery behind it.
			if w.firstStart {
				if _, err := h.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: agent, FirstStart: true}); err == nil {
					w.firstStart, w.last = false, now
				}
				continue
			}
			if _, err := h.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: agent, From: w.from, Count: int32(min(w.count, 1<<20))}); err == nil { // #nosec G115 -- bounded
				w.typed, w.last = true, now
			}
		}
		b.mu.Unlock()
	}
}
