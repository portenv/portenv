// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/portenv/portenv/core/local"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// The New-workspace Team step (ADR 0025): the plan the owner reads and
// approves, kept by CreateBox, written and pinned by ApplyTeamPlan.

func plannedByID(t *testing.T, r *daemonv1.PlanTeamResponse, id string) *daemonv1.PlannedAgent {
	t.Helper()
	for _, a := range r.GetAgents() {
		if a.GetId() == id {
			return a
		}
	}
	t.Fatalf("no planned agent %q", id)
	return nil
}

func TestPlanTeamChecksEachFile(t *testing.T) {
	r, err := planTeam(&daemonv1.TeamPlanChoice{Template: "starter"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if r.GetIncluded() != 3 || r.GetAgentsAllowed() != 4 || len(r.GetProblems()) != 0 {
		t.Fatalf("%v", r)
	}
	rev := plannedByID(t, r, "reviewer")
	if len(rev.GetProblems()) != 0 || rev.GetRunnerName() != "Claude Code" || !strings.HasPrefix(rev.GetInstructions(), "You review") {
		t.Fatalf("%v", rev)
	}
	want := []string{
		"May open up to 2 panes; you approve each.",
		"May message Lead and Builder.",
		"May ask you.",
		"May request system packages; you approve each.",
		"Reads Builder’s copy, never writes it.",
	}
	for _, w := range want {
		if !slices.Contains(rev.GetGrants(), w) {
			t.Errorf("reviewer's grants %q lack %q", rev.GetGrants(), w)
		}
	}
	if rev.GetSha256() != sha(rev.GetContent()) {
		t.Fatal("the sha isn't the content's")
	}
}

// More agents than the owner allows: one line saying so, with the
// numbers the Team step's Raise needs.
func TestPlanTeamSaysTheLimit(t *testing.T) {
	r, err := planTeam(&daemonv1.TeamPlanChoice{Template: "orchestrate"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if r.GetIncluded() != 6 || !slices.Contains(r.GetProblems(), "This team has 6 agents, and you allow 4 in a workspace. Leave 2 out, or raise Agents allowed to 6.") {
		t.Fatalf("%v", r.GetProblems())
	}
	r, _ = planTeam(&daemonv1.TeamPlanChoice{Template: "orchestrate"}, 6)
	if len(r.GetProblems()) != 0 {
		t.Fatalf("at the limit: %v", r.GetProblems())
	}
}

func TestPlanTeamRefusesABadChoice(t *testing.T) {
	_, err := planTeam(&daemonv1.TeamPlanChoice{Template: "starter", Agents: []*daemonv1.TeamAgentChoice{{Id: "lead", Include: true, Model: "gpt-5"}}}, 4)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("%v", err)
	}
}

func approvedChoice(t *testing.T, template string, agents ...*daemonv1.TeamAgentChoice) *daemonv1.TeamPlanChoice {
	t.Helper()
	c := &daemonv1.TeamPlanChoice{Template: template, Agents: agents}
	r, err := planTeam(c, 8)
	if err != nil {
		t.Fatal(err)
	}
	out := &daemonv1.TeamPlanChoice{Template: template}
	for _, a := range r.GetAgents() {
		ch := &daemonv1.TeamAgentChoice{Id: a.GetId(), Include: a.GetIncluded(), Name: a.GetName(), Model: a.GetModel(), TeamStatus: a.GetTeamStatus()}
		if a.GetIncluded() {
			ch.ApprovedSha256 = a.GetSha256()
		}
		out.Agents = append(out.Agents, ch)
	}
	return out
}

// CreateBox keeps only a team the owner approved, file by file, within
// the limit; anything else is refused before the workspace is made.
func TestCheckTeamChoice(t *testing.T) {
	ok := approvedChoice(t, "starter", &daemonv1.TeamAgentChoice{Id: "reviewer", Include: true, Model: "opus"})
	if err := checkTeamChoice(ok, 4); err != nil {
		t.Fatal(err)
	}
	unapproved := approvedChoice(t, "starter")
	unapproved.Agents[1].ApprovedSha256 = ""
	changed := approvedChoice(t, "starter")
	changed.Agents[0].Model = "sonnet" // approved as it was before
	over := approvedChoice(t, "orchestrate")
	for name, c := range map[string]*daemonv1.TeamPlanChoice{"unapproved": unapproved, "changed after approval": changed, "over the limit": over} {
		if err := checkTeamChoice(c, 4); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// fakeProjectAgent is a workspace's helper for one project folder: it
// writes and reads agent files as the helper does (new-only).
type fakeProjectAgent struct {
	agentv1.UnimplementedAgentServiceServer
	mu     sync.Mutex
	root   string // stands for /home/work
	writes int
}

func (f *fakeProjectAgent) dir(project string) string {
	return filepath.Join(f.root, strings.TrimPrefix(project, "/home/work/"))
}

func (f *fakeProjectAgent) WriteAgentFiles(_ context.Context, r *agentv1.WriteAgentFilesRequest) (*agentv1.WriteAgentFilesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	out := &agentv1.WriteAgentFilesResponse{}
	agents := filepath.Join(f.dir(r.GetProjectDir()), ".portenv", "agents")
	for _, file := range r.GetFiles() {
		p := filepath.Join(agents, filepath.FromSlash(file.GetName()))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		h, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			out.Existing = append(out.Existing, file.GetName())
			continue
		}
		if err != nil {
			return nil, err
		}
		_, _ = h.Write(file.GetContent())
		_ = h.Close()
		out.Written = append(out.Written, file.GetName())
	}
	return out, nil
}

func (f *fakeProjectAgent) ReadAgentFiles(_ context.Context, r *agentv1.ReadAgentFilesRequest) (*agentv1.ReadAgentFilesResponse, error) {
	out := &agentv1.ReadAgentFilesResponse{}
	agents := filepath.Join(f.dir(r.GetProjectDir()), ".portenv", "agents")
	ents, _ := os.ReadDir(agents)
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md") {
			b, _ := os.ReadFile(filepath.Join(agents, e.Name()))
			out.Files = append(out.Files, &agentv1.AgentFileContent{Name: e.Name(), Content: b})
		}
	}
	return out, nil
}

func servingProject(t *testing.T) (*Server, *fakeProjectAgent, daemonv1.DaemonServiceClient) {
	t.Helper()
	t.Setenv("PORTENV_KEYS", "file")
	fake := &fakeProjectAgent{root: t.TempDir()}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s, c := servingWith(t, func(s *Server) {
		boxWithState(t, s, "")
		s.open["acme-api"] = &openBox{
			closed: true,
			sess:   &local.Session{Cfg: local.BoxConfig{ID: "box-1", Name: "acme-api"}},
			agent:  agentv1.NewAgentServiceClient(conn),
		}
	})
	return s, fake, c
}

// ADR 0025 §12's gate: the kept team is written (left-out agents to
// off/), each approved file pinned, once; a file the project already had
// is kept and not pinned; and nothing is kept afterwards.
func TestApplyTeamPlan(t *testing.T) {
	s, fake, c := servingProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Nothing kept: nothing done.
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || r.GetApplied() || fake.writes != 0 {
		t.Fatalf("%v %v", r, err)
	}

	choice := approvedChoice(t, "orchestrate",
		&daemonv1.TeamAgentChoice{Id: "reviewer", Include: true, Model: "opus"},
		&daemonv1.TeamAgentChoice{Id: "devops", Include: false},
		&daemonv1.TeamAgentChoice{Id: "builder-c", Include: false})
	// The project already has its own coordinator.
	agents := filepath.Join(fake.root, "acme-api", ".portenv", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "---\nid: coordinator\nname: Mine\nrunner: claude\n---\nmine\n"
	if err := os.WriteFile(filepath.Join(agents, "coordinator.md"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := keepTeamPlan(filepath.Join(s.env.Dir, "state", "box-1"), choice); err != nil {
		t.Fatal(err)
	}
	r, err = c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetApplied() {
		t.Fatalf("%v %v", r, err)
	}
	if !slices.Equal(r.GetPinned(), []string{"reviewer", "builder-a", "builder-b"}) {
		t.Fatalf("pinned %v", r.GetPinned())
	}
	if len(r.GetLines()) != 1 || !strings.Contains(r.GetLines()[0], "already had an agent file for coordinator") {
		t.Fatalf("lines %v", r.GetLines())
	}
	if b, _ := os.ReadFile(filepath.Join(agents, "coordinator.md")); string(b) != mine {
		t.Fatal("the project's own file was overwritten")
	}
	for _, off := range []string{"off/devops.md", "off/builder-c.md", "off/design-system.md"} {
		if _, err := os.Stat(filepath.Join(agents, filepath.FromSlash(off))); err != nil {
			t.Errorf("%s: %v", off, err)
		}
	}
	list, err := c.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	approved := map[string]bool{}
	for _, a := range list.GetAgents() {
		approved[a.GetId()] = a.GetApproved()
	}
	if !approved["reviewer"] || !approved["builder-a"] || approved["coordinator"] {
		t.Fatalf("approved %v", approved)
	}
	rev, _ := os.ReadFile(filepath.Join(agents, "reviewer.md"))
	if !strings.Contains(string(rev), "runner: claude --model opus") || strings.Contains(string(rev), "builder-c") {
		t.Fatalf("reviewer.md:\n%s", rev)
	}
	// Once: the kept team is gone.
	writes := fake.writes
	r, err = c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || r.GetApplied() || fake.writes != writes {
		t.Fatalf("second apply: %v %v", r, err)
	}
}

// A file whose bytes aren't the ones approved is written, never pinned.
func TestApplyTeamPlanPinsOnlyTheApprovedBytes(t *testing.T) {
	s, _, c := servingProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	choice := approvedChoice(t, "starter")
	choice.Agents[1].ApprovedSha256 = strings.Repeat("0", 64) // builder's
	if err := keepTeamPlan(filepath.Join(s.env.Dir, "state", "box-1"), choice); err != nil {
		t.Fatal(err)
	}
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.GetPinned(), "builder") || !slices.Contains(r.GetPinned(), "lead") || len(r.GetLines()) != 1 || !strings.Contains(r.GetLines()[0], "Builder\u2019s file isn\u2019t the one you approved") {
		t.Fatalf("%v", r)
	}
}

func TestTheKeptTeamIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "box-1")
	if err := keepTeamPlan(dir, approvedChoice(t, "starter")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, teamPlanFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	got, err := loadTeamPlan(dir)
	if err != nil || got == nil || got.GetTemplate() != "starter" || len(got.GetAgents()) != 3 {
		t.Fatalf("%v %v", got, err)
	}
}

// CreateBox with a team: an unapproved one makes nothing; an approved one
// is kept for ApplyTeamPlan, and kept again on a retry.
func TestCreateBoxKeepsTheApprovedTeam(t *testing.T) {
	useRestic(t)
	s, _ := loggingServer(t)
	ctx := context.Background()
	bad := createBoxRequest("acme-api", testBoxID)
	bad.Team = approvedChoice(t, "starter")
	bad.Team.Agents[0].ApprovedSha256 = ""
	if _, err := s.CreateBox(ctx, bad); status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "Approve Lead") {
		t.Fatalf("unapproved: %v", err)
	}
	if list, _ := s.ListBoxes(ctx, &daemonv1.ListBoxesRequest{}); len(list.GetBoxes()) != 0 {
		t.Fatalf("a box was made: %v", list.GetBoxes())
	}
	req := createBoxRequest("acme-api", testBoxID)
	req.Team = approvedChoice(t, "starter")
	if _, err := s.CreateBox(ctx, req); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(s.env.Dir, "state", testBoxID)
	kept, err := loadTeamPlan(state)
	if err != nil || kept.GetTemplate() != "starter" {
		t.Fatalf("%v %v", kept, err)
	}
	if err := os.Remove(filepath.Join(state, teamPlanFile)); err != nil {
		t.Fatal(err)
	}
	if again, err := s.CreateBox(ctx, req); err != nil || !again.GetAlreadyCreated() {
		t.Fatalf("again: %v %v", again, err)
	}
	if kept, _ := loadTeamPlan(state); kept == nil {
		t.Fatal("a retry didn't keep the team")
	}
}
