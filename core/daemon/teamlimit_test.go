// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The owner's agent limit (R-0347): Settings › Team › Agents allowed.

func TestAgentsAllowedIsFourUntilTheOwnerSetsIt(t *testing.T) {
	dir := t.TempDir()
	if n := readAgentsAllowed(dir); n != 4 {
		t.Fatalf("no setting: %d, want 4", n)
	}
	for n := 1; n <= 8; n++ {
		if err := writeAgentsAllowed(dir, n); err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if got := readAgentsAllowed(dir); got != n {
			t.Errorf("set %d, read %d", n, got)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, teamSettingsFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("settings file: %v %v", fi, err)
	}
	for _, n := range []int{0, -1, 9, 100} {
		err := writeAgentsAllowed(dir, n)
		if err == nil || !strings.Contains(err.Error(), "1 to 8") {
			t.Errorf("%d: %v, want a refusal naming 1 to 8", n, err)
		}
	}
	if got := readAgentsAllowed(dir); got != 8 {
		t.Errorf("a refused value changed the setting: %d", got)
	}
	// A file portenvd can't use falls back to the default, never above it.
	for _, bad := range []string{"{", `{"agents_allowed": 12}`, `{"agents_allowed": 0}`} {
		if err := os.WriteFile(filepath.Join(dir, teamSettingsFile), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readAgentsAllowed(dir); got != 4 {
			t.Errorf("%q: %d, want 4", bad, got)
		}
	}
}

// The setting reaches LoadTeam through the team source, and the team's
// own line follows it.
func TestLoadTeamUsesTheOwnersLimit(t *testing.T) {
	src := teamDirs(t)
	for _, id := range []string{"a1", "a2", "a3"} {
		writeAgent(t, src, id, simpleAgent(id))
	}
	src.Limits = TeamLimits{MaxAgents: 6}
	team, err := LoadTeam(src)
	if err != nil {
		t.Fatal(err)
	}
	if team.Limits.MaxAgents != 6 {
		t.Errorf("team limits %+v, want 6 agents", team.Limits)
	}
	for _, p := range team.Problems {
		if strings.Contains(p, "limit") {
			t.Errorf("under the raised limit, still %q", p)
		}
	}
}

func ownerCtx() context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: callerInfo{who: ownerCaller()}})
}

func limitServer(t *testing.T, e *teamEngine) *Server {
	t.Helper()
	s := &Server{env: &local.Env{Dir: t.TempDir()}}
	s.teamOnce.Do(func() {})
	s.team = e
	return s
}

func TestSetAgentsAllowedRPC(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	s := limitServer(t, e)
	ctx := ownerCtx()
	r, err := s.GetTeamSettings(ctx, &daemonv1.GetTeamSettingsRequest{})
	if err != nil || r.GetAgentsAllowed() != 4 || r.GetDefaultAgentsAllowed() != 4 || r.GetMaxAgentsAllowed() != 8 {
		t.Fatalf("get: %v, %v", r, err)
	}
	set, err := s.SetAgentsAllowed(ctx, &daemonv1.SetAgentsAllowedRequest{AgentsAllowed: 6})
	if err != nil || set.GetSettings().GetAgentsAllowed() != 6 {
		t.Fatalf("set 6: %v, %v", set, err)
	}
	if n := readAgentsAllowed(s.env.Dir); n != 6 {
		t.Errorf("stored %d", n)
	}
	for _, n := range []int32{0, 9} {
		_, err := s.SetAgentsAllowed(ctx, &daemonv1.SetAgentsAllowedRequest{AgentsAllowed: n})
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "1 to 8") {
			t.Errorf("%d: %v", n, err)
		}
	}
	// Only the owner's app sets it: a call portenvd can't place, or an
	// agent's, is refused.
	if _, err := s.SetAgentsAllowed(context.Background(), &daemonv1.SetAgentsAllowedRequest{AgentsAllowed: 8}); err == nil {
		t.Error("a call with no caller set the limit")
	}
	agent := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: callerInfo{who: caller{kind: callerAgent, id: "builder", name: "Builder"}}})
	if _, err := s.SetAgentsAllowed(agent, &daemonv1.SetAgentsAllowedRequest{AgentsAllowed: 8}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("an agent's call: %v", err)
	}
	if n := readAgentsAllowed(s.env.Dir); n != 6 {
		t.Errorf("after refusals, stored %d", n)
	}
}

// fiveAgents is a team of five, each approved and valid.
func fiveAgents() []TeamAgent {
	var team []TeamAgent
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5"} {
		a := testAgent(id, grant(typesv1.GrantMode_GRANT_MODE_ASK, 2, nil, true))
		a.ApprovedContent = "approved"
		team = append(team, a)
	}
	return team
}

// limitEngine is a test engine whose team carries the limit in allowed.
func limitEngine(t *testing.T, allowed *atomic.Int32, agents ...TeamAgent) (*teamEngine, *fakeTeamHelper) {
	t.Helper()
	e, h, _ := newTestEngine(t, agents...)
	e.loadTeam = func(context.Context, string) (Team, error) {
		return Team{Agents: agents, Limits: TeamLimits{MaxAgents: int(allowed.Load())}}, nil
	}
	return e, h
}

func TestStartingBeyondTheLimitIsRefusedUntilItsRaised(t *testing.T) {
	var allowed atomic.Int32
	allowed.Store(4)
	e, h := limitEngine(t, &allowed, fiveAgents()...)
	ctx := context.Background()
	approveFirstStart(t, e)
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		if _, err := e.startAgent(ctx, "w", id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	_, err := e.startAgent(ctx, "w", "a5")
	want := "A5 can't start: 4 agents are running in this workspace, and you allow 4. Stop one, or raise Agents allowed in Portenv's Settings › Team."
	if err == nil || err.Error() != want {
		t.Fatalf("fifth start: %v\nwant %s", err, want)
	}
	if len(h.startedIDs()) != 4 {
		t.Errorf("the helper started %v", h.startedIDs())
	}
	// A stopped agent frees its place.
	if _, err := e.stopAgent(ctx, "w", "a4"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.startAgent(ctx, "w", "a5"); err != nil {
		t.Fatalf("after a stop: %v", err)
	}
	// Raising lets the next one start.
	allowed.Store(5)
	if _, err := e.startAgent(ctx, "w", "a4"); err != nil {
		t.Fatalf("after the raise: %v", err)
	}
	if got := h.startedIDs(); len(got) != 6 {
		t.Errorf("started %v", got)
	}
}

// Lowering the limit stops nothing: running agents keep running, the
// column says so, and no more start until fewer than the limit run.
func TestLoweringTheLimitStopsNothing(t *testing.T) {
	var allowed atomic.Int32
	allowed.Store(5)
	e, h := limitEngine(t, &allowed, fiveAgents()...)
	ctx := context.Background()
	approveFirstStart(t, e)
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5"} {
		if _, err := e.startAgent(ctx, "w", id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	allowed.Store(3)
	lim := e.teamLimit(ctx, "w")
	if lim.GetAgentsRunning() != 5 || lim.GetAgentsAllowed() != 3 {
		t.Fatalf("limit %v", lim)
	}
	if want := "5 agents are running, and you now allow 3. They keep running; no more start until fewer than 3 run."; lim.GetLine() != want {
		t.Errorf("line %q\nwant %q", lim.GetLine(), want)
	}
	if len(h.stopped) != 0 {
		t.Errorf("lowering stopped %v", h.stopped)
	}
	for id, info := range e.agentInfos(ctx, "w") {
		if info.GetState() != typesv1.AgentState_AGENT_STATE_RUNNING {
			t.Errorf("%s: %v", id, info.GetState())
		}
	}
	if _, err := e.stopAgent(ctx, "w", "a5"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.startAgent(ctx, "w", "a5"); err == nil || !strings.Contains(err.Error(), "you allow 3") {
		t.Errorf("a start with 4 running and 3 allowed: %v", err)
	}
}

// The team's first start names no more agents than the limit; Start on
// one more says why at once, before the owner is asked anything.
func TestTheFirstStartNamesNoMoreThanTheLimit(t *testing.T) {
	var allowed atomic.Int32
	allowed.Store(4)
	e, h := limitEngine(t, &allowed, fiveAgents()...)
	ctx := context.Background()
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		_, _ = e.startAgent(ctx, "w", id)
	}
	_, err := e.startAgent(ctx, "w", "a5")
	if err == nil || !strings.Contains(err.Error(), "A5 can't start") {
		t.Fatalf("a fifth Start: %v", err)
	}
	d := e.box("w").teamStartPendingLocked()
	if d == nil || len(d.GetStartAgentIds()) != 4 || slices.Contains(d.GetStartAgentIds(), "a5") {
		t.Fatalf("team_start names %v", d.GetStartAgentIds())
	}
	// Lowered before Approve: the first ones start, the rest say why.
	allowed.Store(2)
	if _, err := e.answer(ctx, "w", d.GetId(), true, ""); err != nil {
		t.Fatal(err)
	}
	e.starts.Wait()
	if got := h.startedIDs(); !slices.Equal(got, []string{"a1", "a2"}) {
		t.Errorf("started %v, want [a1 a2]", got)
	}
	infos := e.agentInfos(ctx, "w")
	for _, id := range []string{"a3", "a4"} {
		if infos[id].GetState() != typesv1.AgentState_AGENT_STATE_STOPPED || !strings.Contains(infos[id].GetProblem(), "you allow 2") {
			t.Errorf("%s: %v %q", id, infos[id].GetState(), infos[id].GetProblem())
		}
	}
}

// Approving one more file than the limit is refused with the same
// advice; an agent approved before (a changed file) can be approved again.
func TestApprovingBeyondTheLimitIsRefused(t *testing.T) {
	team := Team{Agents: fiveAgents(), Limits: TeamLimits{MaxAgents: 4}}
	team.Agents[4].Approved, team.Agents[4].ApprovedContent = false, ""
	err := team.approveLimit("a5")
	want := "A5 can't be approved: 4 agents are approved in this workspace, and you allow 4. Raise Agents allowed in Portenv's Settings › Team, or remove an agent file"
	if err == nil || err.Error() != want {
		t.Fatalf("approve a5: %v\nwant %s", err, want)
	}
	team.Agents[0].Approved = false // a1's file changed since its approval
	if err := team.approveLimit("a1"); err != nil {
		t.Errorf("re-approving a1: %v", err)
	}
	team.Limits.MaxAgents = 5
	if err := team.approveLimit("a5"); err != nil {
		t.Errorf("after the raise: %v", err)
	}
}

func TestTheTeamLimitLine(t *testing.T) {
	cases := []struct {
		inTeam, running, allowed int
		line                     string
		raise                    int32
	}{
		{2, 0, 4, "", 0},
		{3, 1, 4, "3 of 4 agents allowed in this workspace.", 0},
		{4, 2, 4, "4 of 4 agents allowed: the team is at your limit.", 0},
		{1, 0, 1, "1 of 1 agent allowed: the team is at your limit.", 0},
		{6, 0, 4, "This team has 6 agents, and you allow 4 in a workspace: only 4 can run at once.", 6},
		{6, 6, 4, "6 agents are running, and you now allow 4. They keep running; no more start until fewer than 4 run.", 6},
		{9, 0, 4, "This team has 9 agents, and you allow 4 in a workspace: only 4 can run at once.", 8},
		{9, 0, 8, "This team has 9 agents; Portenv runs at most 8 in a workspace.", 0},
	}
	for _, c := range cases {
		got := teamLimitInfo(c.inTeam, c.running, c.allowed)
		if got.GetLine() != c.line || got.GetRaiseTo() != c.raise || int(got.GetAgentsAllowed()) != c.allowed ||
			int(got.GetAgentsInTeam()) != c.inTeam || int(got.GetAgentsRunning()) != c.running {
			t.Errorf("%d in team, %d running, %d allowed: %v\nwant %q, raise %d", c.inTeam, c.running, c.allowed, got, c.line, c.raise)
		}
	}
}

// ListAgents and WatchTeam carry the limit; WatchTeam sends it again when
// the owner changes the setting.
func TestWatchTeamSendsTheLimit(t *testing.T) {
	var allowed atomic.Int32
	allowed.Store(4)
	e, _ := limitEngine(t, &allowed, fiveAgents()...)
	s := limitServer(t, e)
	ctx, cancel := context.WithCancel(ownerCtx())
	defer cancel()
	// No helper to pump calls from: the pump's loop ends at once.
	bg, stopBg := context.WithCancel(context.Background())
	stopBg()
	s.bgCtx = bg
	list, err := s.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: "w"})
	if err != nil || list.GetTeamLimit().GetRaiseTo() != 5 {
		t.Fatalf("list: %v, %v", list.GetTeamLimit(), err)
	}
	st := &watchStream{ctx: ctx, got: make(chan *daemonv1.WatchTeamResponse, 64)}
	go func() { _ = s.WatchTeam(&daemonv1.WatchTeamRequest{Name: "w"}, st) }()
	next := func() *daemonv1.TeamLimit {
		t.Helper()
		for r := range st.got {
			if l := r.GetTeamLimit(); l != nil {
				return l
			}
		}
		return nil
	}
	if l := next(); l.GetAgentsAllowed() != 4 || l.GetRaiseTo() != 5 {
		t.Fatalf("first limit %v", l)
	}
	allowed.Store(5)
	if _, err := s.SetAgentsAllowed(ctx, &daemonv1.SetAgentsAllowedRequest{AgentsAllowed: 5}); err != nil {
		t.Fatal(err)
	}
	if l := next(); l.GetAgentsAllowed() != 5 || l.GetRaiseTo() != 0 || !strings.Contains(l.GetLine(), "5 of 5") {
		t.Fatalf("after the raise %v", l)
	}
}

// watchStream is WatchTeam's stream, kept in a channel.
type watchStream struct {
	grpc.ServerStream
	ctx context.Context
	got chan *daemonv1.WatchTeamResponse
}

func (w *watchStream) Context() context.Context { return w.ctx }

func (w *watchStream) Send(r *daemonv1.WatchTeamResponse) error {
	select {
	case w.got <- r:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}
