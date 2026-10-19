// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// maxIdlePattern is the helper's limit on an idle-input pattern.
const maxIdlePattern = 512

// maxAgentsMD caps AGENTS.md: the generated block and a file's
// instructions (32 KB, ADR 0022 §3), with room to spare.
const maxAgentsMD = 64 << 10

// AgentServerSpec is what starting one agent's own tmux server needs
// (ADR 0022 §1). Lane A's terminal code starts the server and its control
// client as UID and GID (with the team group, no capabilities and
// no_new_privs), on SocketDir, and runs Argv in its first tab with Env.
type AgentServerSpec struct {
	ID        string
	UID, GID  int
	Home      string
	SocketDir string
	Argv      []string
	Env       []string
	// WorkDir is where the runner starts: the agent's clone
	// (/home/<id>/<workspace>), or its home when empty.
	WorkDir string
}

// errNoAgentServers is what StartTeamAgent says until a build has the
// per-agent tmux servers.
var errNoAgentServers = errors.New("this build can't start agents' terminals yet")

// agentUsers makes agent users: init, over its own socket.
type agentUsers interface {
	EnsureAgentUser(ctx context.Context, id string) (AgentUser, error)
}

// initUsers asks init (root) on its local socket.
type initUsers struct{ socket string }

func (u initUsers) EnsureAgentUser(ctx context.Context, id string) (AgentUser, error) {
	conn, err := grpc.NewClient("unix://"+u.socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return AgentUser{}, fmt.Errorf("connect to init: %w", err)
	}
	defer func() { _ = conn.Close() }()
	r, err := agentv1.NewAgentServiceClient(conn).EnsureAgentUser(ctx, &agentv1.EnsureAgentUserRequest{AgentId: id})
	if err != nil {
		return AgentUser{}, errors.New(status.Convert(err).Message())
	}
	return AgentUser{
		UID: int(r.GetUid()), Home: r.GetHome(), Created: r.GetCreated(),
		RunDir: r.GetRunDir(), TmuxDir: r.GetTmuxDir(), TmpDir: r.GetTmpDir(),
	}, nil
}

// StartTeamAgent starts one agent (ADR 0022 §8): init makes its user if
// it's new, and its run folder; the helper serves its socket in that folder, writes AGENTS.md as the agent,
// and starts its own tmux server with the runner in its first tab.
func (s *apiServer) StartTeamAgent(ctx context.Context, req *agentv1.StartTeamAgentRequest) (*agentv1.StartTeamAgentResponse, error) {
	if s.team == nil || s.users == nil {
		return nil, status.Error(codes.Unimplemented, "no team in this workspace")
	}
	id := req.GetAgentId()
	if !agentIDRE.MatchString(id) {
		return nil, status.Errorf(codes.InvalidArgument, "%q isn't a valid agent id", id)
	}
	argv := req.GetArgv()
	if len(argv) == 0 || argv[0] == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s has no runner", id)
	}
	if p := req.GetIdlePattern(); len(p) > maxIdlePattern {
		return nil, status.Errorf(codes.InvalidArgument, "%s's idle pattern is over %d bytes", id, maxIdlePattern)
	} else if _, err := regexp.Compile(p); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s's idle pattern: %v", id, err)
	}
	seedFrom, err := s.checkSeed(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: %v", id, err)
	}
	if len(req.GetAgentsMd()) > maxAgentsMD {
		return nil, status.Errorf(codes.InvalidArgument, "%s's AGENTS.md is over %d KB", id, maxAgentsMD>>10)
	}
	u, err := s.users.EnsureAgentUser(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%s: %v", id, err)
	}
	base := filepath.Join(s.team.dir, id)
	if u.RunDir != base || u.TmuxDir != filepath.Join(base, "tmux") || u.TmpDir != s.team.TMPDIR(id) {
		return nil, status.Errorf(codes.Internal, "%s: init made its run folder at %q, not %s", id, u.RunDir, base)
	}
	if err := s.team.addAgent(id, u.UID, u.UID); err != nil {
		return nil, status.Errorf(codes.Internal, "%s's socket: %v", id, err)
	}
	fail := func(code codes.Code, format string, a ...any) error {
		s.team.removeAgent(id)
		return status.Errorf(code, format, a...)
	}
	if err := s.writeAsUser(u.UID, u.UID, filepath.Join(u.Home, "AGENTS.md"), []byte(req.GetAgentsMd())); err != nil {
		return nil, fail(codes.Internal, "write %s's AGENTS.md: %v", id, err)
	}
	// Its home_seed, on its first start (ADR 0022 §2).
	if seedFrom != "" {
		if err := s.seedAgentHome(ctx, id, u.UID, seedFrom, req.GetHomeSeed(), req.GetHomeQuotaBytes()); err != nil {
			return nil, fail(status.Code(err), "%s's home_seed couldn't be copied: %s", id, status.Convert(err).Message())
		}
	}
	// Its own clone of the project, made as the agent if it's missing
	// (ADR 0022 §2a): the owner's project is never the agent's to write.
	workDir := ""
	if p := req.GetCloneProjectDir(); p != "" {
		dir, err := s.ensureClone(ctx, id, req.GetAgentName(), u, p, req.GetHomeQuotaBytes())
		if err != nil {
			s.team.removeAgent(id)
			return nil, err
		}
		workDir = dir
	}
	// Each clone readable by exactly the lenses granted on it now.
	if p := req.GetProjectDir(); p != "" {
		if err := s.applyLensGrants(ctx, p, req.GetCloneReaders()); err != nil {
			s.team.removeAgent(id)
			return nil, err
		}
	}
	spec := AgentServerSpec{
		ID: id, UID: u.UID, GID: u.UID, Home: u.Home,
		SocketDir: u.TmuxDir,
		Argv:      argv,
		WorkDir:   workDir,
		Env: []string{
			"HOME=" + u.Home,
			"USER=" + id,
			"LOGNAME=" + id,
			"SHELL=/bin/bash",
			"PATH=/home/" + id + "/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"PORTENV_AGENT=" + id,
			"PORTENV_SOCKET=" + filepath.Join(base, "portenv.sock"),
			"TMPDIR=" + u.TmpDir,
		},
	}
	start := s.startAgentServer
	if start == nil {
		return nil, fail(codes.Unimplemented, "%v", errNoAgentServers)
	}
	tabs, err := start(ctx, spec)
	if err != nil {
		return nil, fail(codes.Internal, "start %s's terminal: %v", id, err)
	}
	// The runner's idle-input pattern, for routines (R-0258): none means
	// the agent gets no routines.
	if p := req.GetIdlePattern(); p != "" && s.setAgentIdleInput != nil {
		if err := s.setAgentIdleInput(id, p); err != nil {
			return nil, status.Errorf(codes.Internal, "set %s's idle pattern: %v", id, err)
		}
	}
	return &agentv1.StartTeamAgentResponse{Tabs: tabs}, nil
}

// StopTeamAgent ends an agent at once (ADR 0022 §8): its tmux server, then
// every process of its user, then its socket. It needs no agent's
// agreement.
func (s *apiServer) StopTeamAgent(ctx context.Context, req *agentv1.StopTeamAgentRequest) (*agentv1.StopTeamAgentResponse, error) {
	if s.team == nil {
		return nil, status.Error(codes.Unimplemented, "no team in this workspace")
	}
	id := req.GetAgentId()
	if !agentIDRE.MatchString(id) {
		return nil, status.Errorf(codes.InvalidArgument, "%q isn't a valid agent id", id)
	}
	s.team.mu.Lock()
	a := s.team.agents[id]
	s.team.mu.Unlock()
	if a == nil {
		return &agentv1.StopTeamAgentResponse{}, nil
	}
	var errs []error
	if s.stopAgentServer != nil {
		if err := s.stopAgentServer(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.killUID(a.uid); err != nil {
		errs = append(errs, err)
	}
	s.team.removeAgent(id)
	if err := errors.Join(errs...); err != nil {
		return nil, status.Errorf(codes.Internal, "stop %s: %v", id, err)
	}
	return &agentv1.StopTeamAgentResponse{}, nil
}

// checkKillableUID allows only agent uids: never root, the helper, work
// or anything outside the agents' range.
func checkKillableUID(uid int) error {
	if uid < AgentUIDFirst || uid > AgentUIDLast {
		return fmt.Errorf("uid %d isn't an agent's", uid)
	}
	return nil
}

// checkSeed checks a start's home_seed before anything is made, and
// returns the project folder (on this filesystem) to copy from, or "" when
// there's nothing to seed.
func (s *apiServer) checkSeed(req *agentv1.StartTeamAgentRequest) (string, error) {
	seeds := req.GetHomeSeed()
	if len(seeds) == 0 {
		return "", nil
	}
	if len(seeds) > maxSeedPaths {
		return "", fmt.Errorf("home_seed lists at most %d paths", maxSeedPaths)
	}
	for _, p := range seeds {
		if _, err := homeRel(p, false); err != nil {
			return "", fmt.Errorf("home_seed %q isn't a plain path in the project", p)
		}
	}
	if req.GetHomeQuotaBytes() <= 0 {
		return "", errors.New("home_seed needs the home's quota")
	}
	prefix := "/home/" + s.cfg.User + "/"
	name, ok := strings.CutPrefix(req.GetProjectDir(), prefix)
	if s.cfg.User == "" || !ok || name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, ".") || path.Clean(req.GetProjectDir()) != req.GetProjectDir() {
		return "", fmt.Errorf("%q isn't a project folder in %s", req.GetProjectDir(), prefix)
	}
	return filepath.Join(s.cfg.HomeRoot, s.cfg.User, name), nil
}

// seedAgentHome runs seed-home as the agent, with the team group, so it
// reads the project only as the agent can.
func (s *apiServer) seedAgentHome(ctx context.Context, id string, uid int, project string, seeds []string, quota int64) error {
	home := (&TeamUsers{Cfg: s.cfg}).home(id)
	args := append([]string{"seed-home", project, home, strconv.FormatInt(quota, 10)}, seeds...)
	var err error
	if s.runHomeIO != nil {
		_, err = s.runHomeIO(ctx, uid, agentGroups(uid, true), args, nil)
	} else {
		_, err = runAsAgentInTeam(ctx, uid, append([]string{"team-io"}, args...), nil, func(w io.Writer) error { return TeamIO(args, nil, w) })
	}
	if err != nil {
		return homeErr(err)
	}
	return nil
}
