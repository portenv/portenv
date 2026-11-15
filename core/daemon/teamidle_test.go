// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// idleHelper answers CheckAgentIdle with check, and counts the calls.
type idleHelper struct {
	*fakeTeamHelper
	imu    sync.Mutex
	check  agentv1.AgentIdleCheck
	checks int
}

func (h *idleHelper) CheckAgentIdle(_ context.Context, _ *agentv1.CheckAgentIdleRequest, _ ...grpc.CallOption) (*agentv1.CheckAgentIdleResponse, error) {
	h.imu.Lock()
	defer h.imu.Unlock()
	h.checks++
	return &agentv1.CheckAgentIdleResponse{Check: h.check}, nil
}

func (h *idleHelper) set(c agentv1.AgentIdleCheck) {
	h.imu.Lock()
	defer h.imu.Unlock()
	h.check = c
}

func newIdleEngine(t *testing.T, team []TeamAgent, start ...string) (*teamEngine, *idleHelper, *testClock) {
	t.Helper()
	h := &idleHelper{fakeTeamHelper: &fakeTeamHelper{}}
	clock := &testClock{t: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	e := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { return Team{Agents: team}, nil },
	)
	e.now = clock.now
	approveFirstStart(t, e)
	for _, id := range start {
		if _, err := e.startAgent(context.Background(), "w", id); err != nil {
			t.Fatal(err)
		}
	}
	return e, h, clock
}

// R-0411: a runner whose idle prompt Portenv doesn't recognise never
// looks merely busy forever. Quiet with no prompt for idleUnknownAfter,
// its column says "Can't tell if Lead is idle"; the line clears as soon
// as a prompt is recognised or output resumes, and any other answer
// starts the count again. Each change is an agent event for the app.
func TestAnUnrecognisedPromptIsSaidAfterThreeMinutes(t *testing.T) {
	e, h, clock := newIdleEngine(t, defaultTeam(), "lead")
	ctx := context.Background()
	line := func() string { return e.agentInfos(ctx, "w")["lead"].GetIdleUnknown() }
	events := func() int {
		b, err := e.lockBox(ctx, "w")
		if err != nil {
			t.Fatal(err)
		}
		defer b.mu.Unlock()
		n := 0
		for _, ev := range b.events {
			if ev.agent != nil && ev.agent.GetId() == "lead" {
				n++
			}
		}
		return n
	}
	h.set(agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT)
	e.tickIdleChecks(ctx)
	clock.add(idleUnknownAfter - 30*time.Second)
	e.tickIdleChecks(ctx)
	if l := line(); l != "" {
		t.Fatalf("said before %s: %q", idleUnknownAfter, l)
	}
	before := events()
	clock.add(30 * time.Second)
	e.tickIdleChecks(ctx)
	if l := line(); !strings.HasPrefix(l, "Can't tell if Lead is idle") {
		t.Fatalf("after %s: %q", idleUnknownAfter, l)
	}
	if events() != before+1 {
		t.Error("no agent event for the app")
	}
	clock.add(30 * time.Second)
	e.tickIdleChecks(ctx)
	if events() != before+1 {
		t.Error("an event without a change")
	}

	// A recognised prompt clears it at once; so does output.
	for _, c := range []agentv1.AgentIdleCheck{agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_IDLE, agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_BUSY} {
		h.set(agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT)
		for range 7 {
			e.tickIdleChecks(ctx)
			clock.add(30 * time.Second)
		}
		if line() == "" {
			t.Fatalf("%s: not said first", c)
		}
		h.set(c)
		e.tickIdleChecks(ctx)
		if l := line(); l != "" {
			t.Errorf("%s: still %q", c, l)
		}
	}

	// Output in between starts the count again.
	h.set(agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT)
	e.tickIdleChecks(ctx)
	clock.add(2 * time.Minute)
	h.set(agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_BUSY)
	e.tickIdleChecks(ctx)
	h.set(agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT)
	e.tickIdleChecks(ctx)
	clock.add(2 * time.Minute)
	e.tickIdleChecks(ctx)
	if l := line(); l != "" {
		t.Errorf("counted across output: %q", l)
	}

	// Stopped, the line goes with it.
	h.set(agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT)
	clock.add(idleUnknownAfter)
	e.tickIdleChecks(ctx)
	if line() == "" {
		t.Fatal("not said")
	}
	if _, err := e.stopAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if l := line(); l != "" {
		t.Errorf("a stopped agent: %q", l)
	}
}

// A runner with no idle rule is never checked: its column already says
// why its routines and wakes are off.
func TestARunnerWithoutARuleIsNotChecked(t *testing.T) {
	team := defaultTeam()
	team[0].Runner, team[0].RunnerLine = []string{"/usr/local/bin/aider"}, "aider"
	team[0].Spec.Runner = team[0].Runner
	e, h, clock := newIdleEngine(t, team, "lead")
	h.set(agentv1.AgentIdleCheck_AGENT_IDLE_CHECK_NO_PROMPT)
	for range 10 {
		e.tickIdleChecks(context.Background())
		clock.add(time.Minute)
	}
	if h.checks != 0 {
		t.Errorf("checked %d times", h.checks)
	}
	if l := e.agentInfos(context.Background(), "w")["lead"].GetIdleUnknown(); l != "" {
		t.Errorf("said %q", l)
	}
}
