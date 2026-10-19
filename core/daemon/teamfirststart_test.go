// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

const keptLine = "~/.claude/CLAUDE.md is the agent's own file, so Portenv left it and didn't add its pointer."

func firstStarts(h *wakeHelper) (n int) {
	for _, w := range h.wakes() {
		if w.GetFirstStart() {
			n++
		}
	}
	return n
}

// R-0330: when a known runner's start pointer was kept (the agent's own
// file holds its place), the first-step line is typed once, through
// WakeAgent's checks: the call carries only first_start, never text.
func TestAKeptPointerGetsTheFirstStepLineOnce(t *testing.T) {
	e, h, clock := newWakeEngine(t, defaultTeam())
	ctx := context.Background()
	h.pointerKept = keptLine
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	e.tickWakes(ctx)
	got := h.wakes()
	if len(got) != 1 || !got[0].GetFirstStart() || got[0].GetAgentId() != "lead" || got[0].GetFrom() != "" || got[0].GetCount() != 0 {
		t.Fatalf("wake-ups = %v, want one first_start for lead", got)
	}
	clock.add(time.Hour)
	e.tickWakes(ctx)
	if n := firstStarts(h); n != 1 {
		t.Fatalf("first-step line typed %d times, want once", n)
	}
	// Messages still wake it as before, after wakeEvery.
	mustCall(t, e, msgCall("builder", "lead", "hi"))
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 2 || got[1].GetFirstStart() || got[1].GetFrom() != "builder" {
		t.Fatalf("after a message: %v", got)
	}
	// A new start with the file still kept types it once more.
	if _, err := e.stopAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	clock.add(time.Hour)
	e.tickWakes(ctx)
	if n := firstStarts(h); n != 2 {
		t.Errorf("after a restart: %d first-step lines, want 2", n)
	}
}

// Busy is refused and tried again on a later tick, within the rate
// limit; the message batch waits behind it.
func TestTheFirstStepLineWaitsForAnIdleAgent(t *testing.T) {
	e, h, clock := newWakeEngine(t, defaultTeam())
	ctx := context.Background()
	h.pointerKept = keptLine
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	h.busy = true
	mustCall(t, e, msgCall("builder", "lead", "hi"))
	e.tickWakes(ctx)
	clock.add(wakeTick)
	e.tickWakes(ctx)
	if len(h.wakes()) != 0 {
		t.Fatalf("typed into a busy agent: %v", h.wakes())
	}
	h.busy = false
	clock.add(wakeTick)
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 1 || !got[0].GetFirstStart() {
		t.Fatalf("once idle: %v, want the first-step line first", got)
	}
	clock.add(wakeTick)
	e.tickWakes(ctx)
	if len(h.wakes()) != 1 {
		t.Fatalf("the message line came within %s of the first-step line", wakeEvery)
	}
	clock.add(wakeEvery)
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 2 || got[1].GetFrom() != "builder" {
		t.Fatalf("after %s: %v, want the message line", wakeEvery, got)
	}
}

// No line when the pointer is in place, for a runner with no idle
// pattern, for a stopped agent, or once the agent ran portenv whoami.
func TestNoFirstStepLineWhenItsNotNeededOrNotSafe(t *testing.T) {
	t.Run("pointer written", func(t *testing.T) {
		e, h, clock := newWakeEngine(t, defaultTeam(), "lead")
		clock.add(time.Hour)
		e.tickWakes(context.Background())
		if h.tries != 0 {
			t.Errorf("typed with the pointer in place: %v", h.wakes())
		}
	})
	t.Run("no idle pattern", func(t *testing.T) {
		team := defaultTeam()
		team[0].Runner, team[0].RunnerLine = []string{"/usr/local/bin/aider"}, "aider"
		team[0].Spec.Runner = team[0].Runner
		e, h, clock := newWakeEngine(t, team)
		h.pointerKept = keptLine // never sent for aider, but even so
		if _, err := e.startAgent(context.Background(), "w", "lead"); err != nil {
			t.Fatal(err)
		}
		clock.add(time.Hour)
		e.tickWakes(context.Background())
		if h.tries != 0 {
			t.Errorf("typed into a runner with no idle pattern: %v", h.wakes())
		}
	})
	t.Run("stopped", func(t *testing.T) {
		e, h, clock := newWakeEngine(t, defaultTeam())
		h.pointerKept = keptLine
		ctx := context.Background()
		if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.stopAgent(ctx, "w", "lead"); err != nil {
			t.Fatal(err)
		}
		clock.add(time.Hour)
		e.tickWakes(ctx)
		if h.tries != 0 {
			t.Errorf("typed into a stopped agent: %v", h.wakes())
		}
	})
	t.Run("ran whoami", func(t *testing.T) {
		e, h, clock := newWakeEngine(t, defaultTeam())
		h.pointerKept = keptLine
		ctx := context.Background()
		if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
			t.Fatal(err)
		}
		mustCall(t, e, &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}})
		clock.add(time.Hour)
		e.tickWakes(ctx)
		if h.tries != 0 {
			t.Errorf("typed after lead ran whoami: %v", h.wakes())
		}
	})
}
