// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// liveHelper is a workspace's helper that knows which agents' terminals
// really run in the box, as ListTabs shows them, and loses them all when
// the box restarts (CC-0747).
type liveHelper struct {
	*fakeTeamHelper
	mu   sync.Mutex
	live map[string]bool
}

func (l *liveHelper) StartTeamAgent(ctx context.Context, in *agentv1.StartTeamAgentRequest, opts ...grpc.CallOption) (*agentv1.StartTeamAgentResponse, error) {
	r, err := l.fakeTeamHelper.StartTeamAgent(ctx, in, opts...)
	if err == nil {
		l.mu.Lock()
		l.live[in.GetAgentId()] = true
		l.mu.Unlock()
	}
	return r, err
}

func (l *liveHelper) StopTeamAgent(ctx context.Context, in *agentv1.StopTeamAgentRequest, opts ...grpc.CallOption) (*agentv1.StopTeamAgentResponse, error) {
	l.mu.Lock()
	delete(l.live, in.GetAgentId())
	l.mu.Unlock()
	return l.fakeTeamHelper.StopTeamAgent(ctx, in, opts...)
}

func (l *liveHelper) ListTabs(_ context.Context, in *agentv1.ListTabsRequest, _ ...grpc.CallOption) (*agentv1.ListTabsResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tabs := []*typesv1.Tab{{Id: "@0", Name: "work"}}
	for _, id := range slices.Sorted(maps.Keys(l.live)) {
		tabs = append(tabs, &typesv1.Tab{Id: id + ":@1", Name: id, AgentId: id, PaneIds: []string{"%1"}})
	}
	return &agentv1.ListTabsResponse{Tabs: tabs}, nil
}

// restart is the box stopping and starting again: nothing of the team runs.
func (l *liveHelper) restart() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live = map[string]bool{}
}

// resumeEngine is a portenvd's team engine over helper h, remembering
// which agents run in dir, as portenvd does in its state folder.
func resumeEngine(h *liveHelper, dir string, team ...TeamAgent) *teamEngine {
	e := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { return Team{Agents: team}, nil },
	)
	e.readBoard = func(context.Context, string) ([]*agentv1.ReadBoardResponse, error) { return h.boardLines(), nil }
	e.wantedFile = func(name string) string { return filepath.Join(dir, name+"-team-running.json") }
	return e
}

func newLiveHelper() *liveHelper {
	return &liveHelper{fakeTeamHelper: &fakeTeamHelper{}, live: map[string]bool{}}
}

// startedTeam is a workspace whose team's first start was approved, with
// lead and builder started; reviewer was never started.
func startedTeam(t *testing.T) (*teamEngine, *liveHelper, string) {
	t.Helper()
	h, dir := newLiveHelper(), t.TempDir()
	e := resumeEngine(h, dir, defaultTeam()...)
	approveFirstStart(t, e)
	ctx := context.Background()
	for _, id := range []string{"lead", "builder"} {
		if _, err := e.startAgent(ctx, "w", id); err != nil {
			t.Fatal(err)
		}
	}
	return e, h, dir
}

func states(e *teamEngine) map[string]typesv1.AgentState {
	out := map[string]typesv1.AgentState{}
	for id, info := range e.agentInfos(context.Background(), "w") {
		out[id] = info.GetState()
	}
	return out
}

// CC-0747, defect 2: after the box restarts, the Team sidebar shows the
// box's real state, never the last one recorded. A box that just started
// has no agent running and no agent tab.
func TestARestartedBoxShowsNoAgentRunning(t *testing.T) {
	team := defaultTeam()
	h, dir := newLiveHelper(), t.TempDir()
	e := resumeEngine(h, dir, team...)
	approveFirstStart(t, e)
	ctx := context.Background()
	if _, err := e.startAgent(ctx, "w", "lead"); err != nil {
		t.Fatal(err)
	}
	if s := states(e)["lead"]; s != typesv1.AgentState_AGENT_STATE_RUNNING {
		t.Fatalf("lead before the restart: %v", s)
	}
	// Lead's file changed while the box was closed and isn't approved
	// again, so nothing can start it: it must show as stopped, with no tab.
	team[0].Approved = false
	h.restart()
	e.boxStarted(ctx, "w")
	e.starts.Wait()
	info := e.agentInfos(ctx, "w")["lead"]
	if info.GetState() != typesv1.AgentState_AGENT_STATE_STOPPED {
		t.Errorf("lead after the box restarted: %v, want stopped", info.GetState())
	}
	if len(info.GetTabIds()) != 0 {
		t.Errorf("lead's tabs after the box restarted: %v, want none", info.GetTabIds())
	}
	if len(h.startedIDs()) != 1 {
		t.Errorf("the helper started %v; an unapproved file must not start", h.startedIDs())
	}
	if !strings.Contains(info.GetProblem(), "approve lead's file first") {
		t.Errorf("lead's line: %q, want why it didn't start again", info.GetProblem())
	}
	if e.box("w").routines["lead"] != nil {
		t.Error("a stopped agent's routines are still timed")
	}
}

// The other way round: a box taken over still running (portenvd
// restarted, ADR 0023) shows the agents whose terminals really run.
func TestATakenOverBoxShowsTheAgentsThatRun(t *testing.T) {
	h, dir := newLiveHelper(), t.TempDir()
	h.live["builder"] = true
	e := resumeEngine(h, dir, defaultTeam()...)
	ctx := context.Background()
	if !e.boxStarted(ctx, "w") {
		t.Error("boxStarted says nothing of the team runs")
	}
	e.starts.Wait()
	got := states(e)
	if got["builder"] != typesv1.AgentState_AGENT_STATE_RUNNING {
		t.Errorf("builder, running in the box: %v", got["builder"])
	}
	if got["lead"] != typesv1.AgentState_AGENT_STATE_STOPPED {
		t.Errorf("lead, not running: %v", got["lead"])
	}
	if ids := e.agentInfos(ctx, "w")["builder"].GetTabIds(); !slices.Equal(ids, []string{"builder:@1"}) {
		t.Errorf("builder's tabs: %v", ids)
	}
	if len(h.startedIDs()) != 0 {
		t.Errorf("the helper started %v; nothing was asked for", h.startedIDs())
	}
}

// CC-0747, defect 1: a workspace whose team was running when it closed
// starts that team again when it opens, through the same start as Start
// Team, even after portenvd itself restarted. An agent the owner stopped,
// or never started, stays stopped.
func TestAReopenedWorkspaceStartsItsTeamAgain(t *testing.T) {
	e, h, dir := startedTeam(t)
	ctx := context.Background()
	if _, err := e.stopAgent(ctx, "w", "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.startAgent(ctx, "w", "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.stopAgent(ctx, "w", "builder"); err != nil {
		t.Fatal(err)
	}

	// Save, Quit, reopen; portenvd restarted in between (a logout).
	h.restart()
	before := len(h.startedIDs())
	fresh := resumeEngine(h, dir, defaultTeam()...)
	if !fresh.boxStarted(ctx, "w") {
		t.Error("boxStarted says nothing of the team starts")
	}
	fresh.starts.Wait()
	if got := h.startedIDs()[before:]; !slices.Equal(got, []string{"lead"}) {
		t.Fatalf("started again: %v, want [lead]", got)
	}
	got := states(fresh)
	want := map[string]typesv1.AgentState{
		"lead":     typesv1.AgentState_AGENT_STATE_RUNNING,
		"builder":  typesv1.AgentState_AGENT_STATE_STOPPED,
		"reviewer": typesv1.AgentState_AGENT_STATE_STOPPED,
	}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("%s after reopening: %v, want %v", id, got[id], s)
		}
	}
	if ids := fresh.agentInfos(ctx, "w")["lead"].GetTabIds(); len(ids) == 0 {
		t.Error("lead runs again with no tab")
	}
	// No decision was asked or answered on the owner's behalf.
	if d := fresh.box("w").teamStartPendingLocked(); d != nil {
		t.Errorf("reopening asked %q", d.GetSummary())
	}

	// The same portenvd, the box restarted again: the same.
	h.restart()
	before = len(h.startedIDs())
	fresh.boxStarted(ctx, "w")
	fresh.starts.Wait()
	if got := h.startedIDs()[before:]; !slices.Equal(got, []string{"lead"}) {
		t.Errorf("started after the second restart: %v, want [lead]", got)
	}
}

// Starting again never skips an owner approval: a workspace whose board
// has no approved first start (a save from before it, say) starts nothing
// and asks nothing. Its agents show as stopped, with a line saying how to
// start them.
func TestReopeningNeverSkipsTheTeamStartApproval(t *testing.T) {
	_, h, dir := startedTeam(t)
	ctx := context.Background()
	// The box comes back with a board that has no approval on it.
	h.restart()
	h.mu.Lock()
	h.board = nil
	h.mu.Unlock()
	before := len(h.startedIDs())
	fresh := resumeEngine(h, dir, defaultTeam()...)
	fresh.boxStarted(ctx, "w")
	fresh.starts.Wait()
	if got := h.startedIDs()[before:]; len(got) != 0 {
		t.Fatalf("started %v without the team's approved first start", got)
	}
	if d := fresh.box("w").teamStartPendingLocked(); d != nil {
		t.Errorf("reopening asked %q on its own", d.GetSummary())
	}
	for _, id := range []string{"lead", "builder"} {
		info := fresh.agentInfos(ctx, "w")[id]
		if info.GetState() != typesv1.AgentState_AGENT_STATE_STOPPED {
			t.Errorf("%s: %v, want stopped", id, info.GetState())
		}
		if !strings.Contains(info.GetProblem(), "Start Team") {
			t.Errorf("%s's line: %q, want how to start it", id, info.GetProblem())
		}
	}
	// The owner's Start asks, as always.
	if _, err := fresh.startAgent(ctx, "w", "lead"); err == nil {
		t.Error("Start went ahead without the approval")
	}
	if fresh.box("w").teamStartPendingLocked() == nil {
		t.Error("Start didn't ask for the team's first start")
	}
}

// A box with no team, or a team never started, opens without a word to
// the board and without reading the team.
func TestABoxWithNoTeamRunningOpensQuietly(t *testing.T) {
	h, dir := newLiveHelper(), t.TempDir()
	loads := 0
	e := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { loads++; return Team{Agents: defaultTeam()}, nil },
	)
	e.wantedFile = func(name string) string { return filepath.Join(dir, name) }
	if e.boxStarted(context.Background(), "w") {
		t.Error("boxStarted says something of the team runs")
	}
	if loads != 0 || len(h.boardLines()) != 0 || len(h.startedIDs()) != 0 {
		t.Errorf("loads %d, board %d, started %v", loads, len(h.boardLines()), h.startedIDs())
	}
}
