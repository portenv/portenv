// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeUsers is init: it makes the agent's run folder in the hub's folder
// as init does, without the chown.
type fakeUsers struct {
	t      *testing.T
	runDir string
	got    []string
	files  []string // instructions files asked for
	kept   string   // what init says about the pointer
	err    error
}

func (f *fakeUsers) EnsureAgentUser(_ context.Context, id, instructionsFile string) (AgentUser, error) {
	f.got = append(f.got, id)
	f.files = append(f.files, instructionsFile)
	if f.err != nil {
		return AgentUser{}, f.err
	}
	makeTestRunDir(f.t, f.runDir, id, 2001)
	base := filepath.Join(f.runDir, id)
	return AgentUser{UID: 2001, Home: "/home/" + id, Created: true,
		RunDir: base, TmuxDir: filepath.Join(base, "tmux"), TmpDir: filepath.Join(base, "tmp"), PointerKept: f.kept}, nil
}

type wrote struct {
	uid, gid int
	path     string
	data     string
}

// newStartServer returns an API server whose team pieces are fakes, and
// what they saw.
func newStartServer(t *testing.T) (*apiServer, *fakeUsers, *[]wrote, *[]AgentServerSpec, *[]int) {
	t.Helper()
	uid := 2001
	h := newTestHub(t, &uid)
	users := &fakeUsers{t: t, runDir: h.dir}
	var writes []wrote
	var started []AgentServerSpec
	var killed []int
	s := &apiServer{
		team:  h,
		users: users,
		writeAsUser: func(uid, gid int, path string, data []byte) error {
			writes = append(writes, wrote{uid, gid, path, string(data)})
			return nil
		},
		startAgentServer: func(_ context.Context, spec AgentServerSpec) ([]*typesv1.Tab, error) {
			started = append(started, spec)
			return []*typesv1.Tab{{Id: spec.ID + ":@1", Name: spec.ID, AgentId: spec.ID}}, nil
		},
		stopAgentServer: func(context.Context, string) error { return nil },
		killUID: func(uid int) error {
			killed = append(killed, uid)
			return nil
		},
	}
	return s, users, &writes, &started, &killed
}

func TestStartTeamAgentMakesTheUserThenItsServer(t *testing.T) {
	s, users, writes, started, _ := newStartServer(t)
	r, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{
		AgentId:        "builder",
		Argv:           []string{"claude", "--permission-mode", "acceptEdits"},
		AgentsMd:       "You are Builder in Portenv.\n",
		HomeQuotaBytes: 2 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(users.got, []string{"builder"}) {
		t.Errorf("asked init for %v", users.got)
	}
	if len(*writes) != 1 || (*writes)[0] != (wrote{2001, 2001, "/home/builder/AGENTS.md", "You are Builder in Portenv.\n"}) {
		t.Errorf("AGENTS.md: %+v", *writes)
	}
	if len(*started) != 1 {
		t.Fatalf("started %d servers", len(*started))
	}
	sp := (*started)[0]
	if sp.ID != "builder" || sp.UID != 2001 || sp.GID != 2001 || sp.Home != "/home/builder" ||
		!slices.Equal(sp.Argv, []string{"claude", "--permission-mode", "acceptEdits"}) {
		t.Errorf("spec = %+v", sp)
	}
	for _, want := range []string{
		"HOME=/home/builder", "USER=builder", "PORTENV_AGENT=builder",
		"PORTENV_SOCKET=" + s.team.dir + "/builder/portenv.sock",
		"TMPDIR=" + s.team.dir + "/builder/tmp",
	} {
		if !slices.Contains(sp.Env, want) {
			t.Errorf("env lacks %q: %v", want, sp.Env)
		}
	}
	if sp.SocketDir != s.team.dir+"/builder/tmux" {
		t.Errorf("tmux socket dir %q", sp.SocketDir)
	}
	if len(r.GetTabs()) != 1 || r.GetTabs()[0].GetAgentId() != "builder" {
		t.Errorf("tabs = %v", r.GetTabs())
	}
	// Its socket now answers as builder.
	s.team.mu.Lock()
	id := s.team.byUID[2001]
	s.team.mu.Unlock()
	if id != "builder" {
		t.Errorf("uid 2001 maps to %q", id)
	}
}

func TestStartTeamAgentRefusesBeforeAskingInit(t *testing.T) {
	s, users, _, started, _ := newStartServer(t)
	for _, req := range []*agentv1.StartTeamAgentRequest{
		{AgentId: "Bad", Argv: []string{"claude"}},
		{AgentId: "builder"}, // no runner
		{AgentId: "builder", Argv: []string{""}},
		{AgentId: "builder", Argv: []string{"claude"}, AgentsMd: strings.Repeat("x", maxAgentsMD+1)},
	} {
		if _, err := s.StartTeamAgent(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v, want InvalidArgument", req, err)
		}
	}
	if len(users.got) != 0 || len(*started) != 0 {
		t.Errorf("init asked %v, servers started %d", users.got, len(*started))
	}
}

func TestStartTeamAgentStopsWhenInitRefuses(t *testing.T) {
	s, users, writes, started, _ := newStartServer(t)
	users.err = ErrTeamFull
	_, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}})
	if err == nil || !strings.Contains(err.Error(), "team is full") {
		t.Fatalf("err = %v", err)
	}
	if len(*writes) != 0 || len(*started) != 0 {
		t.Errorf("went on after init refused: %v, %v", *writes, *started)
	}
}

func TestAFailedServerStartClosesTheSocket(t *testing.T) {
	s, _, _, _, _ := newStartServer(t)
	s.startAgentServer = func(context.Context, AgentServerSpec) ([]*typesv1.Tab, error) {
		return nil, errors.New("tmux didn't start")
	}
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}}); err == nil {
		t.Fatal("StartTeamAgent succeeded without a server")
	}
	s.team.mu.Lock()
	_, ok := s.team.agents["builder"]
	s.team.mu.Unlock()
	if ok {
		t.Error("builder's socket is still served")
	}
}

func TestStopTeamAgentEndsEveryProcessOfItsUser(t *testing.T) {
	s, _, _, _, killed := newStartServer(t)
	ctx := context.Background()
	if _, err := s.StartTeamAgent(ctx, &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopTeamAgent(ctx, &agentv1.StopTeamAgentRequest{AgentId: "builder"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*killed, []int{2001}) {
		t.Errorf("killed %v, want [2001]", *killed)
	}
	s.team.mu.Lock()
	_, ok := s.team.agents["builder"]
	s.team.mu.Unlock()
	if ok {
		t.Error("builder's socket is still served after Stop")
	}
	// Stopping an agent that isn't running is fine; a bad id isn't.
	if _, err := s.StopTeamAgent(ctx, &agentv1.StopTeamAgentRequest{AgentId: "builder"}); err != nil {
		t.Errorf("second stop: %v", err)
	}
	if _, err := s.StopTeamAgent(ctx, &agentv1.StopTeamAgentRequest{AgentId: "../x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad id: %v", err)
	}
}

func TestKillUIDNeverTouchesRootOrWork(t *testing.T) {
	for _, uid := range []int{0, 991, 1000, 2000, 2009, -1} {
		if err := checkKillableUID(uid); err == nil {
			t.Errorf("checkKillableUID(%d) = nil", uid)
		}
	}
	for uid := AgentUIDFirst; uid <= AgentUIDLast; uid++ {
		if err := checkKillableUID(uid); err != nil {
			t.Errorf("checkKillableUID(%d) = %v", uid, err)
		}
	}
}

// R-0258: the runner's idle pattern reaches lane A's idle decision.
func TestStartTeamAgentSetsTheIdlePattern(t *testing.T) {
	s, _, _, _, _ := newStartServer(t)
	var got [2]string
	s.setAgentIdleInput = func(id, p string) error { got = [2]string{id, p}; return nil }
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, IdlePattern: `^\s*>\s*$`}); err != nil {
		t.Fatal(err)
	}
	if got != [2]string{"builder", `^\s*>\s*$`} {
		t.Errorf("set %v", got)
	}
	for _, p := range []string{"(", strings.Repeat("a", maxIdlePattern+1)} {
		if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, IdlePattern: p}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("pattern %.10q: %v", p, err)
		}
	}
}

// R-0285: newChannelServer's wiring. StartTeamAgent's idle pattern lands
// in lane A's own SetAgentIdleInput, so the routine check has it.
func TestStartTeamAgentReachesLaneAsIdleInput(t *testing.T) {
	s, _, _, _, _ := newStartServer(t)
	s.setAgentIdleInput = s.SetAgentIdleInput
	s.agents.by = map[string]*agentServer{}
	s.startAgentServer = func(_ context.Context, spec AgentServerSpec) ([]*typesv1.Tab, error) {
		s.agents.mu.Lock()
		s.agents.by[spec.ID] = &agentServer{user: spec}
		s.agents.mu.Unlock()
		return nil, nil
	}
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, IdlePattern: `^\s*>\s*$`}); err != nil {
		t.Fatal(err)
	}
	s.agents.mu.Lock()
	re := s.agents.by["builder"].idleInput
	s.agents.mu.Unlock()
	if re == nil || !re.MatchString("output\n> \n") || re.MatchString("thinking…") {
		t.Errorf("builder's idle input = %v", re)
	}
}

// A run folder init reports anywhere but where the hub serves is refused
// before anything is served.
func TestStartTeamAgentRefusesAnotherRunFolder(t *testing.T) {
	s, _, _, started, _ := newStartServer(t)
	s.users = elsewhereUsers{}
	_, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}})
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "run folder") {
		t.Errorf("StartTeamAgent = %v, want a refusal naming the run folder", err)
	}
	if len(*started) != 0 {
		t.Errorf("started %d servers", len(*started))
	}
}

type elsewhereUsers struct{}

func (elsewhereUsers) EnsureAgentUser(context.Context, string, string) (AgentUser, error) {
	return AgentUser{UID: 2001, Home: "/home/builder", RunDir: "/tmp/x", TmuxDir: "/tmp/x/tmux", TmpDir: "/tmp/x/tmp"}, nil
}

// R-0327: the runner's instructions file reaches init, an unknown path is
// refused before init is asked, and init's "kept" line comes back.
func TestStartTeamAgentAsksInitForThePointer(t *testing.T) {
	s, users, _, _, _ := newStartServer(t)
	users.kept = "~/.claude/CLAUDE.md is the agent's own file, so Portenv left it and didn't add its pointer."
	r, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, InstructionsFile: ".claude/CLAUDE.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(users.files, []string{".claude/CLAUDE.md"}) {
		t.Errorf("init was asked for %q", users.files)
	}
	if r.GetPointerKept() != users.kept {
		t.Errorf("pointer_kept = %q", r.GetPointerKept())
	}
	for _, f := range []string{"../work/.bashrc", ".bashrc", "/etc/passwd", ".claude/settings.json"} {
		_, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "reviewer", Argv: []string{"claude"}, InstructionsFile: f})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("instructions file %q: %v", f, err)
		}
	}
	if slices.Contains(users.got, "reviewer") {
		t.Error("init was asked with a path it doesn't know")
	}
}
