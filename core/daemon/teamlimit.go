// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The owner's agent limit (R-0347, ADR 0022 §7): Settings › Team › Agents
// allowed, how many agents may run in one workspace. 4 by default, 1 to 8
// (teamkit.MaxTeamAgents). portenvd keeps it in team-settings.json in the
// Portenv directory, reads it at every team load, and enforces it at
// Approve and Start; the app only shows it and asks to change it.
//
// Lowering it stops nothing: running agents keep running, the Team section
// says so, and no more start until fewer than the limit run.

const teamSettingsFile = "team-settings.json"

type teamSettings struct {
	AgentsAllowed int `json:"agents_allowed"`
}

// readAgentsAllowed is the owner's limit; the default when it isn't set,
// or when the file can't be used (never more than the owner chose).
func readAgentsAllowed(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, teamSettingsFile)) // #nosec G304 -- fixed name in the Portenv directory
	if err != nil {
		return defaultTeamAgents
	}
	var s teamSettings
	if json.Unmarshal(b, &s) != nil || s.AgentsAllowed < 1 || s.AgentsAllowed > maxTeamAgents {
		return defaultTeamAgents
	}
	return s.AgentsAllowed
}

// writeAgentsAllowed sets the owner's limit; anything but 1 to 8 is
// refused.
func writeAgentsAllowed(dir string, n int) error {
	if n < 1 || n > maxTeamAgents {
		return errAgentsAllowed(n)
	}
	b, err := json.MarshalIndent(teamSettings{AgentsAllowed: n}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAtomic(filepath.Join(dir, teamSettingsFile), append(b, '\n'))
}

// errAgentsAllowed refuses a limit outside 1 to 8.
func errAgentsAllowed(n int) error {
	return fmt.Errorf("%s must be 1 to %d; %d isn't. Choose a number from 1 to %d", "Agents allowed", maxTeamAgents, n, maxTeamAgents)
}

// agentsWord is "1 agent" or "4 agents".
func agentsWord(n int) string {
	if n == 1 {
		return "1 agent"
	}
	return fmt.Sprintf("%d agents", n)
}

// teamLimitInfo is the team against the limit, with the Team section's
// line: over the limit (and the Raise it offers), at it, or one short.
func teamLimitInfo(inTeam, running, allowed int) *daemonv1.TeamLimit {
	l := &daemonv1.TeamLimit{
		AgentsAllowed: int32(allowed), AgentsInTeam: int32(min(inTeam, 1<<20)), AgentsRunning: int32(min(running, 1<<20)), // #nosec G115 -- bounded
	}
	if inTeam > allowed && allowed < maxTeamAgents {
		l.RaiseTo = int32(min(inTeam, maxTeamAgents)) // #nosec G115 -- at most 8
	}
	switch {
	case running > allowed:
		l.Line = fmt.Sprintf("%s are running, and you now allow %d. They keep running; no more start until fewer than %d run.", agentsWord(running), allowed, allowed)
	case inTeam > allowed && allowed >= maxTeamAgents:
		l.Line = fmt.Sprintf("This team has %s; Portenv runs at most %d in a workspace.", agentsWord(inTeam), maxTeamAgents)
	case inTeam > allowed:
		l.Line = fmt.Sprintf("This team has %s, and you allow %d in a workspace: only %d can run at once.", agentsWord(inTeam), allowed, allowed)
	case inTeam == allowed:
		l.Line = fmt.Sprintf("%d of %s allowed: the team is at your limit.", inTeam, agentsWord(allowed))
	case inTeam == allowed-1:
		l.Line = fmt.Sprintf("%d of %s allowed in this workspace.", inTeam, agentsWord(allowed))
	}
	return l
}

// displayName is the agent's name from its file, or its id with a capital.
func (a *TeamAgent) displayName() string {
	if a.Spec != nil && a.Spec.Name != "" {
		return a.Spec.Name
	}
	if a.ID == "" {
		return a.ID
	}
	return string(a.ID[0]-'a'+'A') + a.ID[1:]
}

// startLimitLine is the refusal for a start past the limit.
func startLimitLine(who string, running, allowed int) string {
	return fmt.Sprintf("%s can't start: %s running in this workspace, and you allow %d. Stop one, or raise Agents allowed in Portenv's Settings › Team.",
		who, isAre(running), allowed)
}

func isAre(n int) string {
	if n == 1 {
		return "1 agent is"
	}
	return fmt.Sprintf("%d agents are", n)
}

// approveLimit refuses approving one more agent than the limit. An agent
// approved before (its file changed since) may be approved again.
func (t *Team) approveLimit(id string) error {
	a := t.agent(id)
	if a == nil || a.Approved || a.ApprovedContent != "" {
		return nil
	}
	allowed, approved := t.Limits.normal().MaxAgents, 0
	for i := range t.Agents {
		if o := &t.Agents[i]; o.ID != id && (o.Approved || o.ApprovedContent != "") {
			approved++
		}
	}
	if approved < allowed {
		return nil
	}
	word := "agents are"
	if approved == 1 {
		word = "agent is"
	}
	return fmt.Errorf("%s can't be approved: %d %s approved in this workspace, and you allow %d. Raise Agents allowed in Portenv's Settings › Team, or remove an agent file",
		a.displayName(), approved, word, allowed)
}

// activeLocked counts the agents running or starting, other than skip.
func (b *teamBox) activeLocked(skip string) int {
	n := 0
	for id, s := range b.states {
		if id != skip && (s == typesv1.AgentState_AGENT_STATE_STARTING || s == typesv1.AgentState_AGENT_STATE_RUNNING) {
			n++
		}
	}
	return n
}

// teamLimit is a workspace's team against the owner's limit, now.
func (e *teamEngine) teamLimit(ctx context.Context, name string) *daemonv1.TeamLimit {
	team, err := e.loadTeam(ctx, name)
	if err != nil {
		return nil
	}
	b, err := e.lockBox(ctx, name)
	if err != nil {
		return nil
	}
	defer b.mu.Unlock()
	return e.teamLimitLocked(b, &team)
}

func (e *teamEngine) teamLimitLocked(b *teamBox, team *Team) *daemonv1.TeamLimit {
	running := 0
	for i := range team.Agents {
		if s := b.states[team.Agents[i].ID]; s == typesv1.AgentState_AGENT_STATE_STARTING || s == typesv1.AgentState_AGENT_STATE_RUNNING {
			running++
		}
	}
	return teamLimitInfo(len(team.Agents), running, team.Limits.normal().MaxAgents)
}

// limitChanged wakes every watcher, so each sends the limit again.
func (e *teamEngine) limitChanged() {
	e.mu.Lock()
	boxes := make([]*teamBox, 0, len(e.boxes))
	for _, b := range e.boxes {
		boxes = append(boxes, b)
	}
	e.mu.Unlock()
	for _, b := range boxes {
		b.mu.Lock()
		b.changedLocked()
		b.mu.Unlock()
	}
}

func (s *Server) teamSettingsResponse() *daemonv1.GetTeamSettingsResponse {
	return &daemonv1.GetTeamSettingsResponse{
		AgentsAllowed:        int32(readAgentsAllowed(s.env.Dir)), // #nosec G115 -- 1 to 8
		DefaultAgentsAllowed: defaultTeamAgents,
		MaxAgentsAllowed:     maxTeamAgents,
	}
}

// GetTeamSettings is Settings › Team.
func (s *Server) GetTeamSettings(context.Context, *daemonv1.GetTeamSettingsRequest) (*daemonv1.GetTeamSettingsResponse, error) {
	return s.teamSettingsResponse(), nil
}

// SetAgentsAllowed is the owner's Agents allowed, from Settings or the
// confirmed Raise in the Team section (and, later, the New-workspace Team
// step). Only the owner's app sets it.
func (s *Server) SetAgentsAllowed(ctx context.Context, req *daemonv1.SetAgentsAllowedRequest) (*daemonv1.SetAgentsAllowedResponse, error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if c.kind != callerApp {
		return nil, status.Error(codes.PermissionDenied, "only the owner sets Agents allowed, in Portenv on this Mac")
	}
	n := int(req.GetAgentsAllowed())
	if n < 1 || n > maxTeamAgents {
		return nil, status.Error(codes.InvalidArgument, errAgentsAllowed(n).Error())
	}
	if err := writeAgentsAllowed(s.env.Dir, n); err != nil {
		return nil, err
	}
	s.teamEng().limitChanged()
	return &daemonv1.SetAgentsAllowedResponse{Settings: s.teamSettingsResponse()}, nil
}
