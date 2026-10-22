// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The owner's portenv commands (ADR 0027 §5, S3): each is one portenvd
// call, the same rules as the app's, and no agent reaches any of them.

// TestTheOwnersWakeTypesTheUnreadBatchsLine: portenv wake types the fixed
// line for the agent's unread batch at once, naming the newest sender and
// never the text, and counts as that batch's line.
func TestTheOwnersWakeTypesTheUnreadBatchsLine(t *testing.T) {
	e, h, _ := newWakeEngine(t, defaultTeam(), "lead")
	ctx := context.Background()
	mustCall(t, e, msgCall("builder", "lead", "please build the parser"))
	if err := e.ownerWake(ctx, "w", "lead"); err != nil {
		t.Fatalf("wake: %v", err)
	}
	got := h.wakes()
	if len(got) != 1 || got[0].GetAgentId() != "lead" || got[0].GetFrom() != "builder" || got[0].GetCount() != 1 {
		t.Fatalf("wake-ups = %v, want one for lead from builder", got)
	}
	// The batch's line is typed: the tick doesn't type it again.
	e.tickWakes(ctx)
	if n := len(h.wakes()); n != 1 {
		t.Errorf("%d wake-ups after the owner's, want 1", n)
	}
}

// TestTheOwnersWakeNeverTypesIntoABusyAgent (R-0359): the helper's checks
// apply; a busy agent is refused with a plain line and nothing is typed.
func TestTheOwnersWakeNeverTypesIntoABusyAgent(t *testing.T) {
	e, h, _ := newWakeEngine(t, defaultTeam(), "lead")
	ctx := context.Background()
	h.busy = true
	mustCall(t, e, msgCall("builder", "lead", "hi"))
	err := e.ownerWake(ctx, "w", "lead")
	if err == nil || err.Error() != "Lead is busy, so Portenv won't type into it. It'll see your message when it's next idle." {
		t.Fatalf("wake of a busy agent: %v", err)
	}
	if n := len(h.wakes()); n != 0 {
		t.Fatalf("a busy agent was typed into: %d", n)
	}
	// Still unread: the tick wakes it once it's idle.
	h.busy = false
	e.tickWakes(ctx)
	if n := len(h.wakes()); n != 1 {
		t.Errorf("after idle: %d wake-ups, want 1", n)
	}
}

// TestTheOwnersWakeRefusals: an unknown agent, a stopped one, one with
// nothing unread and one whose runner can't be woken are each refused with
// a plain line, and the helper is never asked.
func TestTheOwnersWakeRefusals(t *testing.T) {
	team := defaultTeam()
	team[2].Runner, team[2].RunnerLine = []string{"/usr/local/bin/aider"}, "aider"
	team[2].Spec.Runner = team[2].Runner
	e, h, _ := newWakeEngine(t, team, "lead", "reviewer")
	ctx := context.Background()
	mustCall(t, e, msgCall("lead", "reviewer", "look at this"))
	cases := map[string]string{
		"ghost":    `this workspace has no agent "ghost"`,
		"builder":  "Builder isn't running. Start it with portenv team start builder.",
		"lead":     "Lead has nothing new in its inbox. Send it a message with portenv msg lead TEXT.",
		"reviewer": "Reviewer's runner can't be woken: Portenv types only into a runner it can tell is idle.",
	}
	for id, want := range cases {
		err := e.ownerWake(ctx, "w", id)
		if err == nil || err.Error() != want {
			t.Errorf("wake %s: %v, want %q", id, err, want)
		}
	}
	if h.tries != 0 {
		t.Errorf("the helper was asked %d times", h.tries)
	}
}

// TestListDecisionsIsPendingOnlyUnlessAll: decision list shows what waits
// for the owner, oldest first; --all shows answered ones too.
func TestListDecisionsIsPendingOnlyUnlessAll(t *testing.T) {
	e, _, clock := newWakeEngine(t, defaultTeam(), "lead", "builder")
	ctx := context.Background()
	d1 := mustCall(t, e, &agentv1.TeamCallsResponse{AgentId: "builder", Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: "May I add a dependency?"}}}).GetDecision()
	clock.add(time.Second)
	d2 := mustCall(t, e, &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: "Which first?", Choices: []string{"Parser", "Lexer"}}}}).GetDecision()
	if _, err := e.answer(ctx, "w", d1.GetId(), true, ""); err != nil {
		t.Fatal(err)
	}
	pending, err := e.listDecisions(ctx, "w", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].GetId() != d2.GetId() || !slices.Equal(pending[0].GetChoices(), []string{"Parser", "Lexer"}) {
		t.Fatalf("pending = %v, want only %s with its choices", pending, d2.GetId())
	}
	all, err := e.listDecisions(ctx, "w", true)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range all {
		if d.GetKind() == typesv1.DecisionKind_DECISION_KIND_ASK_OWNER {
			ids = append(ids, d.GetId())
		}
	}
	if !slices.Equal(ids, []string{d1.GetId(), d2.GetId()}) {
		t.Errorf("all = %v, want %s then %s", ids, d1.GetId(), d2.GetId())
	}
}

// runbookHelper answers GetPathInfo for the runbook's two places.
type runbookHelper struct {
	*fakeTeamHelper
	mu     sync.Mutex
	exists map[string]bool
	asked  []string
}

func (r *runbookHelper) GetPathInfo(_ context.Context, in *agentv1.GetPathInfoRequest, _ ...grpc.CallOption) (*agentv1.GetPathInfoResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, in.GetPath())
	return &agentv1.GetPathInfoResponse{Exists: r.exists[in.GetPath()]}, nil
}

func runbookEngine(exists ...string) (*teamEngine, *runbookHelper) {
	h := &runbookHelper{fakeTeamHelper: &fakeTeamHelper{}, exists: map[string]bool{}}
	for _, p := range exists {
		h.exists[p] = true
	}
	e := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { return Team{Agents: defaultTeam()}, nil },
	)
	return e, h
}

// TestDeployRefusesWithoutARunbook (R-0359): no DEPLOY.md and no
// .portenv/runbook.md means ADR 0027's line; nothing is written or run.
func TestDeployRefusesWithoutARunbook(t *testing.T) {
	e, h := runbookEngine()
	_, err := e.deploy(context.Background(), "w", "staging")
	want := "This project has no deploy runbook, so Portenv won't guess how to deploy. Add DEPLOY.md (see portenv guide), then try again."
	if err == nil || err.Error() != want {
		t.Fatalf("deploy with no runbook: %v, want %q", err, want)
	}
	if !slices.Equal(h.asked, []string{"/home/work/w/.portenv/runbook.md", "/home/work/w/DEPLOY.md"}) {
		t.Errorf("looked at %v", h.asked)
	}
	if len(h.board) != 0 || len(h.started) != 0 || len(h.panes) != 0 {
		t.Errorf("a refused deploy changed something: board %v", h.board)
	}
}

// TestDeployWithARunbookSaysItIsntAvailableYet: until ADR 0026's
// run-with-secret is built, a runbook doesn't make a deploy run; the line
// says so plainly, and no decision is made that nothing would carry out.
func TestDeployWithARunbookSaysItIsntAvailableYet(t *testing.T) {
	for _, p := range []string{"/home/work/w/DEPLOY.md", "/home/work/w/.portenv/runbook.md"} {
		e, h := runbookEngine(p)
		_, err := e.deploy(context.Background(), "w", "production")
		if status.Code(err) != codes.Unimplemented || !strings.Contains(err.Error(), "Portenv can't run deploys yet") || !strings.Contains(err.Error(), "Nothing was run") {
			t.Errorf("%s: %v, want the not-yet line", p, err)
		}
		if len(h.board) != 0 || len(h.started) != 0 {
			t.Errorf("%s: a deploy that can't run made a decision or started something", p)
		}
	}
}

// TestDeployRefusesABadTargetBeforeLooking: a target is a runbook name,
// never a path or a flag; the helper isn't asked.
func TestDeployRefusesABadTargetBeforeLooking(t *testing.T) {
	for _, target := range []string{"", "../prod", "-x", "Prod", "a b", strings.Repeat("a", 41)} {
		e, h := runbookEngine("/home/work/w/DEPLOY.md")
		if _, err := e.deploy(context.Background(), "w", target); err == nil {
			t.Errorf("deploy %q wasn't refused", target)
		}
		if len(h.asked) != 0 {
			t.Errorf("deploy %q asked the helper", target)
		}
	}
}

// TestAnAgentCallerCantReachTheOwnersCalls (ADR 0027 §5, R-0359): every
// DaemonService call but TabTerminal is the owner's. A caller identified
// as an agent is refused before the call runs, with no workspace helper
// contacted, whatever the request says.
func TestAnAgentCallerCantReachTheOwnersCalls(t *testing.T) {
	t.Setenv("PORTENV_KEYS", "file")
	h := &fakeTabHelper{boxID: "box-a"}
	w := servingTabTerminals(t, agentGrok, h, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calls := map[string]func() error{
		"SendMessage": func() error {
			_, err := w.c.SendMessage(ctx, &daemonv1.SendMessageRequest{Name: "acme-api", To: "lead", Text: "hi"})
			return err
		},
		"WakeAgent": func() error {
			_, err := w.c.WakeAgent(ctx, &daemonv1.WakeAgentRequest{Name: "acme-api", AgentId: "lead"})
			return err
		},
		"NewTab": func() error {
			_, err := w.c.NewTab(ctx, &daemonv1.NewTabRequest{Box: "acme-api", Name: "x"})
			return err
		},
		"StartAgent": func() error {
			_, err := w.c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: "acme-api", AgentId: "lead"})
			return err
		},
		"StopAgent": func() error {
			_, err := w.c.StopAgent(ctx, &daemonv1.StopAgentRequest{Name: "acme-api", AgentId: "lead"})
			return err
		},
		"AnswerDecision": func() error {
			_, err := w.c.AnswerDecision(ctx, &daemonv1.AnswerDecisionRequest{Name: "acme-api", DecisionId: "x", Approve: true})
			return err
		},
		"ListDecisions": func() error {
			_, err := w.c.ListDecisions(ctx, &daemonv1.ListDecisionsRequest{Name: "acme-api"})
			return err
		},
		"Deploy": func() error {
			_, err := w.c.Deploy(ctx, &daemonv1.DeployRequest{Name: "acme-api", Target: "production"})
			return err
		},
		"ListAgents": func() error {
			_, err := w.c.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: "acme-api"})
			return err
		},
		"OpenBox": func() error {
			_, err := w.c.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: "acme-api"})
			return err
		},
		"WatchTeam": func() error {
			st, err := w.c.WatchTeam(ctx, &daemonv1.WatchTeamRequest{Name: "acme-api"})
			if err != nil {
				return err
			}
			_, err = st.Recv()
			return err
		},
	}
	for name, call := range calls {
		err := call()
		if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "Only this Mac's owner can do that; an agent can't." {
			t.Errorf("%s from an agent: %v, want PermissionDenied", name, err)
		}
	}
	if n := h.contacted(); n != 0 {
		t.Errorf("refused calls reached the helper %d times", n)
	}
}

// TestTheOwnersCallsStillRunForTheOwner: the same guard lets the owner
// through (the call fails later, on its own terms, not as refused).
func TestTheOwnersCallsStillRunForTheOwner(t *testing.T) {
	t.Setenv("PORTENV_KEYS", "file")
	w := servingTabTerminals(t, ownerCaller(), &fakeTabHelper{boxID: "box-a"}, &fakeTabHelper{boxID: "box-b"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := w.c.Deploy(ctx, &daemonv1.DeployRequest{Name: "acme-api", Target: "-bad"})
	if status.Code(err) == codes.PermissionDenied {
		t.Fatalf("the owner was refused: %v", err)
	}
}

// TestAnAgentsTeamCallsHaveNoOwnerActions: an agent's portenv reaches
// portenvd only through TeamCalls, whose calls are exactly these. None
// answers a decision, wakes, starts or stops an agent, or deploys; a new
// one must be added here on purpose.
func TestAnAgentsTeamCallsHaveNoOwnerActions(t *testing.T) {
	want := []string{
		// fetch is the owner's (R-0392), taken and refused as push is below.
		"ask_owner", "close", "fetch", "focus", "handoff", "inbox", "msg",
		// permission records the caller's own prompt (R-0412 item 18):
		// it answers nothing; the owner answers in the terminal.
		"permission",
		"propose", "propose_routine",
		// push and push_list are the owner's (R-0391): the helper takes them
		// only from work's socket (SO_PEERCRED) and refuses them on an
		// agent's, and handleCall refuses them from an agent too.
		"push", "push_list",
		"request_package", "set_status", "share", "split", "tab", "team_status", "whoami",
	}
	var got []string
	ops := (&agentv1.TeamCallsResponse{}).ProtoReflect().Descriptor().Oneofs().ByName("op")
	for i := range ops.Fields().Len() {
		got = append(got, string(ops.Fields().Get(i).Name()))
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("an agent's calls are %v, want %v", got, want)
	}
	for _, f := range got {
		for _, owner := range []string{"answer", "decision", "wake", "start", "stop", "deploy", "approve"} {
			if strings.Contains(f, owner) {
				t.Errorf("an agent's call %q looks like the owner's %q", f, owner)
			}
		}
	}
}
