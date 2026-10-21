// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/portenv/portenv/core/internal/githubrepo"
	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The New-workspace Team step (ADR 0025 §3 to §5). The app shows the
// owner exactly the files PlanTeam makes, and the owner approves each by
// its SHA-256. CreateBox checks those approvals and keeps the choice in
// the workspace's state folder; once the workspace runs, ApplyTeamPlan
// makes the same files again, has the helper write them as work (never
// over a file the project has), and pins each one only if its bytes are
// the ones approved. Nothing starts: the team's first start still asks
// the owner, as ADR 0022 has it.

// setupFile is what CreateBox keeps for ApplyTeamPlan: the project's
// source and the team (WorkspaceSetup).
const setupFile = "workspace-setup.json"

func choices(c *daemonv1.TeamPlanChoice) []teamkit.AgentChoice {
	var out []teamkit.AgentChoice
	for _, a := range c.GetAgents() {
		out = append(out, teamkit.AgentChoice{ID: a.GetId(), Include: a.GetInclude(), Name: a.GetName(), Model: a.GetModel(), TeamStatus: a.GetTeamStatus()})
	}
	return out
}

// planTeam is PlanTeam's answer: the template with the choices, each
// included file checked by LoadTeam as any agent file is.
func planTeam(c *daemonv1.TeamPlanChoice, allowed int) (*daemonv1.PlanTeamResponse, error) {
	planned, err := teamkit.Plan(c.GetTemplate(), choices(c))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return planResponse(planned, allowed)
}

func planResponse(planned []teamkit.PlannedAgent, allowed int) (*daemonv1.PlanTeamResponse, error) {
	tmp, err := os.MkdirTemp("", "portenv-plan-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	agents := filepath.Join(tmp, "project", filepath.FromSlash(agentFilesRelative))
	if err := os.MkdirAll(agents, 0o700); err != nil {
		return nil, err
	}
	state := filepath.Join(tmp, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	out := &daemonv1.PlanTeamResponse{AgentsAllowed: int32(allowed)} // #nosec G115 -- 1 to 8
	for _, p := range planned {
		if p.Included {
			out.Included++
			if err := os.WriteFile(filepath.Join(agents, p.ID+".md"), p.Content, 0o600); err != nil {
				return nil, err
			}
		}
	}
	// Checked at the ceiling: the owner's own limit is the line below,
	// with the numbers its Raise needs.
	team, err := LoadTeam(TeamSource{ProjectDir: filepath.Join(tmp, "project"), WorkspaceID: "plan", StateDir: state,
		Limits: TeamLimits{MaxAgents: maxTeamAgents}})
	if err != nil {
		return nil, err
	}
	out.Problems = append(out.Problems, team.Problems...)
	if n := int(out.Included); n > allowed && n <= maxTeamAgents {
		out.Problems = append(out.Problems, fmt.Sprintf("This team has %d agents, and you allow %d in a workspace. Leave %d out, or raise Agents allowed to %d.", n, allowed, n-allowed, n))
	}
	names := map[string]string{}
	for _, p := range planned {
		names[p.ID] = p.Name
	}
	for _, p := range planned {
		runner, _ := teamkit.RunnerByID(p.Runner)
		pa := &daemonv1.PlannedAgent{
			Id: p.ID, Name: p.Name, Role: p.Role, Avatar: p.Avatar, TemplateName: p.TemplateName,
			Included: p.Included, TemplateIncluded: p.TemplateIncluded, Path: agentFilesRelative + "/" + p.Path,
			Content: string(p.Content), Sha256: p.SHA256, Runner: p.Runner, RunnerName: runner.Name, RunnerLine: p.RunnerLine,
			Model: p.Model, TemplateModel: p.TemplateModel, Models: p.Models,
			TemplateTeamStatus: p.TemplateTeamStatus, TeamStatus: p.TeamStatus, Changes: p.Changes,
		}
		if a := team.agent(p.ID); a != nil && p.Included {
			pa.Problems = a.Problems
			if a.Spec != nil {
				pa.Instructions = a.Spec.Instructions
				pa.Grants = grantLines(a, names)
			}
		}
		out.Agents = append(out.Agents, pa)
	}
	return out, nil
}

// grantLines are what a file lets its agent do, in the review screen's
// words (ADR 0025 §4).
func grantLines(a *TeamAgent, names map[string]string) []string {
	name := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	join := func(ids []string) string {
		var ns []string
		for _, id := range ids {
			ns = append(ns, name(id))
		}
		switch len(ns) {
		case 0:
			return ""
		case 1:
			return ns[0]
		}
		return strings.Join(ns[:len(ns)-1], ", ") + " and " + ns[len(ns)-1]
	}
	g := a.Grant
	var out []string
	switch g.GetPanes() {
	case typesv1.GrantMode_GRANT_MODE_NEVER:
		out = append(out, "Opens no panes.")
	default:
		out = append(out, fmt.Sprintf("May open up to %d panes; you approve each.", g.GetMaxPanes()))
	}
	if m := g.GetMessage(); len(m) > 0 {
		out = append(out, "May message "+join(m)+".")
	} else {
		out = append(out, "Messages only you.")
	}
	if g.GetAskOwner() {
		out = append(out, "May ask you.")
	}
	if g.GetPackages() != typesv1.GrantMode_GRANT_MODE_NEVER {
		out = append(out, "May request system packages; you approve each.")
	}
	for _, r := range g.GetReadClones() {
		out = append(out, "Reads "+name(r)+"’s copy, never writes it.")
	}
	if g.GetTeamStatus() {
		out = append(out, "Sees the team’s status (states and status lines, never terminals or files).")
	}
	if a.Spec.Checkout == CheckoutNone {
		out = append(out, "Works without its own copy of the project.")
	} else {
		out = append(out, "Works in its own copy of the project, on the branch agent/"+a.ID+".")
	}
	for _, r := range a.Spec.Routines {
		every := fmt.Sprintf("%d min", r.EveryMinutes)
		if r.EveryMinutes%60 == 0 {
			every = fmt.Sprintf("%d h", r.EveryMinutes/60)
		}
		out = append(out, "Every "+every+": "+r.Note)
	}
	return out
}

// checkTeamChoice is CreateBox's check of the team: a plan with no
// problems, within the owner's limit, every included agent approved as
// the file it would be now.
func checkTeamChoice(c *daemonv1.TeamPlanChoice, allowed int) error {
	r, err := planTeam(c, allowed)
	if err != nil {
		return err
	}
	if len(r.GetProblems()) > 0 {
		return status.Error(codes.InvalidArgument, r.GetProblems()[0])
	}
	approved := map[string]string{}
	for _, a := range c.GetAgents() {
		approved[a.GetId()] = a.GetApprovedSha256()
	}
	for _, a := range r.GetAgents() {
		if !a.GetIncluded() {
			continue
		}
		if len(a.GetProblems()) > 0 {
			return status.Error(codes.InvalidArgument, a.GetProblems()[0])
		}
		switch sum := approved[a.GetId()]; {
		case sum == "":
			return status.Errorf(codes.InvalidArgument, "Approve %s’s file before you create the workspace.", a.GetName())
		case sum != a.GetSha256():
			return status.Errorf(codes.InvalidArgument, "%s’s file changed since you approved it. Review it again, then create the workspace.", a.GetName())
		}
	}
	return nil
}

// checkSetup is CreateBox's check: the project's source (Empty, or a
// GitHub https address), and the team, approved file by file.
func checkSetup(st *daemonv1.WorkspaceSetup, allowed int) error {
	if p := st.GetProject(); p != nil {
		switch src := p.GetSource().(type) {
		case *daemonv1.ProjectSource_Empty:
		case *daemonv1.ProjectSource_GithubUrl:
			if _, err := githubrepo.Parse(src.GithubUrl); err != nil {
				return status.Error(codes.InvalidArgument, "Use a GitHub repository\u2019s https address, like https://github.com/acme/acme-api.")
			}
		default:
			return status.Error(codes.InvalidArgument, "Choose where the project comes from.")
		}
	}
	if t := st.GetTeam(); t != nil {
		return checkTeamChoice(t, allowed)
	}
	return nil
}

// keepSetup keeps what New workspace chose in the workspace's state
// folder (0600), for ApplyTeamPlan. It never holds a sign-in.
func keepSetup(stateDir string, st *daemonv1.WorkspaceSetup) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := protojson.Marshal(st)
	if err != nil {
		return err
	}
	return writePrivateAtomic(filepath.Join(stateDir, setupFile), append(b, '\n'))
}

// keepTeamPlan keeps a team with no project source.
func keepTeamPlan(stateDir string, c *daemonv1.TeamPlanChoice) error {
	return keepSetup(stateDir, &daemonv1.WorkspaceSetup{Team: c})
}

// loadSetup is the kept setup; nil when there is none.
func loadSetup(stateDir string) (*daemonv1.WorkspaceSetup, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, setupFile)) // #nosec G304 -- portenvd's own folder for the workspace
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st daemonv1.WorkspaceSetup
	if err := protojson.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("what New workspace kept for this workspace can't be read: %w", err)
	}
	return &st, nil
}

// loadTeamPlan is the kept team; nil when there is none.
func loadTeamPlan(stateDir string) (*daemonv1.TeamPlanChoice, error) {
	st, err := loadSetup(stateDir)
	if st == nil || err != nil {
		return nil, err
	}
	return st.GetTeam(), nil
}

// teamPlanMu keeps two ApplyTeamPlans (two windows) from both writing.
var teamPlanMu sync.Mutex

// ListTeamTemplates is the Team step's picker.
func (s *Server) ListTeamTemplates(context.Context, *daemonv1.ListTeamTemplatesRequest) (*daemonv1.ListTeamTemplatesResponse, error) {
	allowed := readAgentsAllowed(s.env.Dir)
	out := &daemonv1.ListTeamTemplatesResponse{}
	for _, t := range teamkit.TemplateSummaries() {
		r, err := planTeam(&daemonv1.TeamPlanChoice{Template: t.ID}, allowed)
		if err != nil {
			return nil, err
		}
		out.Templates = append(out.Templates, &daemonv1.TeamTemplate{Id: t.ID, Name: t.Name, Description: t.Description, Agents: r.GetAgents()})
	}
	return out, nil
}

// PlanTeam is the Team step's files after each change the owner makes.
func (s *Server) PlanTeam(_ context.Context, req *daemonv1.PlanTeamRequest) (*daemonv1.PlanTeamResponse, error) {
	return planTeam(req.GetChoice(), readAgentsAllowed(s.env.Dir))
}

// ApplyTeamPlan writes and pins the team CreateBox kept, once.
func (s *Server) ApplyTeamPlan(ctx context.Context, req *daemonv1.ApplyTeamPlanRequest) (*daemonv1.ApplyTeamPlanResponse, error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if c.kind != callerApp {
		return nil, status.Error(codes.PermissionDenied, "only the owner's Portenv on this Mac sets up a workspace's team")
	}
	teamPlanMu.Lock()
	defer teamPlanMu.Unlock()
	box, err := s.env.LoadBox(req.GetName())
	if err != nil {
		return nil, teamStatus(err)
	}
	state := filepath.Join(s.env.Dir, "state", box.ID)
	setup, err := loadSetup(state)
	if err != nil {
		return nil, teamStatus(err)
	}
	if setup == nil {
		return &daemonv1.ApplyTeamPlanResponse{}, nil
	}
	choice := setup.GetTeam()
	var planned []teamkit.PlannedAgent
	if choice != nil {
		if planned, err = teamkit.Plan(choice.GetTemplate(), choices(choice)); err != nil {
			return nil, teamStatus(err)
		}
	}
	h, err := s.agentFor(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	// The project first (ADR 0025 §2). A clone that fails writes and pins
	// nothing, and the setup stays for Try Again.
	if setup.GetProject().GetGithubUrl() != "" {
		o := s.cloneProject(ctx, h, req.GetName(), setup.GetProject(), req.GetGithubToken(), planned)
		if o == nil {
			// The workspace's own repository, the one pushes go to (R-0391).
			if repo, err := githubrepo.Parse(setup.GetProject().GetGithubUrl()); err == nil {
				if err := keepGitHubRepo(state, repo.URL()); err != nil {
					return nil, err
				}
			}
		}
		if o != nil {
			if o.needToken {
				return &daemonv1.ApplyTeamPlanResponse{NeedsToken: true}, nil
			}
			s.log.Info("project not brought in", "box_id", box.ID, "sign_in", o.needsSignIn)
			return &daemonv1.ApplyTeamPlanResponse{ProjectError: o.line, NeedsSignIn: o.needsSignIn}, nil
		}
	}
	out := &daemonv1.ApplyTeamPlanResponse{Applied: true}
	if len(planned) == 0 && !setup.GetProject().GetEmpty() {
		// A cloned project and no team: nothing more to write.
		if err := os.Remove(filepath.Join(state, setupFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		s.log.Info("workspace setup applied", "box_id", box.ID)
		return out, nil
	}
	w := &agentv1.WriteAgentFilesRequest{ProjectDir: projectDir(req.GetName())}
	for _, p := range planned {
		w.Files = append(w.Files, &agentv1.AgentFileContent{Name: p.Path, Content: p.Content})
	}
	wr, err := h.WriteAgentFiles(ctx, w)
	if err != nil {
		s.log.Warn("team plan not applied", "box_id", box.ID, "err", noPaths(err.Error()))
		return nil, teamStatus(fmt.Errorf("write the team's files: %w", agentError(err)))
	}
	existing := map[string]bool{}
	for _, e := range wr.GetExisting() {
		existing[e] = true
	}
	approved := map[string]string{}
	for _, a := range choice.GetAgents() {
		approved[a.GetId()] = a.GetApprovedSha256()
	}
	err = s.withTeam(ctx, req.GetName(), func(src TeamSource, _ Team) error {
		for _, p := range planned {
			if !p.Included {
				continue
			}
			switch {
			case existing[p.Path]:
				out.Lines = append(out.Lines, fmt.Sprintf("%s already had an agent file for %s, so Portenv kept it. Review it in the Team section before it starts.", req.GetName(), p.ID))
			case approved[p.ID] == "" || approved[p.ID] != p.SHA256:
				out.Lines = append(out.Lines, fmt.Sprintf("%s’s file isn’t the one you approved, so it isn’t approved. Review it in the Team section.", p.Name))
			default:
				if err := ApproveAgentFile(src, p.ID, approved[p.ID]); err != nil {
					out.Lines = append(out.Lines, fmt.Sprintf("%s’s file couldn’t be approved: %v. Review it in the Team section.", p.Name, err))
					continue
				}
				out.Pinned = append(out.Pinned, p.ID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, teamStatus(err)
	}
	if err := os.Remove(filepath.Join(state, setupFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	s.log.Info("team plan applied", "box_id", box.ID, "written", len(wr.GetWritten()), "pinned", len(out.Pinned), "kept", len(wr.GetExisting()))
	s.teamEng().limitChanged()
	return out, nil
}

// cloneMargin is the room a clone leaves free (ADR 0025 §2).
const cloneMargin = 1_000_000_000

// cloneLimit is the most a repository's pack may be, so that work's copy
// and each coding agent's, each with its checkout (about twice the pack),
// fit with the margin to spare; 0 when nothing fits.
func cloneLimit(free uint64, copies int) int64 {
	if free <= cloneMargin || copies < 1 {
		return 0
	}
	return int64((free - cloneMargin) / uint64(2*copies)) // #nosec G115 -- a disk's bytes
}

// tooBigLine is ADR 0025 §2's line for a repository the workspace can't
// hold with its team.
func tooBigLine(name string, each int64, copies int, free uint64) string {
	need := uint64(each)*2*uint64(copies) + cloneMargin // #nosec G115 -- positive
	return fmt.Sprintf("%s needs about %s with its team (%d copies of %s, each with its checkout). This workspace has %s free.",
		name, gigabytes(need), copies, gigabytes(uint64(each)), gigabytes(free)) // #nosec G115 -- positive
}

const (
	needsSignInLine = "This repository needs a sign-in. Sign in with GitHub, then try again."
	signInEndedLine = "Your GitHub sign-in in Portenv has ended, or doesn\u2019t reach this repository. Sign in again, then try again."
)

// cloneOutcome is a project that couldn't be brought in.
type cloneOutcome struct {
	line                   string
	needsSignIn, needToken bool
}

// cloneProject brings the GitHub repository in as the project (R-0391):
// fetched here on the Mac, with the owner's sign-in only for a repository
// that needs it, into a 0600 bundle in portenvd's own folder, streamed to
// the helper, which checks it and clones from it as work with no network.
// The bundle is removed whatever happens; the sign-in is used for this
// fetch alone and never leaves this process.
func (s *Server) cloneProject(ctx context.Context, h agentv1.AgentServiceClient, name string, src *daemonv1.ProjectSource, token string, planned []teamkit.PlannedAgent) *cloneOutcome {
	repo, err := githubrepo.Parse(src.GetGithubUrl())
	if err != nil {
		return &cloneOutcome{line: err.Error()}
	}
	room, err := h.GetProjectRoom(ctx, &agentv1.GetProjectRoomRequest{ProjectDir: projectDir(name)})
	if err != nil {
		return &cloneOutcome{line: "Portenv couldn\u2019t look at the workspace\u2019s room for " + repo.String() + ". Try again."}
	}
	if room.GetExists() {
		return nil // an earlier try brought it in
	}
	if src.GetUseSignIn() && token == "" {
		return &cloneOutcome{needToken: true}
	}
	// A repository kept without the sign-in is fetched plainly first: a
	// public one is never sent it. Only when GitHub refuses that fetch is
	// the sign-in used (the owner may have signed in since Create).
	signIn := token
	if !src.GetUseSignIn() {
		token = ""
	}
	copies := 1
	for _, p := range planned {
		if p.Included && p.OwnClone {
			copies++
		}
	}
	limit := cloneLimit(room.GetFreeBytes(), copies)
	api, gitBase := s.githubAPI, s.githubGit
	if api == "" {
		api = githubrepo.API
	}
	if gitBase == "" {
		gitBase = "https://github.com"
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	est, _ := githubrepo.Size(sctx, &http.Client{Timeout: 10 * time.Second}, api, repo, token)
	cancel()
	if est > limit {
		return &cloneOutcome{line: tooBigLine(name, est, copies, room.GetFreeBytes())}
	}
	if limit <= 0 {
		return &cloneOutcome{line: tooBigLine(name, max(est, 1), copies, room.GetFreeBytes())}
	}

	dir := filepath.Join(s.env.Dir, "clone")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
	}
	f, err := os.CreateTemp(dir, "bundle-*")
	if err != nil {
		return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	if err := f.Chmod(0o600); err != nil {
		return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
	}
	got, err := githubrepo.FetchBundle(ctx, client, gitBase+"/"+repo.Owner+"/"+repo.Name, token, f, limit)
	if errors.Is(err, githubrepo.ErrNeedsSignIn) && token == "" {
		if signIn == "" {
			return &cloneOutcome{needToken: true} // the app reads the Keychain, or says to sign in
		}
		token = signIn
		if err := f.Truncate(0); err != nil {
			return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
		}
		if _, err := f.Seek(0, 0); err != nil {
			return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
		}
		got, err = githubrepo.FetchBundle(ctx, client, gitBase+"/"+repo.Owner+"/"+repo.Name, token, f, limit)
	}
	switch {
	case errors.Is(err, githubrepo.ErrNeedsSignIn) && token != "":
		return &cloneOutcome{line: signInEndedLine, needsSignIn: true}
	case errors.Is(err, githubrepo.ErrNeedsSignIn):
		return &cloneOutcome{line: needsSignInLine, needsSignIn: true}
	case errors.Is(err, githubrepo.ErrTooBig):
		return &cloneOutcome{line: tooBigLine(name, limit+1, copies, room.GetFreeBytes())}
	case err != nil:
		return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ": " + err.Error() + "."}
	}
	stream, err := h.CloneProject(ctx)
	if err != nil {
		return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
	}
	start := &agentv1.CloneProjectStart{ProjectDir: projectDir(name), Url: repo.URL(), MaxBytes: limit + 64<<20, Head: got.Head, Empty: got.Empty}
	if err := stream.Send(&agentv1.CloneProjectRequest{Msg: &agentv1.CloneProjectRequest_Start{Start: start}}); err != nil {
		return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
	}
	if !got.Empty {
		if _, err := f.Seek(0, 0); err != nil {
			return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
		}
		buf := make([]byte, 1<<20)
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				if err := stream.Send(&agentv1.CloneProjectRequest{Msg: &agentv1.CloneProjectRequest_Chunk{Chunk: buf[:n]}}); err != nil {
					break // the answer below says why
				}
			}
			if rerr != nil {
				break
			}
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		st, _ := status.FromError(err)
		switch st.Code() {
		case codes.ResourceExhausted, codes.FailedPrecondition:
			return &cloneOutcome{line: st.Message()}
		}
		return &cloneOutcome{line: "Portenv couldn\u2019t clone " + repo.String() + ". Try again."}
	}
	return nil
}
