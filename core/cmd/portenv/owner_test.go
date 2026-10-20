// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/daemon"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The owner's portenv commands (ADR 0027 §5, S3), each one portenvd call.

// fakeDaemon is portenvd for the owner's commands: it records every call
// and answers from its fields.
type fakeDaemon struct {
	daemonv1.UnimplementedDaemonServiceServer

	mu        sync.Mutex
	calls     []string
	boxes     []*daemonv1.BoxStatus
	agents    []*daemonv1.AgentInfo
	decisions []*typesv1.Decision
	// refuse maps a call's name (and "StartAgent lead") to its error.
	refuse map[string]error
	sent   []*daemonv1.SendMessageRequest
	woken  []*daemonv1.WakeAgentRequest
	tabs   []*daemonv1.NewTabRequest
	starts []*daemonv1.StartAgentRequest
	stops  []*daemonv1.StopAgentRequest
	lists  []*daemonv1.ListDecisionsRequest
	deploy []*daemonv1.DeployRequest
}

func (f *fakeDaemon) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if err := f.refuse[call]; err != nil {
		return err
	}
	return nil
}

func (f *fakeDaemon) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeDaemon) ListBoxes(context.Context, *daemonv1.ListBoxesRequest) (*daemonv1.ListBoxesResponse, error) {
	if err := f.record("ListBoxes"); err != nil {
		return nil, err
	}
	return &daemonv1.ListBoxesResponse{Boxes: f.boxes}, nil
}

func (f *fakeDaemon) SendMessage(_ context.Context, in *daemonv1.SendMessageRequest) (*daemonv1.SendMessageResponse, error) {
	if err := f.record("SendMessage"); err != nil {
		return nil, err
	}
	f.sent = append(f.sent, in)
	return &daemonv1.SendMessageResponse{Message: &typesv1.BoardMessage{Id: "01JMSG", From: "owner", To: in.GetTo(), ThreadId: "01JMSG", Text: in.GetText()}}, nil
}

func (f *fakeDaemon) WakeAgent(_ context.Context, in *daemonv1.WakeAgentRequest) (*daemonv1.WakeAgentResponse, error) {
	if err := f.record("WakeAgent"); err != nil {
		return nil, err
	}
	f.woken = append(f.woken, in)
	return &daemonv1.WakeAgentResponse{}, nil
}

func (f *fakeDaemon) NewTab(_ context.Context, in *daemonv1.NewTabRequest) (*daemonv1.NewTabResponse, error) {
	if err := f.record("NewTab"); err != nil {
		return nil, err
	}
	f.tabs = append(f.tabs, in)
	return &daemonv1.NewTabResponse{Tab: &typesv1.Tab{Id: "@7", Name: in.GetName()}}, nil
}

func (f *fakeDaemon) ListAgents(context.Context, *daemonv1.ListAgentsRequest) (*daemonv1.ListAgentsResponse, error) {
	if err := f.record("ListAgents"); err != nil {
		return nil, err
	}
	return &daemonv1.ListAgentsResponse{Agents: f.agents}, nil
}

func (f *fakeDaemon) StartAgent(_ context.Context, in *daemonv1.StartAgentRequest) (*daemonv1.StartAgentResponse, error) {
	if err := f.record("StartAgent"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.starts = append(f.starts, in)
	err := f.refuse["StartAgent "+in.GetAgentId()]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &daemonv1.StartAgentResponse{Agent: &daemonv1.AgentInfo{Id: in.GetAgentId(), State: typesv1.AgentState_AGENT_STATE_RUNNING}}, nil
}

func (f *fakeDaemon) StopAgent(_ context.Context, in *daemonv1.StopAgentRequest) (*daemonv1.StopAgentResponse, error) {
	if err := f.record("StopAgent"); err != nil {
		return nil, err
	}
	f.stops = append(f.stops, in)
	return &daemonv1.StopAgentResponse{Agent: &daemonv1.AgentInfo{Id: in.GetAgentId(), State: typesv1.AgentState_AGENT_STATE_STOPPED}}, nil
}

func (f *fakeDaemon) AnswerDecision(context.Context, *daemonv1.AnswerDecisionRequest) (*daemonv1.AnswerDecisionResponse, error) {
	if err := f.record("AnswerDecision"); err != nil {
		return nil, err
	}
	return &daemonv1.AnswerDecisionResponse{}, nil
}

func (f *fakeDaemon) ListDecisions(_ context.Context, in *daemonv1.ListDecisionsRequest) (*daemonv1.ListDecisionsResponse, error) {
	if err := f.record("ListDecisions"); err != nil {
		return nil, err
	}
	f.lists = append(f.lists, in)
	var out []*typesv1.Decision
	for _, d := range f.decisions {
		if in.GetAll() || d.GetState() == typesv1.DecisionState_DECISION_STATE_PENDING {
			out = append(out, d)
		}
	}
	return &daemonv1.ListDecisionsResponse{Decisions: out}, nil
}

func (f *fakeDaemon) Deploy(_ context.Context, in *daemonv1.DeployRequest) (*daemonv1.DeployResponse, error) {
	if err := f.record("Deploy"); err != nil {
		return nil, err
	}
	f.deploy = append(f.deploy, in)
	return &daemonv1.DeployResponse{}, nil
}

// servingDaemon serves f as portenvd in a fresh Portenv folder, which
// PORTENV_HOME names, outside any workspace.
func servingDaemon(t *testing.T, f *fakeDaemon) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pd") // short: a Unix socket path has 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	lis, err := net.Listen("unix", filepath.Join(dir, daemon.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	daemonv1.RegisterDaemonServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PORTENV_HOME", dir)
	t.Setenv("PORTENV_SOCKET", "")
	t.Setenv(agent.BoxIDEnv, "")
	if insideWorkspace() {
		t.Skip("this machine has the box agent's folder; it is a workspace")
	}
	return dir
}

// oneWorkspace is a daemon with one open workspace, acme.
func oneWorkspace() *fakeDaemon {
	return &fakeDaemon{boxes: []*daemonv1.BoxStatus{
		{Box: &typesv1.Box{Name: "acme"}, State: typesv1.BoxState_BOX_STATE_RUNNING},
		{Box: &typesv1.Box{Name: "old"}, State: typesv1.BoxState_BOX_STATE_STOPPED},
	}}
}

// runBoth runs the CLI with its stdout and stderr caught.
func runBoth(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedOut := os.Stdout
	os.Stdout = outW
	gotOut := make(chan string)
	go func() { b, _ := io.ReadAll(outR); gotOut <- string(b) }()
	code, stderr := runCaught(t, args...)
	os.Stdout = savedOut
	_ = outW.Close()
	return code, <-gotOut, stderr
}

func TestOwnerMsgSendsAsTheOwner(t *testing.T) {
	f := oneWorkspace()
	servingDaemon(t, f)
	code, out, errOut := runBoth(t, "msg", "lead", "please review the parser", "--thread", "01JTHREAD")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(f.sent) != 1 || f.sent[0].GetName() != "acme" || f.sent[0].GetTo() != "lead" ||
		f.sent[0].GetText() != "please review the parser" || f.sent[0].GetThreadId() != "01JTHREAD" {
		t.Fatalf("sent %v", f.sent)
	}
	if !strings.Contains(out, "Sent to lead") {
		t.Errorf("stdout %q", out)
	}
	// The owner's message has no type of an agent's: --type is refused.
	if code, _, _ := runBoth(t, "msg", "lead", "hi", "--type", "question"); code != 2 {
		t.Errorf("--type: exit %d, want 2", code)
	}
	if code, _, _ := runBoth(t, "msg", "lead"); code != 2 {
		t.Errorf("no text: exit %d, want 2", code)
	}
	if len(f.sent) != 1 {
		t.Errorf("a usage mistake sent something: %v", f.sent)
	}
}

// The workspace is --workspace, or the one open workspace; with several
// open and none named, nothing is sent.
func TestOwnerCommandsPickTheWorkspace(t *testing.T) {
	f := oneWorkspace()
	f.boxes = append(f.boxes, &daemonv1.BoxStatus{Box: &typesv1.Box{Name: "web"}, State: typesv1.BoxState_BOX_STATE_RUNNING})
	servingDaemon(t, f)
	code, _, errOut := runBoth(t, "wake", "lead")
	if code != 2 || !strings.Contains(errOut, "--workspace") || !strings.Contains(errOut, "acme, web") {
		t.Errorf("two open: exit %d, stderr %q", code, errOut)
	}
	if len(f.woken) != 0 {
		t.Fatalf("woke with no workspace chosen: %v", f.woken)
	}
	if code, _, errOut := runBoth(t, "wake", "lead", "--workspace", "web"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(f.woken) != 1 || f.woken[0].GetName() != "web" {
		t.Errorf("woken %v, want web's lead", f.woken)
	}
	f.boxes = nil
	if code, _, errOut := runBoth(t, "wake", "lead"); code != 1 || !strings.Contains(errOut, "No workspace is open") {
		t.Errorf("none open: exit %d, stderr %q", code, errOut)
	}
}

// R-0359: wake never wakes a busy agent; portenvd's line is shown as it
// is.
func TestOwnerWakeShowsPortenvdsRefusal(t *testing.T) {
	f := oneWorkspace()
	busy := "Lead is busy, so Portenv won't type into it. It'll see your message when it's next idle."
	f.refuse = map[string]error{"WakeAgent": status.Error(codes.FailedPrecondition, busy)}
	servingDaemon(t, f)
	code, _, errOut := runBoth(t, "wake", "lead")
	if code != 1 || errOut != "portenv: "+busy+"\n" {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	f.refuse = nil
	code, out, errOut := runBoth(t, "wake", "lead")
	if code != 0 || !strings.Contains(out, "asked lead to read its inbox") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestOwnerTabNew(t *testing.T) {
	f := oneWorkspace()
	servingDaemon(t, f)
	code, out, errOut := runBoth(t, "tab", "new", "--name", "logs")
	if code != 0 || len(f.tabs) != 1 || f.tabs[0].GetBox() != "acme" || f.tabs[0].GetName() != "logs" {
		t.Fatalf("exit %d, stderr %q, tabs %v", code, errOut, f.tabs)
	}
	if !strings.Contains(out, "@7") {
		t.Errorf("stdout %q", out)
	}
	// The app's New Tab opens a shell; the owner's tab new runs nothing
	// the app can't.
	code, _, errOut = runBoth(t, "tab", "new", "--name", "x", "--", "rm", "-rf", "/")
	if code != 2 || !strings.Contains(errOut, "type the command in it") {
		t.Errorf("with a command: exit %d, stderr %q", code, errOut)
	}
	if code, _, _ := runBoth(t, "tab", "new"); code != 2 {
		t.Errorf("no --name: exit %d", code)
	}
	if len(f.tabs) != 1 {
		t.Errorf("refused tab new opened a tab: %v", f.tabs)
	}
}

// team start goes through StartAgent: the team's first start waits on the
// owner's approval in the app, and the CLI never answers it.
func TestOwnerTeamStartWaitsForTheAppsApproval(t *testing.T) {
	f := oneWorkspace()
	f.agents = []*daemonv1.AgentInfo{
		{Id: "lead", Approved: true, State: typesv1.AgentState_AGENT_STATE_STOPPED},
		{Id: "builder", Approved: true, State: typesv1.AgentState_AGENT_STATE_STOPPED},
		{Id: "draft", Approved: false, State: typesv1.AgentState_AGENT_STATE_STOPPED},
		{Id: "far", Approved: true, Remote: true},
		{Id: "busy", Approved: true, State: typesv1.AgentState_AGENT_STATE_RUNNING},
	}
	f.refuse = map[string]error{
		"StartAgent lead":    status.Error(codes.FailedPrecondition, "approve the team's first start in Portenv first"),
		"StartAgent builder": status.Error(codes.FailedPrecondition, "approve the team's first start in Portenv first"),
	}
	servingDaemon(t, f)
	code, out, errOut := runBoth(t, "team", "start")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var ids []string
	for _, s := range f.starts {
		ids = append(ids, s.GetAgentId())
	}
	if !slices.Equal(ids, []string{"lead", "builder"}) {
		t.Errorf("started %v, want the approved, local, stopped ones", ids)
	}
	if !strings.Contains(out, "Waiting for your approval in Portenv") {
		t.Errorf("stdout %q", out)
	}
	if slices.Contains(f.called(), "AnswerDecision") {
		t.Error("team start answered a decision")
	}
	// A named agent that fails says why, and the exit says so too.
	f.refuse = map[string]error{"StartAgent draft": status.Error(codes.FailedPrecondition, "approve draft's file first")}
	code, _, errOut = runBoth(t, "team", "start", "draft")
	if code != 1 || !strings.Contains(errOut, "approve draft's file first") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

func TestOwnerTeamStopStopsTheRunningOnes(t *testing.T) {
	f := oneWorkspace()
	f.agents = []*daemonv1.AgentInfo{
		{Id: "lead", State: typesv1.AgentState_AGENT_STATE_RUNNING},
		{Id: "builder", State: typesv1.AgentState_AGENT_STATE_STOPPED},
		{Id: "reviewer", State: typesv1.AgentState_AGENT_STATE_STARTING},
	}
	servingDaemon(t, f)
	if code, _, errOut := runBoth(t, "team", "stop"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var ids []string
	for _, s := range f.stops {
		ids = append(ids, s.GetAgentId())
	}
	if !slices.Equal(ids, []string{"lead", "reviewer"}) {
		t.Errorf("stopped %v", ids)
	}
	f.stops = nil
	if code, _, _ := runBoth(t, "team", "stop", "builder"); code != 0 || len(f.stops) != 1 || f.stops[0].GetAgentId() != "builder" {
		t.Errorf("exit %d, stops %v", code, f.stops)
	}
}

// R-0359: deploy without a runbook is refused with portenvd's line; no
// flag skips an approval, so there are none.
func TestOwnerDeploy(t *testing.T) {
	f := oneWorkspace()
	line := "This project has no deploy runbook, so Portenv won't guess how to deploy. Add DEPLOY.md (see portenv guide), then try again."
	f.refuse = map[string]error{"Deploy": status.Error(codes.FailedPrecondition, line)}
	servingDaemon(t, f)
	code, _, errOut := runBoth(t, "deploy", "staging")
	if code != 1 || errOut != "portenv: "+line+"\n" {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	deploys := func() int {
		n := 0
		for _, c := range f.called() {
			if c == "Deploy" {
				n++
			}
		}
		return n
	}
	if deploys() != 1 {
		t.Errorf("Deploy called %d times, want 1", deploys())
	}
	for _, flag := range []string{"--yes", "--force", "--approve", "--no-confirm"} {
		if code, _, _ := runBoth(t, "deploy", "production", flag); code != 2 {
			t.Errorf("deploy %s: exit %d, want a usage mistake", flag, code)
		}
	}
	if deploys() != 1 {
		t.Errorf("a refused deploy reached portenvd: %d calls", deploys())
	}
	// The target and workspace go to portenvd as typed.
	f.refuse = nil
	if code, _, errOut := runBoth(t, "deploy", "staging"); code != 0 || len(f.deploy) != 1 || f.deploy[0].GetTarget() != "staging" || f.deploy[0].GetName() != "acme" {
		t.Errorf("exit %d, stderr %q, deploy %v", code, errOut, f.deploy)
	}
}

func decisionFixtures() []*typesv1.Decision {
	return []*typesv1.Decision{
		{Id: "01JA", From: "builder", Kind: typesv1.DecisionKind_DECISION_KIND_ASK_OWNER, Summary: "May I add a dependency?", State: typesv1.DecisionState_DECISION_STATE_PENDING},
		{Id: "01JB", From: "lead", Kind: typesv1.DecisionKind_DECISION_KIND_ASK_OWNER, Summary: "Which first?", Choices: []string{"Parser", "Lexer"}, State: typesv1.DecisionState_DECISION_STATE_PENDING},
		{Id: "01JC", From: "lead", Kind: typesv1.DecisionKind_DECISION_KIND_PACKAGE, Summary: "Install jq", State: typesv1.DecisionState_DECISION_STATE_APPROVED},
	}
}

func TestOwnerDecisionList(t *testing.T) {
	f := oneWorkspace()
	f.decisions = decisionFixtures()
	servingDaemon(t, f)
	code, out, errOut := runBoth(t, "decision", "list")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"01JA", "builder", "May I add a dependency?", "01JB", `1 "Parser"`, `2 "Lexer"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "01JC") {
		t.Errorf("an answered decision is listed without --all:\n%s", out)
	}
	code, out, _ = runBoth(t, "decision", "list", "--all", "--json")
	var got []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got) != 3 {
		t.Fatalf("--all --json: exit %d, %q", code, out)
	}
	if got[2]["id"] != "01JC" || got[2]["state"] != "DECISION_STATE_APPROVED" {
		t.Errorf("--json: %v", got[2])
	}
	if code, out, _ := runBoth(t, "decision", "list", "--workspace", "acme"); code != 0 || !strings.Contains(out, "01JA") {
		t.Errorf("exit %d, %q", code, out)
	}
}

// Only the app answers a decision, in front of the person (ADR 0022 §6):
// decision answer checks the request and points to it in Portenv, and
// never calls AnswerDecision.
func TestOwnerDecisionAnswerPointsToTheApp(t *testing.T) {
	f := oneWorkspace()
	f.decisions = decisionFixtures()
	servingDaemon(t, f)
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"decision", "answer", "01JA", "approve"}, 1, "Answer it in Portenv"},
		{[]string{"decision", "answer", "01JB", "2"}, 1, "Answer it in Portenv"},
		{[]string{"decision", "answer", "01JB", "3"}, 2, "choose 1 to 2"},
		{[]string{"decision", "answer", "01JA", "2"}, 2, "approve or decline"},
		{[]string{"decision", "answer", "01JC", "decline"}, 1, "already answered"},
		{[]string{"decision", "answer", "01JZ", "approve"}, 1, "no request 01JZ"},
		{[]string{"decision", "answer", "01JA"}, 2, ""},
		{[]string{"decision", "answer", "01JA", "approve", "--yes"}, 2, ""},
	}
	for _, c := range cases {
		code, _, errOut := runBoth(t, c.args...)
		if code != c.code || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stderr %q; want %d and %q", c.args, code, errOut, c.code, c.want)
		}
	}
	if slices.Contains(f.called(), "AnswerDecision") {
		t.Fatal("decision answer answered a decision")
	}
}

// The owner's commands never reach portenvd from an agent's shell: an
// agent's portenv has only its grant socket, and the owner-only verbs are
// refused before anything is dialled; msg and tab new stay the agent's
// own grant-checked calls (ADR 0027 §5, R-0359).
func TestAnAgentCantRunTheOwnersCommands(t *testing.T) {
	f := oneWorkspace()
	servingDaemon(t, f)
	sock, got := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true})
	t.Setenv("PORTENV_SOCKET", sock)
	for _, args := range [][]string{
		{"wake", "lead"},
		{"deploy", "production"},
		{"decision", "list"},
		{"decision", "answer", "01JA", "approve"},
		{"team", "start"},
		{"team", "stop", "lead"},
	} {
		code, _, errOut := runBoth(t, args...)
		if code != 2 || errOut != "This is the owner's command; an agent can't run it.\n" {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
	if len(*got) != 0 {
		t.Errorf("an owner's command reached the agent's socket: %v", *got)
	}
	// msg goes to the agent's own socket, as the agent's call.
	if code, _, errOut := runBoth(t, "msg", "lead", "hi"); code != 0 {
		t.Errorf("agent msg: exit %d, stderr %q", code, errOut)
	}
	if len(*got) != 1 || (*got)[0].GetMsg().GetTo() != "lead" {
		t.Errorf("agent msg: %v", *got)
	}
	if calls := f.called(); len(calls) != 0 {
		t.Errorf("an agent's portenv reached portenvd: %v", calls)
	}
}

// In the workspace's own shell (work, no agent socket) the owner's
// commands are refused, as today; nothing reaches portenvd even when its
// socket could be reached.
func TestTheOwnersTeamCommandsAreRefusedInTheWorkspaceShell(t *testing.T) {
	f := oneWorkspace()
	dir := servingDaemon(t, f)
	insideBox(t)
	t.Setenv("PORTENV_HOME", dir)
	for _, args := range [][]string{
		{"wake", "lead"},
		{"deploy", "staging"},
		{"decision", "list"},
		{"decision", "answer", "01JA", "approve"},
		{"team", "start"},
		{"team", "stop"},
	} {
		code, _, errOut := runBoth(t, args...)
		if code != 2 || errOut != "This is the owner's command; run it on your Mac.\n" {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
	for _, args := range [][]string{{"msg", "lead", "hi"}, {"tab", "new", "--name", "x"}} {
		code, _, errOut := runBoth(t, args...)
		if code != 2 || !strings.Contains(errOut, "works only for an agent inside a workspace") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
	if calls := f.called(); len(calls) != 0 {
		t.Errorf("the workspace's shell reached portenvd: %v", calls)
	}
}
