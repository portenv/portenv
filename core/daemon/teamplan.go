// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

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

const teamPlanFile = "team-plan.json"

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

// keepTeamPlan keeps the approved choice in the workspace's state folder
// (0600), for ApplyTeamPlan.
func keepTeamPlan(stateDir string, c *daemonv1.TeamPlanChoice) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := protojson.Marshal(c)
	if err != nil {
		return err
	}
	return writePrivateAtomic(filepath.Join(stateDir, teamPlanFile), append(b, '\n'))
}

// loadTeamPlan is the kept choice; nil when there is none.
func loadTeamPlan(stateDir string) (*daemonv1.TeamPlanChoice, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, teamPlanFile)) // #nosec G304 -- portenvd's own folder for the workspace
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c daemonv1.TeamPlanChoice
	if err := protojson.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("the team kept for this workspace can't be read: %w", err)
	}
	return &c, nil
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
	choice, err := loadTeamPlan(state)
	if err != nil {
		return nil, teamStatus(err)
	}
	if choice == nil {
		return &daemonv1.ApplyTeamPlanResponse{}, nil
	}
	planned, err := teamkit.Plan(choice.GetTemplate(), choices(choice))
	if err != nil {
		return nil, teamStatus(err)
	}
	h, err := s.agentFor(ctx, req.GetName())
	if err != nil {
		return nil, err
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
	out := &daemonv1.ApplyTeamPlanResponse{Applied: true}
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
	if err := os.Remove(filepath.Join(state, teamPlanFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	s.log.Info("team plan applied", "box_id", box.ID, "written", len(wr.GetWritten()), "pinned", len(out.Pinned), "kept", len(wr.GetExisting()))
	s.teamEng().limitChanged()
	return out, nil
}
