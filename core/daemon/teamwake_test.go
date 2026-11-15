// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// wakeHelper is the fake helper with WakeAgent: busy refuses as the real
// helper does when its routine checks fail.
type wakeHelper struct {
	*fakeTeamHelper
	wmu   sync.Mutex
	tries int
	woken []*agentv1.WakeAgentRequest
	busy  bool
}

func (w *wakeHelper) WakeAgent(_ context.Context, in *agentv1.WakeAgentRequest, _ ...grpc.CallOption) (*agentv1.WakeAgentResponse, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	w.tries++
	if w.busy {
		return nil, status.Error(codes.FailedPrecondition, "busy: lead isn't waiting at its input")
	}
	w.woken = append(w.woken, in)
	return &agentv1.WakeAgentResponse{}, nil
}

func (w *wakeHelper) wakes() []*agentv1.WakeAgentRequest {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	return append([]*agentv1.WakeAgentRequest(nil), w.woken...)
}

// newWakeEngine is a team engine with lead started (its runner is
// claude, which the runner table knows, unless the team says otherwise).
func newWakeEngine(t *testing.T, team []TeamAgent, start ...string) (*teamEngine, *wakeHelper, *testClock) {
	t.Helper()
	h := &wakeHelper{fakeTeamHelper: &fakeTeamHelper{}}
	clock := &testClock{t: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
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

func mustCall(t *testing.T, e *teamEngine, c *agentv1.TeamCallsResponse) *agentv1.TeamCallsRequest {
	t.Helper()
	r := e.handleCall(context.Background(), "w", c)
	if !r.GetOk() {
		t.Fatalf("%v: %s", c, r.GetLine())
	}
	return r
}

// CC-0498: several messages before the agent reads make one wake-up,
// naming the newest sender, with no message text in the call at all. A
// line already typed for a batch isn't typed again until the agent reads;
// after it reads, the next batch waits out wakeEvery.
func TestAnIdleAgentIsWokenOnceForABatch(t *testing.T) {
	e, h, clock := newWakeEngine(t, defaultTeam(), "lead")
	ctx := context.Background()
	mustCall(t, e, msgCall("builder", "lead", "please build the parser"))
	mustCall(t, e, msgCall("builder", "lead", "and run the tests"))
	if _, err := e.ownerMessage(ctx, "w", "lead", "", "secret plan: ship friday"); err != nil {
		t.Fatal(err)
	}
	e.tickWakes(ctx)
	got := h.wakes()
	if len(got) != 1 || got[0].GetAgentId() != "lead" || got[0].GetFrom() != "owner" || got[0].GetCount() != 3 {
		t.Fatalf("wake-ups = %v, want one for lead from owner, 3 lines", got)
	}
	// The call carries who and how many, never what: the helper writes
	// the line itself.
	if txt := prototext.Format(got[0]); strings.Contains(txt, "parser") || strings.Contains(txt, "secret") || strings.Contains(txt, "friday") {
		t.Errorf("message text reached the wake-up: %s", txt)
	}

	// Unread: more messages and more time make no second line.
	mustCall(t, e, msgCall("builder", "lead", "ping"))
	clock.add(time.Hour)
	e.tickWakes(ctx)
	if n := len(h.wakes()); n != 1 {
		t.Fatalf("%d wake-ups for one unread batch, want 1", n)
	}

	// lead reads; the next message is a new batch, held by the rate limit.
	inbox(t, e, "lead", "")
	clock.add(time.Hour)
	e.tickWakes(ctx)
	if n := len(h.wakes()); n != 1 {
		t.Fatalf("woken with nothing unread: %d", n)
	}
	h.wmu.Lock()
	h.woken = nil
	h.wmu.Unlock()
	mustCall(t, e, msgCall("builder", "lead", "done?"))
	e.tickWakes(ctx) // the last line was an hour ago: allowed
	inbox(t, e, "lead", "")
	mustCall(t, e, msgCall("builder", "lead", "again"))
	clock.add(wakeEvery - time.Second)
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 1 || got[0].GetFrom() != "builder" || got[0].GetCount() != 1 {
		t.Fatalf("within %s of the last: %v, want only the first", wakeEvery, got)
	}
	clock.add(time.Second)
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 2 || got[1].GetFrom() != "builder" {
		t.Fatalf("after %s: %v, want a second", wakeEvery, got)
	}

	// inbox --wait reads too: it ends the batch.
	clock.add(time.Hour)
	mustCall(t, e, msgCall("builder", "lead", "last one"))
	mustCall(t, e, &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Inbox{Inbox: &agentv1.Inbox{Wait: true}}})
	e.tickWakes(ctx)
	if n := len(h.wakes()); n != 2 {
		t.Errorf("woken after inbox --wait read the batch: %d", n)
	}
}

// A busy agent isn't typed into: the helper refuses, portenvd tries again
// on the next tick, and a refusal doesn't count against the rate limit.
// A non-idle agent gets no line until it's idle.
func TestABusyAgentIsWokenWhenIdle(t *testing.T) {
	e, h, clock := newWakeEngine(t, defaultTeam(), "lead")
	ctx := context.Background()
	h.busy = true
	mustCall(t, e, msgCall("builder", "lead", "hi"))
	e.tickWakes(ctx)
	clock.add(wakeTick)
	e.tickWakes(ctx)
	if h.tries != 2 || len(h.wakes()) != 0 {
		t.Fatalf("tries %d, woken %v: want 2 refused tries", h.tries, h.wakes())
	}
	h.busy = false
	clock.add(wakeTick)
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 1 || got[0].GetFrom() != "builder" {
		t.Fatalf("woken = %v, want one once idle", got)
	}
}

// The owner's answer to an agent's decision wakes it, from owner; Portenv's
// own lines (notices, routines) and messages to the owner wake no one.
func TestTheOwnersAnswerWakesTheAgentAndPortenvsLinesDont(t *testing.T) {
	e, h, _ := newWakeEngine(t, defaultTeam(), "lead", "builder")
	ctx := context.Background()
	b, err := e.lockBox(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	e.noticeLocked(ctx, "w", b, "builder", "", "Portenv's own notice")
	b.mu.Unlock()
	mustCall(t, e, msgCall("builder", "owner", "for the owner"))
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 0 {
		t.Fatalf("Portenv's lines woke someone: %v", got)
	}
	d := mustCall(t, e, &agentv1.TeamCallsResponse{AgentId: "builder", Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: "May I add a dependency?"}}}).GetDecision()
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 0 {
		t.Fatalf("a pending decision woke someone: %v", got)
	}
	if _, err := e.answer(ctx, "w", d.GetId(), true, "yes"); err != nil {
		t.Fatal(err)
	}
	e.tickWakes(ctx)
	if got := h.wakes(); len(got) != 1 || got[0].GetAgentId() != "builder" || got[0].GetFrom() != "owner" {
		t.Fatalf("after the answer: %v, want builder woken from owner", got)
	}
}

// No wake-up for a runner with no idle pattern (as for routines), and its
// row and whoami say so; none for a stopped agent, nor for one that read
// its inbox before the tick.
func TestNoWakeForAnUnknownRunnerAStoppedAgentOrOneThatRead(t *testing.T) {
	team := defaultTeam()
	team[0].Runner, team[0].RunnerLine = []string{"/usr/local/bin/aider"}, "aider"
	team[0].Spec.Runner = team[0].Runner
	e, h, clock := newWakeEngine(t, team, "lead", "builder", "reviewer")
	ctx := context.Background()
	mustCall(t, e, msgCall("builder", "lead", "hi"))
	e.tickWakes(ctx)
	if h.tries != 0 {
		t.Errorf("tried to wake a runner with no idle pattern: %v", h.wakes())
	}
	if off := e.agentInfos(ctx, "w")["lead"].GetRoutinesOff(); !strings.Contains(off, "Messages don't wake it") || !strings.Contains(off, "aider") {
		t.Errorf("lead's row: %q", off)
	}
	w := mustCall(t, e, &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}).GetWhoami()
	if !strings.Contains(w.GetRoutinesOff(), "Messages don't wake it") {
		t.Errorf("lead's whoami: %q", w.GetRoutinesOff())
	}
	if off := e.agentInfos(ctx, "w")["builder"].GetRoutinesOff(); off != "" {
		t.Errorf("builder's runner is known, but its row says %q", off)
	}

	if _, err := e.stopAgent(ctx, "w", "reviewer"); err != nil {
		t.Fatal(err)
	}
	mustCall(t, e, msgCall("lead", "reviewer", "review this"))
	mustCall(t, e, msgCall("lead", "builder", "build this"))
	inbox(t, e, "builder", "")
	clock.add(time.Hour)
	e.tickWakes(ctx)
	if h.tries != 0 {
		t.Errorf("woke a stopped agent or one that had read: %v", h.wakes())
	}
}

// A refused wake-up is visible in portenvd's log: one line per agent,
// rate-limited, with the helper's reason and never the message's text;
// a wake-up that lands starts a new episode. A routine refused as busy
// says so the same way.
func TestARefusedWakeIsLoggedOncePerAgent(t *testing.T) {
	e, h, clock := newWakeEngine(t, defaultTeam(), "lead")
	var out strings.Builder
	var mu sync.Mutex
	e.log = slog.New(slog.NewTextHandler(lockedWriter{&mu, &out}, nil))
	logged := func() string { mu.Lock(); defer mu.Unlock(); return out.String() }
	ctx := context.Background()
	h.busy = true
	mustCall(t, e, msgCall("builder", "lead", "the secret plan"))
	for range 5 {
		e.tickWakes(ctx)
		clock.add(wakeTick)
	}
	if n := strings.Count(logged(), "not woken"); n != 1 {
		t.Fatalf("%d lines for 5 refusals, want 1:\n%s", n, logged())
	}
	if l := logged(); !strings.Contains(l, "its prompt wasn't idle") || !strings.Contains(l, "agent=lead") || !strings.Contains(l, "isn't waiting at its input") || strings.Contains(l, "secret plan") {
		t.Errorf("the line: %s", l)
	}
	clock.add(notTypedLogEvery)
	e.tickWakes(ctx)
	if n := strings.Count(logged(), "not woken"); n != 2 {
		t.Errorf("%d lines after %s, want 2", n, notTypedLogEvery)
	}
}

func TestARefusedRoutineIsLogged(t *testing.T) {
	e, h, clock := startedWithRoutine(t)
	var out strings.Builder
	var mu sync.Mutex
	e.log = slog.New(slog.NewTextHandler(lockedWriter{&mu, &out}, nil))
	h.busy = true
	clock.add(time.Hour)
	e.tickRoutines(context.Background())
	clock.add(30 * time.Second)
	e.tickRoutines(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if l := out.String(); strings.Count(l, "routine not typed") != 1 || strings.Contains(l, "Status round") {
		t.Errorf("the log: %s", l)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
