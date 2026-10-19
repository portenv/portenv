// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// The team's RPCs for the app, and the TeamCalls stream that brings
// agents' calls from each workspace's helper (ADR 0022 §8, §9).

// teamEng is the server's team engine, made on first use.
func (s *Server) teamEng() *teamEngine {
	s.teamOnce.Do(func() {
		s.team = newTeamEngine(s.agentFor, s.loadTeam)
		s.team.readBoard = s.readBoard
		// Routines' timers (R-0258), while portenvd runs.
		go func() {
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-s.bgCtx.Done():
					return
				case <-tick.C:
					s.team.tickRoutines(s.bgCtx)
				}
			}
		}()
		// Message wake-ups (CC-0498): their own, shorter tick.
		go func() {
			tick := time.NewTicker(wakeTick)
			defer tick.Stop()
			for {
				select {
				case <-s.bgCtx.Done():
					return
				case <-tick.C:
					s.team.tickWakes(s.bgCtx)
				}
			}
		}()
	})
	return s.team
}

// projectDir is the workspace's project folder inside the box (ADR 0022
// §3): /home/work/<workspace name>.
func projectDir(name string) string { return "/home/work/" + name }

var agentFileNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}\.md$`)

// teamFilesMu serialises every rebuild and read of a workspace's copy of
// its agent files (R-0260 item 4).
var teamFilesMu sync.Mutex

// withTeamFiles rebuilds state/team-files from files (when not nil), then
// runs fn on it, all under one lock, so no reader sees half a rebuild.
func withTeamFiles(state string, files []*agentv1.AgentFileContent, fn func(dir string) error) error {
	teamFilesMu.Lock()
	defer teamFilesMu.Unlock()
	snap := filepath.Join(state, "team-files")
	if files != nil {
		if err := os.RemoveAll(snap); err != nil {
			return err
		}
		agents := filepath.Join(snap, filepath.FromSlash(agentFilesRelative))
		if err := os.MkdirAll(agents, 0o700); err != nil {
			return err
		}
		for _, f := range files {
			if !agentFileNameRE.MatchString(f.GetName()) || len(f.GetContent()) > maxAgentFileBytes {
				continue // the checks would refuse it; nothing else is written
			}
			if err := os.WriteFile(filepath.Join(agents, f.GetName()), f.GetContent(), 0o600); err != nil {
				return err
			}
		}
	}
	return fn(snap)
}

// withTeam reads the project's agent files through the helper into a
// private copy in portenvd's state folder, checks them, and runs fn with
// the team, under the copy's lock.
func (s *Server) withTeam(ctx context.Context, name string, fn func(src TeamSource, team Team) error) error {
	c, err := s.env.LoadBox(name)
	if err != nil {
		return err
	}
	a, err := s.agentFor(ctx, name)
	if err != nil {
		return err
	}
	r, err := a.ReadAgentFiles(ctx, &agentv1.ReadAgentFilesRequest{ProjectDir: projectDir(name)})
	if err != nil {
		return fmt.Errorf("read the agent files: %w", agentError(err))
	}
	files := r.GetFiles()
	if files == nil {
		files = []*agentv1.AgentFileContent{}
	}
	state := filepath.Join(s.env.Dir, "state", c.ID)
	return withTeamFiles(state, files, func(dir string) error {
		src := TeamSource{ProjectDir: dir, OverridesDir: filepath.Join(s.env.Dir, "agents.local"), WorkspaceID: c.ID, StateDir: state}
		team, err := LoadTeam(src)
		if err != nil {
			return err
		}
		return fn(src, team)
	})
}

func (s *Server) loadTeam(ctx context.Context, name string) (Team, error) {
	var team Team
	err := s.withTeam(ctx, name, func(_ TeamSource, t Team) error { team = t; return nil })
	return team, err
}

func (s *Server) readBoard(ctx context.Context, name string) ([]*agentv1.ReadBoardResponse, error) {
	a, err := s.agentFor(ctx, name)
	if err != nil {
		return nil, err
	}
	st, err := a.ReadBoard(ctx, &agentv1.ReadBoardRequest{})
	if err != nil {
		return nil, err
	}
	var out []*agentv1.ReadBoardResponse
	for {
		l, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if status.Code(err) == codes.Unimplemented && len(out) == 0 {
			return nil, nil // a helper without a board yet: nothing to read
		}
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
}

// ensureTeamPump keeps a TeamCalls stream open to the workspace's helper
// while the workspace is open, so its agents' calls reach the engine.
func (s *Server) ensureTeamPump(name string) {
	s.teamMu.Lock()
	if s.teamPumps == nil {
		s.teamPumps = map[string]bool{}
	}
	if s.teamPumps[name] {
		s.teamMu.Unlock()
		return
	}
	s.teamPumps[name] = true
	s.teamMu.Unlock()
	go func() {
		defer func() {
			s.teamMu.Lock()
			delete(s.teamPumps, name)
			s.teamMu.Unlock()
		}()
		for fails := 0; fails < 30 && s.bgCtx.Err() == nil; {
			if err := s.pumpTeamCalls(s.bgCtx, name); err != nil {
				fails++
			} else {
				fails = 0
			}
			select {
			case <-s.bgCtx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}

func (s *Server) pumpTeamCalls(ctx context.Context, name string) error {
	a, err := s.agentFor(ctx, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := a.TeamCalls(ctx)
	if err != nil {
		return err
	}
	var sendMu sync.Mutex
	eng := s.teamEng()
	for {
		c, err := st.Recv()
		if err != nil {
			return err
		}
		go func() {
			r := eng.handleCall(ctx, name, c)
			r.CallId = c.GetCallId()
			sendMu.Lock()
			defer sendMu.Unlock()
			_ = st.Send(r)
		}()
	}
}

func teamStatus(err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}

func (s *Server) ListAgents(ctx context.Context, req *daemonv1.ListAgentsRequest) (*daemonv1.ListAgentsResponse, error) {
	s.ensureTeamPump(req.GetName())
	eng := s.teamEng()
	team, err := eng.loadTeam(ctx, req.GetName())
	if err != nil {
		return nil, teamStatus(err)
	}
	b, err := eng.lockBox(ctx, req.GetName())
	if err != nil {
		return nil, teamStatus(err)
	}
	defer b.mu.Unlock()
	out := &daemonv1.ListAgentsResponse{}
	for i := range team.Agents {
		out.Agents = append(out.Agents, eng.infoLocked(b, &team.Agents[i]))
	}
	return out, nil
}

// teamSyncEvery is how often an open team looks for agent files added,
// changed or removed.
const teamSyncEvery = 5 * time.Second

// WatchTeam sends the agents, then every board line and agent change from
// the cursor on, including agent files added while it watches.
func (s *Server) WatchTeam(req *daemonv1.WatchTeamRequest, stream daemonv1.DaemonService_WatchTeamServer) error {
	ctx, name := stream.Context(), req.GetName()
	s.ensureTeamPump(name)
	_ = s.teamEng().syncAgents(ctx, name) // a failed look is tried again on the next tick
	look := time.NewTicker(teamSyncEvery)
	defer look.Stop()
	list, err := s.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: name})
	if err != nil {
		return err
	}
	eng := s.teamEng()
	b, err := eng.lockBox(ctx, name)
	if err != nil {
		return teamStatus(err)
	}
	cursor, _ := strconv.Atoi(req.GetCursor()) // a bad cursor starts from the beginning
	b.mu.Unlock()
	for _, a := range list.GetAgents() {
		if err := stream.Send(&daemonv1.WatchTeamResponse{Event: &daemonv1.WatchTeamResponse_Agent{Agent: a}}); err != nil {
			return err
		}
	}
	for {
		b, err := eng.lockBox(ctx, name)
		if err != nil {
			return teamStatus(err)
		}
		cursor = max(0, min(cursor, len(b.events)))
		evs := append([]teamEvent(nil), b.events[cursor:]...)
		cursor = len(b.events)
		changed := b.changed
		b.mu.Unlock()
		for i, ev := range evs {
			r := &daemonv1.WatchTeamResponse{Cursor: fmt.Sprint(cursor - len(evs) + i + 1)}
			switch {
			case ev.msg != nil:
				r.Event = &daemonv1.WatchTeamResponse_Message{Message: ev.msg}
			case ev.dec != nil:
				r.Event = &daemonv1.WatchTeamResponse_Decision{Decision: ev.dec}
			case ev.agent != nil:
				r.Event = &daemonv1.WatchTeamResponse_Agent{Agent: ev.agent}
			case ev.removed != "":
				r.Event = &daemonv1.WatchTeamResponse_RemovedAgentId{RemovedAgentId: ev.removed}
			}
			if err := stream.Send(r); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-look.C:
			_ = eng.syncAgents(ctx, name)
		}
	}
}

func (s *Server) StartAgent(ctx context.Context, req *daemonv1.StartAgentRequest) (*daemonv1.StartAgentResponse, error) {
	s.ensureTeamPump(req.GetName())
	info, err := s.teamEng().startAgent(ctx, req.GetName(), req.GetAgentId())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.StartAgentResponse{Agent: info}, nil
}

// FetchAgentBranch is the owner's Fetch of an agent's branch (ADR 0022
// §2a).
func (s *Server) FetchAgentBranch(ctx context.Context, req *daemonv1.FetchAgentBranchRequest) (*daemonv1.FetchAgentBranchResponse, error) {
	r, err := s.teamEng().fetchAgent(ctx, req.GetName(), req.GetAgentId())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.FetchAgentBranchResponse{Ref: r.GetRef(), Commit: r.GetCommit()}, nil
}

func (s *Server) StopAgent(ctx context.Context, req *daemonv1.StopAgentRequest) (*daemonv1.StopAgentResponse, error) {
	info, err := s.teamEng().stopAgent(ctx, req.GetName(), req.GetAgentId())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.StopAgentResponse{Agent: info}, nil
}

// AnswerDecision is the owner's Approve or Decline, or choice (CC-0501).
// Only the app, in front of the person, reaches portenvd's socket; no
// agent call answers one.
func (s *Server) AnswerDecision(ctx context.Context, req *daemonv1.AnswerDecisionRequest) (*daemonv1.AnswerDecisionResponse, error) {
	d, err := s.teamEng().answerChoice(ctx, req.GetName(), req.GetDecisionId(),
		ownerAnswer{approve: req.GetApprove(), note: req.GetNote(), choice: req.GetChoice(), label: req.GetChoiceLabel()})
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.AnswerDecisionResponse{Decision: d}, nil
}

func (s *Server) GetAgentFile(ctx context.Context, req *daemonv1.GetAgentFileRequest) (*daemonv1.GetAgentFileResponse, error) {
	var out *daemonv1.GetAgentFileResponse
	err := s.withTeam(ctx, req.GetName(), func(src TeamSource, team Team) error {
		a := team.agent(req.GetAgentId())
		if a == nil {
			return status.Errorf(codes.NotFound, "this workspace has no agent %q", req.GetAgentId())
		}
		b, err := os.ReadFile(filepath.Join(src.agentsDir(), a.ID+".md")) // #nosec G304 -- portenvd's own copy
		if err != nil {
			return err
		}
		out = &daemonv1.GetAgentFileResponse{Content: string(b), FileSha256: a.SHA256, ApprovedContent: a.ApprovedContent, Runner: a.RunnerLine}
		return nil
	})
	if err != nil {
		return nil, teamStatus(err)
	}
	return out, nil
}

func (s *Server) ApproveAgentFile(ctx context.Context, req *daemonv1.ApproveAgentFileRequest) (*daemonv1.ApproveAgentFileResponse, error) {
	err := s.withTeam(ctx, req.GetName(), func(src TeamSource, _ Team) error {
		return ApproveAgentFile(src, req.GetAgentId(), req.GetFileSha256())
	})
	if err != nil {
		return nil, teamStatus(err)
	}
	list, err := s.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: req.GetName()})
	if err != nil {
		return nil, err
	}
	for _, a := range list.GetAgents() {
		if a.GetId() == req.GetAgentId() {
			return &daemonv1.ApproveAgentFileResponse{Agent: a}, nil
		}
	}
	return &daemonv1.ApproveAgentFileResponse{}, nil
}

// SendMessage is the owner's message to an agent.
func (s *Server) SendMessage(ctx context.Context, req *daemonv1.SendMessageRequest) (*daemonv1.SendMessageResponse, error) {
	m, err := s.teamEng().ownerMessageOf(ctx, req.GetName(), req.GetTo(), req.GetThreadId(), req.GetText(), req.GetType())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.SendMessageResponse{Message: m}, nil
}

// SetRoutinePaused pauses or resumes a routine from the agent's row menu.
func (s *Server) SetRoutinePaused(ctx context.Context, req *daemonv1.SetRoutinePausedRequest) (*daemonv1.SetRoutinePausedResponse, error) {
	info, err := s.teamEng().setRoutinePaused(ctx, req.GetName(), req.GetAgentId(), int(req.GetIndex()), req.GetPaused())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.SetRoutinePausedResponse{Agent: info}, nil
}

// ListAgentFiles is the Agent files view's listing.
func (s *Server) ListAgentFiles(ctx context.Context, req *daemonv1.ListAgentFilesRequest) (*daemonv1.ListAgentFilesResponse, error) {
	files, used, quota, err := s.teamEng().listAgentFiles(ctx, req.GetName(), req.GetAgentId(), req.GetDir())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.ListAgentFilesResponse{Files: files, HomeBytes: used, HomeQuotaBytes: quota}, nil
}

// ReadAgentFile streams a file from an agent's home to the app.
func (s *Server) ReadAgentFile(req *daemonv1.ReadAgentFileRequest, stream daemonv1.DaemonService_ReadAgentFileServer) error {
	err := s.teamEng().readAgentFile(stream.Context(), req.GetName(), req.GetAgentId(), req.GetPath(), func(b []byte) error {
		return stream.Send(&daemonv1.ReadAgentFileResponse{Data: b})
	})
	if err != nil {
		return teamStatus(err)
	}
	return nil
}

// AddAgentFiles is the owner's drag and drop into an agent's home.
func (s *Server) AddAgentFiles(stream daemonv1.DaemonService_AddAgentFilesServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	sent := false
	added, err := s.teamEng().addAgentFiles(stream.Context(), first.GetName(), first.GetAgentId(), func() (*daemonv1.AddAgentFilesRequest, error) {
		if !sent {
			sent = true
			return first, nil
		}
		return stream.Recv()
	})
	if err != nil {
		return teamStatus(err)
	}
	return stream.SendAndClose(&daemonv1.AddAgentFilesResponse{Added: added})
}
