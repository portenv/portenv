// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeTeamHelper is a workspace's helper as the team engine uses it: the
// board's writer, pane actions, and agent start and stop.
type fakeTeamHelper struct {
	agentv1.AgentServiceClient

	mu         sync.Mutex
	board      []*agentv1.AppendBoardRequest
	panes      []*agentv1.AgentPaneActionRequest
	started    []*agentv1.StartTeamAgentRequest
	stopped    []string
	nextPane   int
	handoffs   []*agentv1.HandoffFilesRequest
	delivered  []*agentv1.DeliverRoutineRequest
	busy       bool
	prepared   []string
	prepareErr string
	fixed      []string
	listed     []*agentv1.ListHomeFilesRequest
	written    []*agentv1.WriteHomeFilesRequest
	fetched    []*agentv1.FetchAgentBranchRequest
}

func (f *fakeTeamHelper) FetchAgentBranch(_ context.Context, in *agentv1.FetchAgentBranchRequest, _ ...grpc.CallOption) (*agentv1.FetchAgentBranchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, in)
	return &agentv1.FetchAgentBranchResponse{Ref: "refs/remotes/agents/" + in.GetAgentId(), Commit: "0123456789abcdef0123456789abcdef01234567"}, nil
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
			e.starts.Wait()
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

// approveFirstStart makes a team_start decision that names no agent, and
// approves it: the team may start, and nothing has started yet.
func approveFirstStart(t *testing.T, e *teamEngine) {
	t.Helper()
	ctx := context.Background()
	b, err := e.lockBox(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	r := e.decideLocked(ctx, "w", b, boardFrom, typesv1.DecisionKind_DECISION_KIND_TEAM_START, "Start this workspace's agent team?", residualRisk, nil, "")
	b.mu.Unlock()
	if !r.GetOk() {
		t.Fatal(r.GetLine())
	}
	approveTeamStart(t, e)
}

// boardLines is what the helper's board holds, read back as a restarted
// portenvd reads it.
func (f *fakeTeamHelper) boardLines() []*agentv1.ReadBoardResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*agentv1.ReadBoardResponse
	for _, l := range f.board {
		switch {
		case l.GetMessage() != nil:
			out = append(out, &agentv1.ReadBoardResponse{Line: &agentv1.ReadBoardResponse_Message{Message: l.GetMessage()}})
		case l.GetDecision() != nil:
			out = append(out, &agentv1.ReadBoardResponse{Line: &agentv1.ReadBoardResponse_Decision{Decision: l.GetDecision()}})
		}
	}
	return out
}

func (f *fakeTeamHelper) startedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, r := range f.started {
		ids = append(ids, r.GetAgentId())
	}
	return ids
}

// R-0296: the team_start decision names the agents the owner clicked Start
// on, and portenvd starts them itself on Approve, even when it (or the
// app) restarted in between: the start never hangs on the app's memory.
func TestApprovingTheTeamStartStartsTheNamedAgents(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	if _, err := e.startAgent(ctx, "w", "lead"); err == nil {
		t.Fatal("started without the team's first-start approval")
	}
	_, _ = e.startAgent(ctx, "w", "builder")
	_, _ = e.startAgent(ctx, "w", "lead") // named once
	d := e.box("w").teamStartPendingLocked()
	if d == nil || !slices.Equal(d.GetStartAgentIds(), []string{"lead", "builder"}) {
		t.Fatalf("team_start names %v, want [lead builder]", d.GetStartAgentIds())
	}

	// A fresh portenvd: only the board remembers.
	fresh := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { return Team{Agents: defaultTeam()}, nil },
	)
	fresh.now = e.now
	fresh.readBoard = func(context.Context, string) ([]*agentv1.ReadBoardResponse, error) { return h.boardLines(), nil }
	x, err := fresh.answer(ctx, "w", d.GetId(), true, "")
	if err != nil || x.GetState() != typesv1.DecisionState_DECISION_STATE_APPROVED {
		t.Fatalf("answer = %v, %v", x, err)
	}
	// Starting at once, so the app shows it while the helper works.
	if s := fresh.agentInfos(ctx, "w")["lead"].GetState(); s != typesv1.AgentState_AGENT_STATE_STARTING && s != typesv1.AgentState_AGENT_STATE_RUNNING {
		t.Errorf("lead right after Approve: %v", s)
	}
	fresh.starts.Wait()
	if got := h.startedIDs(); !slices.Equal(got, []string{"lead", "builder"}) {
		t.Fatalf("the helper started %v, want [lead builder]", got)
	}
	infos := fresh.agentInfos(ctx, "w")
	for _, id := range []string{"lead", "builder"} {
		if s := infos[id].GetState(); s != typesv1.AgentState_AGENT_STATE_RUNNING {
			t.Errorf("%s: %v, want running", id, s)
		}
	}
	if s := infos["reviewer"].GetState(); s != typesv1.AgentState_AGENT_STATE_STOPPED {
		t.Errorf("reviewer, never asked for: %v", s)
	}
	// An app that still starts the agent itself after Approve starts
	// nothing twice.
	if _, err := fresh.startAgent(ctx, "w", "lead"); err != nil || len(h.startedIDs()) != 2 {
		t.Errorf("a second Start: %v, started %v", err, h.startedIDs())
	}
}

// Declining the team's first start starts nothing; an approved start that
// fails goes back to stopped and tells the owner why.
func TestATeamStartThatCantStartSaysWhy(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	_, _ = e.startAgent(ctx, "w", "lead")
	d := e.box("w").teamStartPendingLocked()
	if _, err := e.answer(ctx, "w", d.GetId(), false, "not today"); err != nil {
		t.Fatal(err)
	}
	e.starts.Wait()
	if len(h.startedIDs()) != 0 || len(h.prepared) != 0 {
		t.Fatalf("Decline started %v", h.startedIDs())
	}

	_, _ = e.startAgent(ctx, "w", "lead")
	d = e.box("w").teamStartPendingLocked()
	h.prepareErr = "~/.ssh can be read by others in this workspace."
	if _, err := e.answer(ctx, "w", d.GetId(), true, ""); err != nil {
		t.Fatal(err)
	}
	e.starts.Wait()
	if len(h.startedIDs()) != 0 {
		t.Fatal("an agent started over an unprepared project")
	}
	if s := e.agentInfos(ctx, "w")["lead"].GetState(); s != typesv1.AgentState_AGENT_STATE_STOPPED {
		t.Errorf("lead after a failed start: %v, want stopped", s)
	}
	told := false
	for _, ev := range e.box("w").events {
		if m := ev.msg; m != nil && m.GetTo() == boardOwner && strings.Contains(m.GetText(), "Lead couldn't start") && strings.Contains(m.GetText(), ".ssh") {
			told = true
			// About Lead, so the app shows it in Lead's column (R-0302).
			if m.GetAbout() != "lead" {
				t.Errorf("the notice is about %q, want lead", m.GetAbout())
			}
		}
	}
	if !told {
		t.Error("the owner wasn't told why Lead didn't start")
	}
	// Lead's row carries the reason as its one line (R-0302).
	if p := e.agentInfos(ctx, "w")["lead"].GetProblem(); p != "Couldn't start: ~/.ssh can be read by others in this workspace." {
		t.Errorf("lead's problem after a failed start: %q", p)
	}
	if p := e.agentInfos(ctx, "w")["builder"].GetProblem(); p != "" {
		t.Errorf("builder's problem: %q, want none", p)
	}

	// The next start that works clears it.
	h.prepareErr = ""
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if p := e.agentInfos(ctx, "w")["lead"].GetProblem(); p != "" {
		t.Errorf("lead's problem after a start that worked: %q", p)
	}
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
			if !strings.Contains(d.GetDetail(), "own copies of your project") || !strings.Contains(d.GetDetail(), "Check their branches before you run them.") {
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

// R-0260 item 1: share is a message, under the same grant, limit and checks.
func TestShareIsAMessageUnderTheGrant(t *testing.T) {
	team := defaultTeam()
	team[2].Grant.Message = nil // reviewer may message no agent, only the owner
	e, h, _ := newTestEngine(t, team...)
	ctx := context.Background()
	share := func(id, text string) *agentv1.TeamCallsRequest {
		return e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: id, Op: &agentv1.TeamCallsResponse_Share{Share: &agentv1.Share{Text: text}}})
	}
	if r := share("reviewer", "done"); !r.GetOk() {
		t.Fatalf("share: %s", r.GetLine())
	}
	if got := inbox(t, e, "lead", "").GetMessages(); len(got) != 0 {
		t.Errorf("lead got a share from an agent not granted to message it: %v", got)
	}
	if r := share("builder", "\x1b]52;c;cHduZWQ=\x07"); r.GetOk() {
		t.Error("a share with an escape sequence went through")
	}
	if r := share("builder", strings.Repeat("x", maxBoardMessage)); r.GetOk() {
		t.Error("a share over 8 KB went through")
	}
	n := len(h.board)
	for i := 0; ; i++ {
		if r := share("builder", fmt.Sprint(i)); !r.GetOk() {
			break
		}
		if i > messagesPerMinute {
			t.Fatal("share has no per-minute limit")
		}
	}
	if got := len(h.board) - n; got > messagesPerMinute {
		t.Errorf("%d share lines in a minute, over the limit of %d", got, messagesPerMinute)
	}
}

// ADR 0022 §2a: share names the agent's own branch; an agent without a
// clone has nothing to share, and another branch is refused.
func TestShareNamesTheAgentsBranch(t *testing.T) {
	team := defaultTeam()
	team[2].Spec.Checkout = CheckoutNone // reviewer
	e, _, _ := newTestEngine(t, team...)
	ctx := context.Background()
	share := func(id, branch string) *agentv1.TeamCallsRequest {
		return e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: id, Op: &agentv1.TeamCallsResponse_Share{Share: &agentv1.Share{Branch: branch, Text: "tests pass"}}})
	}
	if r := share("builder", ""); !r.GetOk() || r.GetMessage().GetText() != "Work is ready on agent/builder: tests pass" {
		t.Errorf("share: %v", r)
	}
	if r := share("builder", "agent/builder"); !r.GetOk() {
		t.Errorf("share agent/builder: %s", r.GetLine())
	}
	if r := share("builder", "main"); r.GetOk() || !strings.Contains(r.GetLine(), "agent/builder") {
		t.Errorf("share main: %v", r)
	}
	if r := share("reviewer", ""); r.GetOk() || !strings.Contains(r.GetLine(), "checkout: none") {
		t.Errorf("share without a clone: %v", r)
	}
}

// ADR 0022 §2a: the owner's Fetch asks the helper for the agent's branch,
// within its home's limit, and tells the agent; an agent without a clone
// has nothing to fetch.
func TestTheOwnersFetch(t *testing.T) {
	team := defaultTeam()
	team[2].Spec.Checkout = CheckoutNone
	e, h, _ := newTestEngine(t, team...)
	ctx := context.Background()
	r, err := e.fetchAgent(ctx, "w", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if r.GetRef() != "refs/remotes/agents/builder" || len(h.fetched) != 1 ||
		h.fetched[0].GetProjectDir() != "/home/work/w" || h.fetched[0].GetMaxBytes() != defaultHomeQuota {
		t.Errorf("fetch: %v, asked %v", r, h.fetched)
	}
	got := inbox(t, e, "builder", "").GetMessages()
	if len(got) != 1 || !strings.Contains(got[0].GetText(), "fetched agent/builder (0123456789ab)") {
		t.Errorf("builder's inbox: %v", got)
	}
	if _, err := e.fetchAgent(ctx, "w", "reviewer"); err == nil || !strings.Contains(err.Error(), "checkout: none") {
		t.Errorf("reviewer: %v", err)
	}
	if _, err := e.fetchAgent(ctx, "w", "ghost"); err == nil {
		t.Error("fetched a stranger's branch")
	}
}

// ADR 0022 §2a: an agent with its own clone starts with the project named,
// so the helper makes the clone; checkout: none starts without one. Its
// AGENTS.md says where it works.
func TestStartNamesTheProjectForTheClone(t *testing.T) {
	team := defaultTeam()
	team[2].Spec.Checkout = CheckoutNone
	e, h, _ := newTestEngine(t, team...)
	ctx := context.Background()
	_, _ = e.startAgent(ctx, "w", "lead") // asks the team's first start
	approveTeamStart(t, e)                // which starts lead
	if _, err := e.startAgent(ctx, "w", "reviewer"); err != nil {
		t.Fatalf("start reviewer: %v", err)
	}
	got := map[string]*agentv1.StartTeamAgentRequest{}
	for _, r := range h.started {
		got[r.GetAgentId()] = r
	}
	if got["lead"].GetCloneProjectDir() != "/home/work/w" {
		t.Errorf("lead's start: clone %q, want /home/work/w", got["lead"].GetCloneProjectDir())
	}
	if !strings.Contains(got["lead"].GetAgentsMd(), "your own copy of the project, ~/w, on branch agent/lead") {
		t.Errorf("lead's AGENTS.md doesn't say where it works: %q", got["lead"].GetAgentsMd())
	}
	if got["reviewer"].GetCloneProjectDir() != "" || strings.Contains(got["reviewer"].GetAgentsMd(), "own copy") {
		t.Errorf("reviewer (checkout: none) got a clone: %v", got["reviewer"])
	}
}

// R-0260 item 2: pending pane requests count, and Approve rechecks.
func TestApproveRechecksThePaneLimit(t *testing.T) {
	team := defaultTeam()
	team[1].Grant.MaxPanes = 1 // builder, panes: ask
	e, h, _ := newTestEngine(t, team...)
	ctx := context.Background()
	first := e.handleCall(ctx, "w", splitCall("builder"))
	if !first.GetOk() {
		t.Fatal(first.GetLine())
	}
	if r := e.handleCall(ctx, "w", splitCall("builder")); r.GetOk() {
		t.Error("a second pane request was queued past max_panes 1")
	}
	// Even if several were queued (an older build), Approve rechecks.
	b := e.box("w")
	b.mu.Lock()
	b.panes["builder"] = []string{"%9"}
	b.mu.Unlock()
	if _, err := e.answer(ctx, "w", first.GetDecision().GetId(), true, ""); err == nil {
		t.Error("Approve opened a pane past max_panes")
	}
	if h.paneActions() != 0 {
		t.Errorf("pane actions: %d", h.paneActions())
	}
	found := false
	for _, m := range inbox(t, e, "builder", "").GetMessages() {
		if strings.Contains(m.GetText(), "limit") {
			found = true
		}
	}
	if !found {
		t.Error("builder wasn't told why")
	}
}

// R-0260 item 3: every text and id field is plain; thread ids are ULIDs.
func TestBoardTextIsPlain(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	bad := []*agentv1.TeamCallsResponse{
		msgCall("builder", "lead", "hi \x1b[2J"),
		msgCall("builder", "lead", "abc\u202edcb"),
		{AgentId: "builder", Op: &agentv1.TeamCallsResponse_Msg{Msg: &agentv1.Msg{To: "lead", Text: "hi", ThreadId: "../../etc", Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE}}},
		{AgentId: "builder", Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: "ok?\x07"}}},
		{AgentId: "builder", Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: "ok?", ThreadId: "not-a-ulid"}}},
		{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Tab{Tab: &agentv1.NewTeamTab{Name: "t\x1b", Argv: []string{"x"}}}},
		{AgentId: "builder", Op: &agentv1.TeamCallsResponse_Split{Split: &agentv1.Split{PaneId: "%1", Argv: []string{"rm\u202e"}}}},
	}
	for _, c := range bad {
		if r := e.handleCall(ctx, "w", c); r.GetOk() {
			t.Errorf("%v: accepted", c)
		}
	}
	if _, err := e.ownerMessage(ctx, "w", "builder", "", "hi\x1b"); err == nil {
		t.Error("the owner's message with an escape was accepted")
	}
	if _, err := e.ownerMessage(ctx, "w", "builder", "x/y", "hi"); err == nil {
		t.Error("the owner's message with a bad thread was accepted")
	}
	if len(h.board) != 0 {
		t.Errorf("board lines written: %d", len(h.board))
	}
	// Newlines and tabs are fine in message text; a real ULID thread is fine.
	ok := msgCall("builder", "lead", "line one\n\tline two")
	ok.GetMsg().ThreadId = newULID(time.Now())
	if r := e.handleCall(ctx, "w", ok); !r.GetOk() {
		t.Errorf("a plain multi-line message: %s", r.GetLine())
	}
}

// R-0260 item 5: a cap on calls in flight per agent.
func TestCallsInFlightAreCapped(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait := &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Inbox{Inbox: &agentv1.Inbox{Wait: true, SinceCursor: "999"}}}
	done := make(chan struct{}, maxCallsInFlight)
	for range maxCallsInFlight {
		go func() { e.handleCall(ctx, "w", wait); done <- struct{}{} }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.inFlight("w", "lead") < maxCallsInFlight && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	r := e.handleCall(ctx, "w", msgCall("lead", "builder", "one more"))
	if r.GetOk() || !strings.Contains(r.GetLine(), "at once") {
		t.Errorf("call past the cap: %v", r)
	}
	if r := e.handleCall(ctx, "w", msgCall("builder", "lead", "another agent")); !r.GetOk() {
		t.Errorf("another agent was capped too: %s", r.GetLine())
	}
	cancel()
	for range maxCallsInFlight {
		<-done
	}
	if n := e.inFlight("w", "lead"); n != 0 {
		t.Errorf("%d calls still counted", n)
	}
}

// R-0260 item 6: an overdue decision can't be approved, even if its
// expiry line wasn't written.
func TestAnOverdueDecisionCantBeApproved(t *testing.T) {
	e, h, clock := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	d := e.handleCall(ctx, "w", splitCall("builder")).GetDecision()
	clock.add(decisionExpiry + time.Second)
	// Approve directly, past the sweep: the state is still pending.
	b := e.box("w")
	b.mu.Lock()
	_, err := e.answerLocked(ctx, "w", b, d.GetId(), true, "")
	b.mu.Unlock()
	if err == nil {
		t.Fatal("an overdue decision was approved")
	}
	if h.paneActions() != 0 {
		t.Error("a pane opened for an overdue decision")
	}
}

// R-0260 item 4: a reader never sees half a rebuild of the agent files.
func TestTeamFilesRebuildsDontInterleave(t *testing.T) {
	state := t.TempDir()
	sets := [][]*agentv1.AgentFileContent{
		{{Name: "lead.md", Content: []byte("a")}, {Name: "builder.md", Content: []byte("a")}, {Name: "reviewer.md", Content: []byte("a")}},
		{{Name: "designer.md", Content: []byte("b")}, {Name: "writer.md", Content: []byte("b")}},
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set := sets[i%2]
			errs <- withTeamFiles(state, set, func(dir string) error {
				entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(agentFilesRelative)))
				if err != nil {
					return err
				}
				if len(entries) != len(set) {
					return fmt.Errorf("saw %d files, want %d", len(entries), len(set))
				}
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func setStatus(e *teamEngine, id string, s *agentv1.SetStatus) *agentv1.TeamCallsRequest {
	return e.handleCall(context.Background(), "w", &agentv1.TeamCallsResponse{AgentId: id, Op: &agentv1.TeamCallsResponse_SetStatus{SetStatus: s}})
}

// R-0256: an agent sets and clears its own status, within the limits.
func TestAnAgentsStatus(t *testing.T) {
	e, _, clock := newTestEngine(t, defaultTeam()...)
	if r := setStatus(e, "builder", &agentv1.SetStatus{Text: "Running the payments tests", ProgressDone: 3, ProgressTotal: 8}); !r.GetOk() {
		t.Fatal(r.GetLine())
	}
	list := e.agentInfos(context.Background(), "w")
	if st := list["builder"].GetStatus(); st.GetText() != "Running the payments tests" || st.GetProgressDone() != 3 || st.GetSetAt() == nil {
		t.Errorf("status = %v", st)
	}
	// One update every 10 s.
	if r := setStatus(e, "builder", &agentv1.SetStatus{Text: "again"}); r.GetOk() {
		t.Error("a second update within 10 s")
	}
	clock.add(statusEvery)
	for name, s := range map[string]*agentv1.SetStatus{
		"long":     {Text: strings.Repeat("x", maxStatus+1)},
		"escape":   {Text: "ok\x1b[2J"},
		"newline":  {Text: "one\ntwo"},
		"blocked":  {Text: "x", Blocked: "needs \u202ekey"},
		"progress": {Text: "x", ProgressDone: 9, ProgressTotal: 8},
		"empty":    {Text: ""},
	} {
		if r := setStatus(e, "builder", s); r.GetOk() {
			t.Errorf("%s: accepted", name)
		}
	}
	if r := setStatus(e, "builder", &agentv1.SetStatus{Clear: true}); !r.GetOk() {
		t.Fatal(r.GetLine())
	}
	if st := e.agentInfos(context.Background(), "w")["builder"].GetStatus(); st != nil {
		t.Errorf("after clear: %v", st)
	}
}

// R-0257 item 1: team status only with the grant, and never terminals.
func TestTeamStatusNeedsTheGrant(t *testing.T) {
	team := defaultTeam()
	team[0].Grant.TeamStatus = true // lead
	e, _, _ := newTestEngine(t, team...)
	ctx := context.Background()
	setStatus(e, "builder", &agentv1.SetStatus{Text: "building", Blocked: "needs the staging key"})
	ask := func(id string) *agentv1.TeamCallsRequest {
		return e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: id, Op: &agentv1.TeamCallsResponse_TeamStatus{TeamStatus: &agentv1.TeamStatus{}}})
	}
	if r := ask("builder"); r.GetOk() {
		t.Error("team status without the grant")
	}
	r := ask("lead")
	if !r.GetOk() {
		t.Fatal(r.GetLine())
	}
	var b *agentv1.Teammate
	for _, m := range r.GetTeamStatus().GetTeammates() {
		if m.GetId() == "builder" {
			b = m
		}
	}
	if b == nil || b.GetStatus().GetBlocked() != "needs the staging key" {
		t.Errorf("lead sees builder as %v", b)
	}
	// whoami: teammates' status only with the grant.
	who := func(id string) *agentv1.WhoAmIResult {
		return e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: id, Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}).GetWhoami()
	}
	for _, m := range who("reviewer").GetTeammates() {
		if m.GetStatus() != nil {
			t.Errorf("reviewer (no grant) sees %s's status", m.GetId())
		}
	}
	if w := who("builder"); w.GetStatus().GetText() != "building" {
		t.Errorf("builder's own status in whoami: %v", w.GetStatus())
	}
}

// R-0256: Ask for Status is a status_request in the agent's inbox.
func TestTheOwnerAsksForStatus(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	m, err := e.ownerMessageOf(context.Background(), "w", "builder", "", "What are you working on?", typesv1.MessageType_MESSAGE_TYPE_STATUS_REQUEST)
	if err != nil || m.GetType() != typesv1.MessageType_MESSAGE_TYPE_STATUS_REQUEST {
		t.Fatalf("%v, %v", m, err)
	}
	if _, err := e.ownerMessageOf(context.Background(), "w", "builder", "", "x", typesv1.MessageType_MESSAGE_TYPE_ROUTINE); err == nil {
		t.Error("the owner sent a routine-typed message")
	}
}

func (f *fakeTeamHelper) HandoffFiles(_ context.Context, in *agentv1.HandoffFilesRequest, _ ...grpc.CallOption) (*agentv1.HandoffFilesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handoffs = append(f.handoffs, in)
	return &agentv1.HandoffFilesResponse{Files: []*typesv1.HandoffFile{{Path: "inbox/" + in.GetFromAgent() + "/x/v2.fig", Sha256: strings.Repeat("a", 64), Size: 10}}}, nil
}

// R-0257 item 3: a hand-off goes only where the message grant allows,
// only from the sender's home or the project, and posts the files.
func TestHandoff(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	hand := func(from, to, path, note string) *agentv1.TeamCallsRequest {
		return e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: from, Op: &agentv1.TeamCallsResponse_Handoff{Handoff: &agentv1.Handoff{To: to, Path: path, Note: note}}})
	}
	r := hand("builder", "lead", "/home/builder/design/v2", "revision 2")
	if !r.GetOk() || r.GetMessage().GetType() != typesv1.MessageType_MESSAGE_TYPE_HANDOFF || len(r.GetMessage().GetFiles()) != 1 {
		t.Fatalf("handoff: %v", r)
	}
	if got := h.handoffs[0]; got.GetFromAgent() != "builder" || got.GetToAgent() != "lead" || got.GetRecipientQuotaBytes() <= 0 {
		t.Errorf("helper asked %v", got)
	}
	if in := inbox(t, e, "lead", "").GetMessages(); len(in) != 1 || len(in[0].GetFiles()) != 1 {
		t.Errorf("lead's inbox: %v", in)
	}
	for name, c := range map[string][4]string{
		"not granted":         {"builder", "reviewer", "/home/builder/x", ""},
		"to itself":           {"builder", "builder", "/home/builder/x", ""},
		"another's home":      {"builder", "lead", "/home/reviewer/secret", ""},
		"the owner's dotfile": {"builder", "lead", "/home/work/.ssh/id_ed25519", ""},
		"outside home":        {"builder", "lead", "/etc/shadow", ""},
		"dot-dot":             {"builder", "lead", "/home/builder/../work/.ssh", ""},
		"relative":            {"builder", "lead", "design/v2", ""},
		"bad note":            {"builder", "lead", "/home/builder/x", "hi\x1b[2J"},
	} {
		if r := hand(c[0], c[1], c[2], c[3]); r.GetOk() {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(h.handoffs) != 1 {
		t.Errorf("the helper was asked %d times", len(h.handoffs))
	}
	// From the project is fine.
	if r := hand("builder", "lead", "/home/work/w/exports/report.pdf", ""); !r.GetOk() {
		t.Errorf("from the project: %s", r.GetLine())
	}
}

func (f *fakeTeamHelper) DeliverRoutine(_ context.Context, in *agentv1.DeliverRoutineRequest, _ ...grpc.CallOption) (*agentv1.DeliverRoutineResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return nil, status.Error(codes.FailedPrecondition, "busy")
	}
	f.delivered = append(f.delivered, in)
	return &agentv1.DeliverRoutineResponse{}, nil
}

func routineTeam() []TeamAgent {
	team := defaultTeam()
	team[0].Spec.Routines = []AgentRoutine{{EveryMinutes: 30, Note: "Status round"}}
	return team
}

func startedWithRoutine(t *testing.T) (*teamEngine, *fakeTeamHelper, *testClock) {
	t.Helper()
	e, h, clock := newTestEngine(t, routineTeam()...)
	approveFirstStart(t, e)
	if _, err := e.startAgent(context.Background(), "w", "lead"); err != nil {
		t.Fatal(err)
	}
	return e, h, clock
}

func routineNotices(t *testing.T, e *teamEngine, id string) int {
	n := 0
	for _, m := range inbox(t, e, id, "").GetMessages() {
		if m.GetType() == typesv1.MessageType_MESSAGE_TYPE_ROUTINE {
			n++
		}
	}
	return n
}

// R-0258: Portenv fires a routine on time, into the agent's own pane when
// it's idle, and into its inbox.
func TestARoutineFiresOnTime(t *testing.T) {
	e, h, clock := startedWithRoutine(t)
	ctx := context.Background()
	e.tickRoutines(ctx)
	clock.add(29 * time.Minute)
	e.tickRoutines(ctx)
	if len(h.delivered) != 0 {
		t.Fatal("fired early")
	}
	clock.add(time.Minute)
	e.tickRoutines(ctx)
	if len(h.delivered) != 1 || h.delivered[0].GetAgentId() != "lead" || h.delivered[0].GetText() != "Status round" {
		t.Fatalf("delivered = %v", h.delivered)
	}
	if n := routineNotices(t, e, "lead"); n != 1 {
		t.Errorf("%d routine notices in lead's inbox, want 1", n)
	}
	if info := e.agentInfos(ctx, "w")["lead"]; len(info.GetRoutines()) != 1 || info.GetRoutines()[0].GetEveryMinutes() != 30 {
		t.Errorf("routines on the row: %v", info.GetRoutines())
	}
}

// Busy: it waits for the next idle, and missed fires collapse into one.
func TestABusyAgentGetsOneCollapsedRoutine(t *testing.T) {
	e, h, clock := startedWithRoutine(t)
	ctx := context.Background()
	h.busy = true
	clock.add(30 * time.Minute)
	e.tickRoutines(ctx)
	clock.add(30 * time.Minute)
	e.tickRoutines(ctx)
	if len(h.delivered) != 0 {
		t.Fatal("typed into a busy agent")
	}
	h.busy = false
	clock.add(time.Minute)
	e.tickRoutines(ctx)
	if len(h.delivered) != 1 || !strings.Contains(h.delivered[0].GetText(), "missed 2 rounds") {
		t.Fatalf("delivered = %v", h.delivered)
	}
	clock.add(time.Minute)
	e.tickRoutines(ctx)
	if len(h.delivered) != 1 {
		t.Errorf("delivered again: %v", h.delivered)
	}
}

// Paused, or the agent stopped: nothing fires.
func TestPausedAndStoppedRoutinesDontFire(t *testing.T) {
	e, h, clock := startedWithRoutine(t)
	ctx := context.Background()
	if _, err := e.setRoutinePaused(ctx, "w", "lead", 0, true); err != nil {
		t.Fatal(err)
	}
	clock.add(time.Hour)
	e.tickRoutines(ctx)
	if len(h.delivered) != 0 || routineNotices(t, e, "lead") != 0 {
		t.Fatal("a paused routine fired")
	}
	if _, err := e.setRoutinePaused(ctx, "w", "lead", 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.setRoutinePaused(ctx, "w", "lead", 3, true); err == nil {
		t.Error("paused a routine that doesn't exist")
	}
	if _, err := e.stopAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	clock.add(time.Hour)
	e.tickRoutines(ctx)
	if len(h.delivered) != 0 {
		t.Error("a stopped agent's routine fired")
	}
}

// R-0258 with lane A: routines need the runner's idle pattern, which goes
// to the helper with the start; a runner Portenv doesn't know gets none,
// and says so.
func TestRoutinesNeedAKnownRunner(t *testing.T) {
	_, h, _ := startedWithRoutine(t)
	if p := h.started[0].GetIdlePattern(); p == "" || p != runnerIdlePattern([]string{"claude"}) {
		t.Errorf("idle pattern sent: %q", p)
	}

	team := routineTeam()
	team[0].Runner, team[0].RunnerLine = []string{"/usr/local/bin/aider"}, "aider"
	team[0].Spec.Runner = team[0].Runner
	e, h, clock := newTestEngine(t, team...)
	approveFirstStart(t, e)
	ctx := context.Background()
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	clock.add(time.Hour)
	e.tickRoutines(ctx)
	if len(h.delivered) != 0 || routineNotices(t, e, "lead") != 0 {
		t.Error("a routine fired for a runner with no idle pattern")
	}
	info := e.agentInfos(ctx, "w")["lead"]
	if !strings.Contains(info.GetRoutinesOff(), "aider") {
		t.Errorf("the row doesn't say why: %q", info.GetRoutinesOff())
	}
	w := e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}).GetWhoami()
	if !strings.Contains(w.GetRoutinesOff(), "Routines are off") {
		t.Errorf("whoami doesn't say why: %q", w.GetRoutinesOff())
	}
}

func TestTheRunnerTablesPatterns(t *testing.T) {
	re := regexp.MustCompile(runnerIdlePattern([]string{"claude", "--permission-mode", "acceptEdits"}))
	for _, idle := range []string{"> ", "│ >                │", "  >"} {
		if !re.MatchString(idle) {
			t.Errorf("%q isn't idle", idle)
		}
	}
	for _, busy := range []string{"> fix the tests", "Do you want to proceed? (y/n)", "✻ Thinking…", ""} {
		if re.MatchString(busy) {
			t.Errorf("%q counted as idle", busy)
		}
	}
	if runnerIdlePattern([]string{"bash"}) != "" || runnerIdlePattern(nil) != "" {
		t.Error("an unknown runner has a pattern")
	}
}

func (f *fakeTeamHelper) PrepareTeamProject(_ context.Context, in *agentv1.PrepareTeamProjectRequest, _ ...grpc.CallOption) (*agentv1.PrepareTeamProjectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, in.GetProjectDir())
	if f.prepareErr != "" {
		return nil, status.Error(codes.FailedPrecondition, f.prepareErr)
	}
	return &agentv1.PrepareTeamProjectResponse{Fixed: f.fixed}, nil
}

// ADR 0022 §2: the project is opened to the team before its first agent
// starts, and a refusal (open dotfiles, no ACLs) stops the start.
func TestTheProjectIsPreparedBeforeAgentsStart(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	approveFirstStart(t, e)
	ctx := context.Background()
	h.prepareErr = "~/.ssh can be read by others in this workspace."
	if _, err := e.startAgent(ctx, "w", "lead"); err == nil || !strings.Contains(err.Error(), ".ssh") {
		t.Fatalf("start = %v, want the helper's line", err)
	}
	if len(h.started) != 0 {
		t.Fatal("an agent started over an unprepared project")
	}
	h.prepareErr = ""
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.startAgent(ctx, "w", "builder"); err != nil {
		t.Fatal(err)
	}
	if got := h.prepared; len(got) < 2 || got[len(got)-1] != "/home/work/w" {
		t.Errorf("prepared %v", got)
	}
}

// R-0261: whoami lists only what works; the first start says what changes.
func TestWhoAmIListsOnlyWhatWorks(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	w := e.handleCall(context.Background(), "w", &agentv1.TeamCallsResponse{AgentId: "builder", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}).GetWhoami()
	for _, c := range w.GetCommands() {
		if strings.Contains(c, "propose") || c == "request-package" {
			t.Errorf("whoami lists %q, which isn't in this build", c)
		}
	}
	_, _ = e.startAgent(context.Background(), "w", "lead")
	for _, d := range e.box("w").decisions {
		if d.GetKind() == typesv1.DecisionKind_DECISION_KIND_TEAM_START && !strings.Contains(d.GetDetail(), "Stop Team doesn't change it back") {
			t.Errorf("team_start detail: %q", d.GetDetail())
		}
	}
}

type readHomeClient struct {
	agentv1.AgentService_ReadHomeFileClient
	chunks [][]byte
}

func (r *readHomeClient) Recv() (*agentv1.ReadHomeFileResponse, error) {
	if len(r.chunks) == 0 {
		return nil, io.EOF
	}
	c := r.chunks[0]
	r.chunks = r.chunks[1:]
	return &agentv1.ReadHomeFileResponse{Data: c}, nil
}

type writeHomeClient struct {
	agentv1.AgentService_WriteHomeFilesClient
	f *fakeTeamHelper
}

func (w *writeHomeClient) Send(m *agentv1.WriteHomeFilesRequest) error {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	w.f.written = append(w.f.written, m)
	return nil
}

func (w *writeHomeClient) CloseAndRecv() (*agentv1.WriteHomeFilesResponse, error) {
	return &agentv1.WriteHomeFilesResponse{Added: []*typesv1.AgentFile{{Path: "brief.md", Size: 5}}}, nil
}

func (f *fakeTeamHelper) ListHomeFiles(_ context.Context, in *agentv1.ListHomeFilesRequest, _ ...grpc.CallOption) (*agentv1.ListHomeFilesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, in)
	return &agentv1.ListHomeFilesResponse{Files: []*typesv1.AgentFile{{Path: "notes.md", Size: 3}}, HomeBytes: 3}, nil
}

func (f *fakeTeamHelper) ReadHomeFile(_ context.Context, in *agentv1.ReadHomeFileRequest, _ ...grpc.CallOption) (agentv1.AgentService_ReadHomeFileClient, error) {
	return &readHomeClient{chunks: [][]byte{[]byte("he"), []byte("llo")}}, nil
}

func (f *fakeTeamHelper) WriteHomeFiles(_ context.Context, _ ...grpc.CallOption) (agentv1.AgentService_WriteHomeFilesClient, error) {
	return &writeHomeClient{f: f}, nil
}

// The Agent files view (ADR 0022 §2): only for agents of the team, with
// the quota from portenvd, relayed to the helper (which does the file work
// as the agent, R-0254 rule 1).
func TestTheAgentFilesView(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	files, used, quota, err := e.listAgentFiles(ctx, "w", "builder", "")
	if err != nil || len(files) != 1 || used != 3 || quota != teamkit.DefaultHomeQuota {
		t.Fatalf("list: %v %d %d %v", files, used, quota, err)
	}
	if h.listed[0].GetAgentId() != "builder" {
		t.Errorf("listed %v", h.listed[0])
	}
	var got []byte
	if err := e.readAgentFile(ctx, "w", "builder", "notes.md", func(b []byte) error { got = append(got, b...); return nil }); err != nil || string(got) != "hello" {
		t.Errorf("read: %q, %v", got, err)
	}
	in := []*daemonv1.AddAgentFilesRequest{
		{Name: "w", AgentId: "builder", Path: "brief.md", Data: []byte("brief"), Last: true},
	}
	added, err := e.addAgentFiles(ctx, "w", "builder", func() (*daemonv1.AddAgentFilesRequest, error) {
		if len(in) == 0 {
			return nil, io.EOF
		}
		m := in[0]
		in = in[1:]
		return m, nil
	})
	if err != nil || len(added) != 1 {
		t.Fatalf("add: %v, %v", added, err)
	}
	first := h.written[0]
	if first.GetAgentId() != "builder" || first.GetHomeQuotaBytes() != teamkit.DefaultHomeQuota || first.GetPath() != "brief.md" {
		t.Errorf("sent %v", first)
	}
	for _, bad := range []string{"nobody", "../x", "owner"} {
		if _, _, _, err := e.listAgentFiles(ctx, "w", bad, ""); err == nil {
			t.Errorf("listed %q's files", bad)
		}
	}
}

func TestADragAndDropGoesToOneAgent(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	in := []*daemonv1.AddAgentFilesRequest{
		{Name: "w", AgentId: "builder", Path: "a.md", Data: []byte("a"), Last: true},
		{AgentId: "lead", Path: "b.md", Data: []byte("b"), Last: true},
	}
	_, err := e.addAgentFiles(context.Background(), "w", "builder", func() (*daemonv1.AddAgentFilesRequest, error) {
		if len(in) == 0 {
			return nil, io.EOF
		}
		m := in[0]
		in = in[1:]
		return m, nil
	})
	if err == nil {
		t.Error("a second agent mid-stream was accepted")
	}
}

// R-0262: what init made private in the owner's home reaches the owner.
func TestTheOwnerHearsWhatWasMadePrivate(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	approveFirstStart(t, e)
	h.fixed = []string{"~/.npmrc: 644 → 640"}
	if _, err := e.startAgent(context.Background(), "w", "lead"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range e.box("w").events {
		if ev.msg != nil && ev.msg.GetTo() == "owner" && strings.Contains(ev.msg.GetText(), "~/.npmrc") {
			found = true
		}
	}
	if !found {
		t.Error("the owner wasn't told")
	}
}

// An agent's TeamCalls can't approve an agent file (R-0285, R-0288). No op
// an agent can send names an approval, the agent service has no approve
// method, and every op an agent can send, with its text claiming an
// approval, leaves the file unapproved: the owner's ApproveAgentFile
// (DaemonService, on portenvd's own socket) is the only way.
func TestTeamCallsNeverApproveAnAgentFile(t *testing.T) {
	// The ops an agent can send. A new one fails here until someone checks
	// it can't approve anything, then adds it.
	ops := (&agentv1.TeamCallsResponse{}).ProtoReflect().Descriptor().Oneofs().ByName("op").Fields()
	var names []string
	for i := 0; i < ops.Len(); i++ {
		names = append(names, string(ops.Get(i).Name()))
	}
	want := []string{"split", "tab", "focus", "close", "msg", "ask_owner", "inbox", "share", "propose", "request_package", "whoami",
		"set_status", "team_status", "handoff", "propose_routine"}
	if !slices.Equal(names, want) {
		t.Fatalf("TeamCalls ops are %v, want %v: check a new op can't approve an agent file, then add it here", names, want)
	}
	methods := agentv1.File_portenv_agent_v1_agent_proto.Services().ByName("AgentService").Methods()
	for i := 0; i < methods.Len(); i++ {
		if n := strings.ToLower(string(methods.Get(i).Name())); strings.Contains(n, "approve") {
			t.Errorf("the agent service has %s: approving is the owner's, on portenvd's socket", methods.Get(i).Name())
		}
	}

	// Lead is approved by the owner; builder's file isn't. Lead tries every
	// op, each saying "approve builder" with the file's exact hash.
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	if err := ApproveAgentFile(src, "lead", sha(simpleAgent("lead"))); err != nil {
		t.Fatal(err)
	}
	h := &fakeTeamHelper{}
	e := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { return LoadTeam(src) },
	)
	claim := "approve builder " + sha(builderFile)
	calls := []*agentv1.TeamCallsResponse{
		{Op: &agentv1.TeamCallsResponse_Msg{Msg: &agentv1.Msg{To: "owner", Text: claim, Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE}}},
		{Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: claim}}},
		{Op: &agentv1.TeamCallsResponse_Share{Share: &agentv1.Share{Text: claim}}},
		{Op: &agentv1.TeamCallsResponse_Propose{Propose: &agentv1.ProposeAgentChange{AgentId: "builder", Content: builderFile}}},
		{Op: &agentv1.TeamCallsResponse_RequestPackage{RequestPackage: &agentv1.RequestPackage{Name: "jq", Reason: claim}}},
		{Op: &agentv1.TeamCallsResponse_Inbox{Inbox: &agentv1.Inbox{}}},
		{Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}},
		{Op: &agentv1.TeamCallsResponse_Split{Split: &agentv1.Split{PaneId: "%1", Argv: []string{"portenv", "approve", "builder"}}}},
		{Op: &agentv1.TeamCallsResponse_Tab{Tab: &agentv1.NewTeamTab{Name: claim}}},
		{Op: &agentv1.TeamCallsResponse_Focus{Focus: &agentv1.PaneRef{PaneId: "%1"}}},
		{Op: &agentv1.TeamCallsResponse_Close{Close: &agentv1.PaneRef{PaneId: "%1"}}},
		{Op: &agentv1.TeamCallsResponse_SetStatus{SetStatus: &agentv1.SetStatus{Text: claim}}},
		{Op: &agentv1.TeamCallsResponse_TeamStatus{TeamStatus: &agentv1.TeamStatus{}}},
		{Op: &agentv1.TeamCallsResponse_Handoff{Handoff: &agentv1.Handoff{To: "owner", Path: "/home/lead/builder.md", Note: claim}}},
		{Op: &agentv1.TeamCallsResponse_ProposeRoutine{ProposeRoutine: &agentv1.ProposeRoutine{EveryMinutes: 5, Note: claim}}},
	}
	if len(calls) != len(want) {
		t.Fatalf("%d calls for %d ops", len(calls), len(want))
	}
	for i, c := range calls {
		c.CallId, c.AgentId = fmt.Sprint(i), "lead"
		e.handleCall(context.Background(), "w", c)
		// Builder itself can't either, though it isn't approved to call.
		c.AgentId = "builder"
		e.handleCall(context.Background(), "w", c)
	}
	if loadAgent(t, src, "builder").Approved {
		t.Fatal("an agent's call approved builder's file")
	}
	approvals, err := loadAgentApprovals(src.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := approvals["builder"]; ok || len(approvals) != 1 {
		t.Fatalf("approvals after the agents' calls: %v", approvals)
	}
	// The owner's Approve, with the hash of the text the app showed, does.
	if err := ApproveAgentFile(src, "builder", sha(builderFile)); err != nil {
		t.Fatal(err)
	}
	if !loadAgent(t, src, "builder").Approved {
		t.Fatal("the owner's Approve didn't approve")
	}
}

// An agent file added, changed or removed while the workspace is open
// reaches the app without reopening it.
func TestSyncAgentsFollowsTheFiles(t *testing.T) {
	e, _, _ := newTestEngine(t)
	team := []TeamAgent{testAgent("lead", grant(typesv1.GrantMode_GRANT_MODE_ASK, 2, nil, true))}
	e.loadTeam = func(context.Context, string) (Team, error) { return Team{Agents: slices.Clone(team)}, nil }
	ctx := context.Background()
	look := func() []teamEvent {
		t.Helper()
		n := len(e.box("w").events)
		if err := e.syncAgents(ctx, "w"); err != nil {
			t.Fatal(err)
		}
		return e.box("w").events[n:]
	}
	if evs := look(); len(evs) != 0 {
		t.Fatalf("the first look only records: %v", evs)
	}
	team = append(team, testAgent("builder", grant(typesv1.GrantMode_GRANT_MODE_ASK, 2, nil, true)))
	if evs := look(); len(evs) != 1 || evs[0].agent.GetId() != "builder" {
		t.Fatalf("added: %v", evs)
	}
	if evs := look(); len(evs) != 0 {
		t.Fatalf("nothing changed: %v", evs)
	}
	team[0].SHA256, team[0].Approved = "def", false
	if evs := look(); len(evs) != 1 || evs[0].agent.GetId() != "lead" || evs[0].agent.GetApproved() {
		t.Fatalf("changed: %v", evs)
	}
	team = team[1:]
	if evs := look(); len(evs) != 1 || evs[0].removed != "lead" {
		t.Fatalf("removed: %v", evs)
	}
}

// share_login isn't in the tester build (R-0323): an agent whose file asks
// for it starts with nothing shared and no decision asked, and both its
// column and its whoami say so.
func TestShareLoginIsSaidToBeOff(t *testing.T) {
	builder := testAgent("builder", grant(typesv1.GrantMode_GRANT_MODE_ASK, 2, []string{"lead"}, true))
	builder.ShareLogin = []string{"claude", "gh"}
	e, h, _ := newTestEngine(t, testAgent("lead", grant(typesv1.GrantMode_GRANT_MODE_ALLOW, 4, []string{"builder"}, true)), builder)
	approveFirstStart(t, e)
	ctx := context.Background()
	if _, err := e.startAgent(ctx, "w", "builder"); err != nil {
		t.Fatal(err)
	}
	if len(h.startedIDs()) != 1 {
		t.Fatalf("started %v", h.startedIDs())
	}
	for _, d := range e.box("w").decisions {
		if d.GetKind() == typesv1.DecisionKind_DECISION_KIND_SHARE_LOGIN {
			t.Fatalf("a share_login decision was asked: %v", d)
		}
	}
	infos := e.agentInfos(ctx, "w")
	want := "Sharing your sign-in (claude, gh) isn't in this build, so nothing is shared. Sign Builder in from its own terminal if it needs to."
	if got := infos["builder"].GetShareLoginOff(); got != want {
		t.Errorf("builder's column: %q\nwant %q", got, want)
	}
	if got := infos["lead"].GetShareLoginOff(); got != "" {
		t.Errorf("lead asks for nothing, but its column says %q", got)
	}
	w := e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: "builder", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}).GetWhoami()
	if got := w.GetShareLoginOff(); !strings.Contains(got, "isn't supported in this build") || !strings.Contains(got, "claude, gh") {
		t.Errorf("builder's whoami: %q", got)
	}
	lw := e.handleCall(ctx, "w", &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}).GetWhoami()
	if got := lw.GetShareLoginOff(); got != "" {
		t.Errorf("lead's whoami: %q", got)
	}
}

// An agent's home_seed reaches the helper with the project to copy it from
// (ADR 0022 §2, R-0323).
func TestHomeSeedReachesTheHelper(t *testing.T) {
	builder := testAgent("builder", grant(typesv1.GrantMode_GRANT_MODE_ASK, 2, []string{"lead"}, true))
	builder.Spec.HomeSeed = []string{"docs/style-guide.md"}
	e, h, _ := newTestEngine(t, builder)
	approveFirstStart(t, e)
	if _, err := e.startAgent(context.Background(), "w", "builder"); err != nil {
		t.Fatal(err)
	}
	if len(h.started) != 1 {
		t.Fatalf("started %v", h.started)
	}
	r := h.started[0]
	if !slices.Equal(r.GetHomeSeed(), []string{"docs/style-guide.md"}) || r.GetProjectDir() != "/home/work/w" || r.GetHomeQuotaBytes() <= 0 {
		t.Errorf("request: seed %v, project %q, quota %d", r.GetHomeSeed(), r.GetProjectDir(), r.GetHomeQuotaBytes())
	}
}
