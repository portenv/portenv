// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeTeamHelper is a workspace's helper as the team engine uses it: the
// board's writer, pane actions, and agent start and stop.
type fakeTeamHelper struct {
	agentv1.AgentServiceClient

	mu       sync.Mutex
	board    []*agentv1.AppendBoardRequest
	panes    []*agentv1.AgentPaneActionRequest
	started  []*agentv1.StartTeamAgentRequest
	stopped  []string
	nextPane int
}

func (f *fakeTeamHelper) AppendBoard(_ context.Context, in *agentv1.AppendBoardRequest, _ ...grpc.CallOption) (*agentv1.AppendBoardResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.board = append(f.board, in)
	return &agentv1.AppendBoardResponse{}, nil
}

func (f *fakeTeamHelper) AgentPaneAction(_ context.Context, in *agentv1.AgentPaneActionRequest, _ ...grpc.CallOption) (*agentv1.AgentPaneActionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.panes = append(f.panes, in)
	switch a := in.GetAction().(type) {
	case *agentv1.AgentPaneActionRequest_Focus:
		return &agentv1.AgentPaneActionResponse{Pane: a.Focus}, nil
	case *agentv1.AgentPaneActionRequest_Close:
		return &agentv1.AgentPaneActionResponse{Pane: a.Close}, nil
	}
	f.nextPane++
	return &agentv1.AgentPaneActionResponse{Pane: &agentv1.PaneRef{PaneId: fmt.Sprintf("%%%d", 10+f.nextPane), TabId: in.GetAgentId() + ":@1"}}, nil
}

func (f *fakeTeamHelper) StartTeamAgent(_ context.Context, in *agentv1.StartTeamAgentRequest, _ ...grpc.CallOption) (*agentv1.StartTeamAgentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, in)
	return &agentv1.StartTeamAgentResponse{Tabs: []*typesv1.Tab{{Id: in.GetAgentId() + ":@1", AgentId: in.GetAgentId(), PaneIds: []string{"%1"}}}}, nil
}

func (f *fakeTeamHelper) StopTeamAgent(_ context.Context, in *agentv1.StopTeamAgentRequest, _ ...grpc.CallOption) (*agentv1.StopTeamAgentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, in.GetAgentId())
	return &agentv1.StopTeamAgentResponse{}, nil
}

func (f *fakeTeamHelper) paneActions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.panes)
}

func testAgent(id string, g *typesv1.AgentGrant) TeamAgent {
	spec := &AgentSpec{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Role: "Works", RunnerLine: "claude", Runner: []string{"claude"}, Grant: g, Instructions: "You build.\n"}
	return TeamAgent{ID: id, Spec: spec, RunnerLine: "claude", Runner: []string{"claude"}, Grant: g, SHA256: "abc", Approved: true}
}

func grant(panes typesv1.GrantMode, maxPanes int32, message []string, askOwner bool) *typesv1.AgentGrant {
	return &typesv1.AgentGrant{Panes: panes, MaxPanes: maxPanes, Message: message, AskOwner: askOwner, Packages: typesv1.GrantMode_GRANT_MODE_ASK}
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newTestEngine(t *testing.T, agents ...TeamAgent) (*teamEngine, *fakeTeamHelper, *testClock) {
	t.Helper()
	h := &fakeTeamHelper{}
	clock := &testClock{t: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)}
	e := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { return Team{Agents: agents}, nil },
	)
	e.now = clock.now
	return e, h, clock
}

func defaultTeam() []TeamAgent {
	return []TeamAgent{
		testAgent("lead", grant(typesv1.GrantMode_GRANT_MODE_ALLOW, 4, []string{"builder", "reviewer"}, true)),
		testAgent("builder", grant(typesv1.GrantMode_GRANT_MODE_ASK, 2, []string{"lead"}, true)),
		testAgent("reviewer", grant(typesv1.GrantMode_GRANT_MODE_NEVER, 4, []string{"lead"}, false)),
	}
}

func msgCall(from, to, text string) *agentv1.TeamCallsResponse {
	return &agentv1.TeamCallsResponse{AgentId: from, Op: &agentv1.TeamCallsResponse_Msg{Msg: &agentv1.Msg{To: to, Text: text, Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE}}}
}

func splitCall(from string) *agentv1.TeamCallsResponse {
	return &agentv1.TeamCallsResponse{AgentId: from, Op: &agentv1.TeamCallsResponse_Split{Split: &agentv1.Split{PaneId: "%1", Argv: []string{"npm", "test"}}}}
}

func inbox(t *testing.T, e *teamEngine, id, since string) *agentv1.InboxResult {
	t.Helper()
	r := e.handleCall(context.Background(), "w", &agentv1.TeamCallsResponse{AgentId: id, Op: &agentv1.TeamCallsResponse_Inbox{Inbox: &agentv1.Inbox{SinceCursor: since}}})
	if !r.GetOk() {
		t.Fatalf("inbox for %s: %s", id, r.GetLine())
	}
	return r.GetInbox()
}

func TestMessagesGoOnlyWhereTheGrantAllows(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	if r := e.handleCall(ctx, "w", msgCall("builder", "reviewer", "psst")); r.GetOk() || r.GetLine() == "" {
		t.Errorf("builder → reviewer (not granted): %v", r)
	}
	if r := e.handleCall(ctx, "w", msgCall("builder", "lead", "ready")); !r.GetOk() || r.GetMessage().GetFrom() != "builder" {
		t.Errorf("builder → lead: %v", r)
	}
	if r := e.handleCall(ctx, "w", msgCall("reviewer", "owner", "done")); !r.GetOk() {
		t.Errorf("reviewer → owner (always allowed): %v", r)
	}
	if r := e.handleCall(ctx, "w", msgCall("builder", "nobody", "hi")); r.GetOk() {
		t.Errorf("to someone not in the team: %v", r)
	}
	if got := inbox(t, e, "lead", "").GetMessages(); len(got) != 1 || got[0].GetText() != "ready" {
		t.Errorf("lead's inbox: %v", got)
	}
	if got := inbox(t, e, "reviewer", "").GetMessages(); len(got) != 0 {
		t.Errorf("reviewer sees others' messages: %v", got)
	}
	if len(h.board) != 2 {
		t.Errorf("board lines written: %d, want 2", len(h.board))
	}
}

func TestACallerNotInTheTeamIsRefused(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	if r := e.handleCall(context.Background(), "w", msgCall("intruder", "lead", "hi")); r.GetOk() {
		t.Errorf("reply = %v", r)
	}
	unapproved := defaultTeam()
	unapproved[1].Approved = false
	e2, _, _ := newTestEngine(t, unapproved...)
	if r := e2.handleCall(context.Background(), "w", msgCall("builder", "lead", "hi")); r.GetOk() {
		t.Errorf("an agent whose file isn't approved: %v", r)
	}
	if len(h.board) != 0 {
		t.Errorf("board written: %v", h.board)
	}
}

func TestMessageLimits(t *testing.T) {
	e, _, clock := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	if r := e.handleCall(ctx, "w", msgCall("builder", "lead", strings.Repeat("x", maxBoardMessage+1))); r.GetOk() {
		t.Error("a message over 8 KB was sent")
	}
	if r := e.handleCall(ctx, "w", msgCall("builder", "lead", "")); r.GetOk() {
		t.Error("an empty message was sent")
	}
	for i := range messagesPerMinute {
		if r := e.handleCall(ctx, "w", msgCall("builder", "lead", fmt.Sprint(i))); !r.GetOk() {
			t.Fatalf("message %d: %s", i, r.GetLine())
		}
	}
	if r := e.handleCall(ctx, "w", msgCall("builder", "lead", "one too many")); r.GetOk() {
		t.Error("message 61 in a minute was sent")
	}
	clock.add(time.Minute)
	if r := e.handleCall(ctx, "w", msgCall("builder", "lead", "later")); !r.GetOk() {
		t.Errorf("a minute later: %s", r.GetLine())
	}
}

func TestPanesFollowTheGrant(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	// never: refused, the helper isn't asked.
	if r := e.handleCall(ctx, "w", splitCall("reviewer")); r.GetOk() {
		t.Errorf("reviewer (panes: never): %v", r)
	}
	if h.paneActions() != 0 {
		t.Fatal("the helper opened a pane for panes: never")
	}
	// allow: at once, for the caller.
	r := e.handleCall(ctx, "w", splitCall("lead"))
	if !r.GetOk() || r.GetPane().GetPaneId() == "" {
		t.Fatalf("lead (panes: allow): %v", r)
	}
	if h.panes[0].GetAgentId() != "lead" {
		t.Errorf("pane opened for %q", h.panes[0].GetAgentId())
	}
	// ask: a decision, and nothing opens until the owner approves.
	r = e.handleCall(ctx, "w", splitCall("builder"))
	d := r.GetDecision()
	if !r.GetOk() || d.GetKind() != typesv1.DecisionKind_DECISION_KIND_PANE || d.GetState() != typesv1.DecisionState_DECISION_STATE_PENDING {
		t.Fatalf("builder (panes: ask): %v", r)
	}
	if !strings.Contains(d.GetDetail(), "npm test") {
		t.Errorf("the decision doesn't show the command: %q", d.GetDetail())
	}
	if h.paneActions() != 1 {
		t.Fatal("a pane opened before the owner answered")
	}
	if _, err := e.answer(ctx, "w", d.GetId(), true, ""); err != nil {
		t.Fatal(err)
	}
	if h.paneActions() != 2 || h.panes[1].GetAgentId() != "builder" {
		t.Errorf("after Approve: %v", h.panes)
	}
	// The agent hears about it.
	in := inbox(t, e, "builder", "")
	if len(in.GetDecisions()) == 0 || in.GetDecisions()[len(in.GetDecisions())-1].GetState() != typesv1.DecisionState_DECISION_STATE_APPROVED {
		t.Errorf("builder's inbox: %v", in)
	}
}

func TestDeclineOpensNothing(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	d := e.handleCall(ctx, "w", splitCall("builder")).GetDecision()
	if _, err := e.answer(ctx, "w", d.GetId(), false, "not now"); err != nil {
		t.Fatal(err)
	}
	if h.paneActions() != 0 {
		t.Error("Decline opened a pane")
	}
	if _, err := e.answer(ctx, "w", d.GetId(), true, ""); err == nil {
		t.Error("a declined decision was approved afterwards")
	}
}

func TestTheAgentsPaneLimit(t *testing.T) {
	team := defaultTeam()
	team[0].Grant.MaxPanes = 2 // lead, panes: allow; its first tab's pane counts
	e, h, _ := newTestEngine(t, team...)
	ctx := context.Background()
	if _, err := e.startAgent(ctx, "w", "lead"); err == nil {
		// The first start needs the owner's team_start approval.
		t.Fatal("started without the team's first-start approval")
	}
	approveTeamStart(t, e)
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if r := e.handleCall(ctx, "w", splitCall("lead")); !r.GetOk() {
		t.Fatalf("second pane: %s", r.GetLine())
	}
	r := e.handleCall(ctx, "w", splitCall("lead"))
	if r.GetOk() || !strings.Contains(r.GetLine(), "limit") {
		t.Errorf("third pane past max_panes 2: %v", r)
	}
	if h.paneActions() != 1 {
		t.Errorf("pane actions: %d, want 1", h.paneActions())
	}
}

func approveTeamStart(t *testing.T, e *teamEngine) {
	t.Helper()
	for _, d := range e.box("w").decisions {
		if d.GetKind() == typesv1.DecisionKind_DECISION_KIND_TEAM_START && d.GetState() == typesv1.DecisionState_DECISION_STATE_PENDING {
			if _, err := e.answer(context.Background(), "w", d.GetId(), true, ""); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("no pending team_start decision")
}

func TestOnlyItsOwnPanes(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	opened := e.handleCall(ctx, "w", splitCall("lead")).GetPane().GetPaneId()
	for _, op := range []*agentv1.TeamCallsResponse{
		{AgentId: "builder", Op: &agentv1.TeamCallsResponse_Close{Close: &agentv1.PaneRef{PaneId: opened}}},
		{AgentId: "builder", Op: &agentv1.TeamCallsResponse_Focus{Focus: &agentv1.PaneRef{PaneId: opened}}},
		{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Close{Close: &agentv1.PaneRef{PaneId: "%0"}}}, // the owner's
	} {
		if r := e.handleCall(ctx, "w", op); r.GetOk() {
			t.Errorf("%v: allowed", op)
		}
	}
	if h.paneActions() != 1 {
		t.Errorf("pane actions: %d, want 1", h.paneActions())
	}
	if r := e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Close{Close: &agentv1.PaneRef{PaneId: opened}}}); !r.GetOk() {
		t.Errorf("lead closing its own pane: %s", r.GetLine())
	}
}

func TestAskOwnerFollowsTheGrantAndItsLimit(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	ask := func(id, text string) *agentv1.TeamCallsRequest {
		return e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: id, Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: text}}})
	}
	if r := ask("reviewer", "may I?"); r.GetOk() {
		t.Error("reviewer (ask_owner: false) asked")
	}
	r := ask("builder", "May I add left-pad?\nIt's small.")
	d := r.GetDecision()
	if !r.GetOk() || d.GetKind() != typesv1.DecisionKind_DECISION_KIND_ASK_OWNER || d.GetSummary() != "May I add left-pad?" {
		t.Fatalf("ask: %v", r)
	}
	if !d.GetExpiresAt().AsTime().Equal(d.GetCreatedAt().AsTime().Add(decisionExpiry)) {
		t.Errorf("expires %v, created %v", d.GetExpiresAt().AsTime(), d.GetCreatedAt().AsTime())
	}
	for i := 1; i < pendingDecisionsPerAgent; i++ {
		if r := ask("builder", fmt.Sprint(i)); !r.GetOk() {
			t.Fatalf("ask %d: %s", i, r.GetLine())
		}
	}
	if r := ask("builder", "one more"); r.GetOk() {
		t.Error("a sixth pending decision was made")
	}
}

func TestExpiredIsFinal(t *testing.T) {
	e, h, clock := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	d := e.handleCall(ctx, "w", splitCall("builder")).GetDecision()
	clock.add(decisionExpiry + time.Second)
	if _, err := e.answer(ctx, "w", d.GetId(), true, ""); err == nil {
		t.Fatal("an expired decision was approved")
	}
	if h.paneActions() != 0 {
		t.Error("a pane opened for an expired decision")
	}
	in := inbox(t, e, "builder", "")
	found := false
	for _, m := range in.GetMessages() {
		if m.GetFrom() == "portenv" && strings.Contains(m.GetText(), "expired") && strings.Contains(m.GetText(), "Ask again") {
			found = true
		}
	}
	if !found {
		t.Errorf("builder wasn't told: %v", in.GetMessages())
	}
	if got := e.box("w").decisions[d.GetId()].GetState(); got != typesv1.DecisionState_DECISION_STATE_EXPIRED {
		t.Errorf("state %v", got)
	}
}

func TestInboxCursorAndWait(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	e.handleCall(ctx, "w", msgCall("builder", "lead", "one"))
	first := inbox(t, e, "lead", "")
	if len(first.GetMessages()) != 1 || first.GetCursor() == "" {
		t.Fatalf("first: %v", first)
	}
	if again := inbox(t, e, "lead", first.GetCursor()); len(again.GetMessages()) != 0 {
		t.Errorf("since the cursor: %v", again)
	}
	done := make(chan *agentv1.TeamCallsRequest, 1)
	go func() {
		done <- e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Inbox{Inbox: &agentv1.Inbox{Wait: true, SinceCursor: first.GetCursor()}}})
	}()
	time.Sleep(50 * time.Millisecond)
	e.handleCall(ctx, "w", msgCall("builder", "lead", "two"))
	select {
	case r := <-done:
		if len(r.GetInbox().GetMessages()) != 1 || r.GetInbox().GetMessages()[0].GetText() != "two" {
			t.Errorf("waited inbox: %v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inbox --wait didn't wake")
	}
}

func TestWhoAmIIsWhatIsEnforced(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	r := e.handleCall(context.Background(), "w", &agentv1.TeamCallsResponse{AgentId: "reviewer", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}})
	me := r.GetWhoami()
	if !r.GetOk() || me.GetId() != "reviewer" || me.GetGrant().GetPanes() != typesv1.GrantMode_GRANT_MODE_NEVER || me.GetGrant().GetAskOwner() {
		t.Fatalf("whoami: %v", r)
	}
	for _, c := range me.GetCommands() {
		if c == "split" || c == "tab new" || c == "ask-owner" {
			t.Errorf("whoami lists %q, which reviewer's grant refuses", c)
		}
	}
	if len(me.GetTeammates()) != 2 {
		t.Errorf("teammates: %v", me.GetTeammates())
	}
}

func TestStartAgentNeedsAnApprovedValidFile(t *testing.T) {
	team := defaultTeam()
	team[1].Approved = false
	team[2].Problems = []string{"reviewer: grant.max_panes is 12; it must be 1 to 8."}
	e, h, _ := newTestEngine(t, team...)
	ctx := context.Background()
	approveFirstStart(t, e)
	if _, err := e.startAgent(ctx, "w", "builder"); err == nil {
		t.Error("started an unapproved file")
	}
	if _, err := e.startAgent(ctx, "w", "reviewer"); err == nil || !strings.Contains(err.Error(), "max_panes") {
		t.Errorf("started an invalid file: %v", err)
	}
	if len(h.started) != 0 {
		t.Errorf("helper started %d", len(h.started))
	}
	info, err := e.startAgent(ctx, "w", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if info.GetState() != typesv1.AgentState_AGENT_STATE_RUNNING || len(h.started) != 1 {
		t.Fatalf("lead: %v, started %d", info, len(h.started))
	}
	md := h.started[0].GetAgentsMd()
	if !strings.HasPrefix(md, "You are Lead in Portenv.") || !strings.Contains(md, "portenv whoami") || !strings.HasSuffix(md, "You build.\n") {
		t.Errorf("AGENTS.md = %q", md)
	}
	if _, err := e.stopAgent(ctx, "w", "lead"); err != nil || len(h.stopped) != 1 {
		t.Errorf("stop: %v, %v", err, h.stopped)
	}
}

// approveFirstStart asks for a start, which makes the team_start decision,
// and approves it.
func approveFirstStart(t *testing.T, e *teamEngine) {
	t.Helper()
	_, _ = e.startAgent(context.Background(), "w", "lead")
	approveTeamStart(t, e)
}

func TestTheFirstStartAsksOnceWithTheResidualRisk(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	_, err := e.startAgent(ctx, "w", "lead")
	if err == nil || len(h.started) != 0 {
		t.Fatalf("first start went ahead: %v", err)
	}
	_, _ = e.startAgent(ctx, "w", "builder") // no second decision
	n := 0
	for _, d := range e.box("w").decisions {
		if d.GetKind() == typesv1.DecisionKind_DECISION_KIND_TEAM_START {
			n++
			if !strings.Contains(d.GetDetail(), "Check their changes before you run them.") {
				t.Errorf("team_start detail: %q", d.GetDetail())
			}
		}
	}
	if n != 1 {
		t.Errorf("%d team_start decisions, want 1", n)
	}
}

func TestTheOwnerMessagesAnAgent(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	m, err := e.ownerMessage(ctx, "w", "builder", "", "please rebase")
	if err != nil || m.GetFrom() != "owner" {
		t.Fatalf("ownerMessage = %v, %v", m, err)
	}
	if got := inbox(t, e, "builder", "").GetMessages(); len(got) != 1 || got[0].GetText() != "please rebase" {
		t.Errorf("builder's inbox: %v", got)
	}
	if _, err := e.ownerMessage(ctx, "w", "nobody", "", "hi"); err == nil {
		t.Error("a message to no agent was sent")
	}
}
