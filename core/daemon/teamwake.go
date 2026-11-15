// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
			if w.count == 0 || w.typed || !b.wakeable[agent] || b.states[agent] != typesv1.AgentState_AGENT_STATE_RUNNING {
				continue
			}
			if !w.last.IsZero() && now.Sub(w.last) < wakeEvery {
				continue
			}
			h, err := e.helper(ctx, name)
			if err != nil {
				break
			}
			_, err = h.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: agent, From: w.from, Count: int32(min(w.count, 1<<20))}) // #nosec G115 -- bounded
			e.logNotTyped(name, agent, "not woken", err)
			if err == nil {
				w.typed, w.last = true, now
			}
		}
		b.mu.Unlock()
	}
}

// notTypedLogEvery is the least time between two log lines saying one
// agent wasn't typed into, for each path: wakes are tried every 5 s and
// routines every 30 s while it's busy.
const notTypedLogEvery = 10 * time.Minute

// logNotTyped writes, at most once per notTypedLogEvery per agent and
// path, why a wake-up or a routine wasn't typed (err from the helper), so
// a runner whose screen the idle rule doesn't know shows in portenvd's
// log. The helper's busy reasons name only the agent and the check that
// failed; other errors log only their code. Never a message's or a
// routine's text. A typed line (nil err) ends the episode.
func (e *teamEngine) logNotTyped(box, agent, what string, err error) {
	key := box + "/" + agent + "/" + what
	e.logMu.Lock()
	defer e.logMu.Unlock()
	if err == nil {
		delete(e.notTyped, key)
		return
	}
	if e.log == nil {
		return
	}
	now := e.now()
	if last, ok := e.notTyped[key]; ok && now.Sub(last) < notTypedLogEvery {
		return
	}
	if e.notTyped == nil {
		e.notTyped = map[string]time.Time{}
	}
	e.notTyped[key] = now
	st := status.Convert(err)
	if st.Code() == codes.FailedPrecondition && strings.HasPrefix(st.Message(), "busy") {
		e.log.Info(what+": its prompt wasn't idle", "box", box, "agent", agent, "why", st.Message())
		return
	}
	e.log.Info(what, "box", box, "agent", agent, "code", st.Code().String())
}
