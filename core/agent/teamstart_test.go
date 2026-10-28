// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/portenv/portenv/core/internal/idleinput"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeUsers is init: it makes the agent's run folder in the hub's folder
// as init does, without the chown.
type fakeUsers struct {
	t      *testing.T
	runDir string
	got    []string
	err    error
}

func (f *fakeUsers) EnsureAgentUser(_ context.Context, id string) (AgentUser, error) {
	f.got = append(f.got, id)
	if f.err != nil {
		return AgentUser{}, f.err
	}
	makeTestRunDir(f.t, f.runDir, id, 2001)
	base := filepath.Join(f.runDir, id)
	return AgentUser{UID: 2001, Home: "/home/" + id, Created: true,
		RunDir: base, TmuxDir: filepath.Join(base, "tmux"), TmpDir: filepath.Join(base, "tmp")}, nil
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
		// The image pins the runner: no update nudges in an agent's tab.
		"DISABLE_AUTOUPDATER=1",
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

// R-0258: the runner's idle-input rule, pattern and lines, reaches lane
// A's idle decision; a bad one is refused.
func TestStartTeamAgentSetsTheIdlePattern(t *testing.T) {
	s, _, _, _, _ := newStartServer(t)
	var gotID string
	var got idleinput.Input
	s.setAgentIdleInput = func(id string, in idleinput.Input) error { gotID, got = id, in; return nil }
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, IdlePattern: `^\s*>\s*$`, IdleLines: 6}); err != nil {
		t.Fatal(err)
	}
	if gotID != "builder" || got != (idleinput.Input{Pattern: `^\s*>\s*$`, Lines: 6}) {
		t.Errorf("set %s %v", gotID, got)
	}
	for _, c := range []struct {
		p     string
		lines int32
	}{{"(", 1}, {strings.Repeat("a", idleinput.MaxPattern+1), 1}, {"x", -1}, {"x", idleinput.MaxLines + 1}} {
		if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, IdlePattern: c.p, IdleLines: c.lines}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("pattern %.10q, %d lines: %v", c.p, c.lines, err)
		}
	}
}

// R-0285: newChannelServer's wiring. StartTeamAgent's idle-input rule
// lands in lane A's own SetAgentIdleInput, so the routine check has it.
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
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, IdlePattern: `^\s*>\s*$`, IdleLines: 2}); err != nil {
		t.Fatal(err)
	}
	s.agents.mu.Lock()
	m := s.agents.by["builder"].idleInput
	s.agents.mu.Unlock()
	if m == nil || m.Find([]string{"output", "> ", "footer"}) != 1 || m.Find([]string{"> ", "a", "b"}) != -1 || m.Find([]string{"thinking…"}) != -1 {
		t.Errorf("builder's idle input = %v", m)
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

func (elsewhereUsers) EnsureAgentUser(context.Context, string) (AgentUser, error) {
	return AgentUser{UID: 2001, Home: "/home/builder", RunDir: "/tmp/x", TmuxDir: "/tmp/x/tmux", TmpDir: "/tmp/x/tmp"}, nil
}

// home_seed runs seed-home as the agent, from the project, before its
// terminal starts; a bad request is refused before init is asked, and a
// seeding that fails stops the start (ADR 0022 §2).
func TestStartTeamAgentSeedsItsHome(t *testing.T) {
	s, users, _, started, _ := newStartServer(t)
	s.cfg.User, s.cfg.HomeRoot = "work", "/home"
	type call struct {
		uid  int
		args []string
	}
	var calls []call
	var seedErr error
	s.runHomeIO = func(_ context.Context, uid int, _ []uint32, args []string, _ io.Reader) ([]byte, error) {
		calls = append(calls, call{uid, args})
		return []byte(`{"copied":["docs/style-guide.md"]}`), seedErr
	}
	req := &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, HomeSeed: []string{"docs/style-guide.md", "notes"},
		HomeQuotaBytes: 2 << 30, ProjectDir: "/home/work/acme"}
	if _, err := s.StartTeamAgent(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	want := []string{"seed-home", "/home/work/acme", "/home/builder", strconv.FormatInt(2<<30, 10), "docs/style-guide.md", "notes"}
	if len(calls) != 1 || calls[0].uid != 2001 || !slices.Equal(calls[0].args, want) {
		t.Errorf("seed-home calls %+v, want uid 2001 %v", calls, want)
	}
	if len(*started) != 1 {
		t.Errorf("started %d", len(*started))
	}
	_, _ = s.StopTeamAgent(context.Background(), &agentv1.StopTeamAgentRequest{AgentId: "builder"})

	// No home_seed: no call.
	calls = nil
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}}); err != nil || len(calls) != 0 {
		t.Errorf("no home_seed: %v, calls %v", err, calls)
	}
	_, _ = s.StopTeamAgent(context.Background(), &agentv1.StopTeamAgentRequest{AgentId: "builder"})

	// Refused before init.
	users.got, *started, calls = nil, nil, nil
	for name, bad := range map[string]func(r *agentv1.StartTeamAgentRequest){
		"no project":          func(r *agentv1.StartTeamAgentRequest) { r.ProjectDir = "" },
		"another folder":      func(r *agentv1.StartTeamAgentRequest) { r.ProjectDir = "/home/builder/acme" },
		"the owner's home":    func(r *agentv1.StartTeamAgentRequest) { r.ProjectDir = "/home/work" },
		"a hidden folder":     func(r *agentv1.StartTeamAgentRequest) { r.ProjectDir = "/home/work/.ssh" },
		"an absolute seed":    func(r *agentv1.StartTeamAgentRequest) { r.HomeSeed = []string{"/etc/passwd"} },
		"a seed leaving":      func(r *agentv1.StartTeamAgentRequest) { r.HomeSeed = []string{"../x"} },
		"no quota":            func(r *agentv1.StartTeamAgentRequest) { r.HomeQuotaBytes = 0 },
		"too many seed paths": func(r *agentv1.StartTeamAgentRequest) { r.HomeSeed = make([]string, maxSeedPaths+1) },
	} {
		r := proto.CloneOf(req)
		bad(r)
		if _, err := s.StartTeamAgent(context.Background(), r); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
	if len(users.got) != 0 || len(*started) != 0 || len(calls) != 0 {
		t.Errorf("a refused start asked init %v, started %d, seeded %v", users.got, len(*started), calls)
	}

	// A seeding that fails stops the start, with its reason.
	seedErr = errHomeFull
	_, err := s.StartTeamAgent(context.Background(), req)
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "home_seed couldn't be copied") || len(*started) != 0 {
		t.Errorf("a failed seeding: %v, started %d", err, len(*started))
	}
}
