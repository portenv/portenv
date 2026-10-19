// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The agent channel (ADR 0010). The driver hands each start's secrets to
// the box on its stdin (attach, never exec, never a file); init passes that
// stdin on to the API process, which reads them.
const (
	ChannelPort = 7700
	// ChannelEnv set to "stdin" tells init that the secrets come on stdin.
	ChannelEnv = "PORTENV_CHANNEL"
	// BoxIDEnv is the box's ID, set by the driver when it makes the
	// container (R-0172, R-0173). It's an environment variable rather than
	// part of the secrets line because helpers in older images refuse a
	// secrets line with a field they don't know (DisallowUnknownFields), so
	// a new field there would stop older boxes from starting; an unknown
	// variable is ignored. A container is made afresh for every start, so
	// it never goes stale.
	BoxIDEnv = "PORTENV_BOX_ID"
)

// The API process runs as portenv-agent from a copy of the agent with file
// capabilities, which makes it non-dumpable from its first instruction.
const (
	ServeBinary = "/usr/local/libexec/portenv/agent-serve"
	ServeUID    = 991
)

// TokenHeader carries the per-start token on every call.
const TokenHeader = "portenv-agent-token" // #nosec G101 -- a header name, not a credential

// sessionRE is a tmux session name the terminal call accepts.
var sessionRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// apiServer is the agent API on the agent channel, served by
// portenv-agent serve.
type apiServer struct {
	agentv1.UnimplementedAgentServiceServer
	cfg  Config
	keys *channelKeys
	// tmux runs tmux as the main user (tmuxAs; a fake in tests).
	tmux func(Config) (tmuxFunc, error)
	// tmuxCmd builds a long-running tmux client as the main user
	// (tmuxCmdAs), for the control-mode clients.
	tmuxCmd func(Config) (tmuxCmdFunc, error)

	hubMu sync.Mutex
	hub   *ctlHub

	// team holds the agents' sockets and portenvd's TeamCalls stream
	// (ADR 0022 §4); nil where there's no team (tests).
	team *teamHub
	// users asks init for agent users; writeAsUser writes a file as an
	// agent; killUID ends every process of an agent's uid.
	users       agentUsers
	writeAsUser func(uid, gid int, path string, data []byte) error
	killUID     func(uid int) error
	// startAgentServer and stopAgentServer run one agent's own tmux server
	// (lane A's terminal code; nil until a build has them).
	startAgentServer func(ctx context.Context, spec AgentServerSpec) ([]*typesv1.Tab, error)
	stopAgentServer  func(ctx context.Context, id string) error
	// agents are the running agents' own tmux servers; agentTmux reaches
	// one as its agent (agentAs; plain tmux in tests).
	agents    agentServers
	agentTmux agentTmuxFunc
	// runTeamIO runs "portenv-agent team-io" as work (teamIOAs; in this
	// process in tests).
	runTeamIO func(ctx context.Context, args []string, stdin io.Reader) ([]byte, error)
	// runHomeIO runs team-io as an agent, for its home's files (nil: as
	// the agent's uid, runAsAgent).
	runHomeIO func(ctx context.Context, uid int, args []string, stdin io.Reader) ([]byte, error)
	// asAgentIO runs team-io as an agent through as-agent's switch, for
	// its clone (nil: runTeamIOAsAgent; tests replace it).
	asAgentIO func(ctx context.Context, uid int, home string, args []string, stdin io.Reader) ([]byte, error)
	// asAgentStream starts team-io as an agent with its output on a
	// reader, for its branch's bundle (nil: streamTeamIOAsAgent).
	asAgentStream func(ctx context.Context, uid int, home string, args []string) (io.ReadCloser, func() error, error)
	// setAgentIdleInput gives lane A's idle decision a running agent's
	// idle-input pattern (SetAgentIdleInput; nil in tests).
	setAgentIdleInput func(id, pattern string) error
	// prepareProject asks init to open the project to the team.
	prepareProject func(ctx context.Context, projectDir string) ([]string, error)
	// now is the clock (nil: time.Now).
	now func() time.Time
}

// ctlHub returns the server's control-mode clients, made on first use.
func (s *apiServer) ctlHub() (*ctlHub, error) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if s.hub != nil {
		return s.hub, nil
	}
	if s.tmuxCmd == nil {
		return nil, errors.New("no control-mode client here")
	}
	run, err := s.tmux(s.cfg)
	if err != nil {
		return nil, err
	}
	cmd, err := s.tmuxCmd(s.cfg)
	if err != nil {
		return nil, err
	}
	s.hub = newCtlHub(run, cmd)
	return s.hub, nil
}

// newChannelServer serves the API with the channel's current secrets.
func newChannelServer(cfg Config, keys *channelKeys) *grpc.Server {
	s := &apiServer{cfg: cfg, keys: keys, tmux: tmuxAs, tmuxCmd: tmuxCmdAs, team: newTeamHub(TeamRunDir),
		users: initUsers{socket: DefaultSocket}, writeAsUser: writeFileAsUser, killUID: killAgentUID, prepareProject: askInitPrepare(DefaultSocket)}
	s.agentTmux = agentAs
	s.startAgentServer, s.stopAgentServer = s.StartAgentTerminals, s.StopAgentTerminals
	s.setAgentIdleInput = s.SetAgentIdleInput
	s.runTeamIO = s.teamIOAs
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(keys.tlsConfig())),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if err := s.authorize(ctx); err != nil {
				return nil, err
			}
			return h(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			if err := s.authorize(ss.Context()); err != nil {
				return err
			}
			return h(srv, ss)
		}),
	)
	agentv1.RegisterAgentServiceServer(srv, s)
	return srv
}

func serveOn(ctx context.Context, srv *grpc.Server, lis net.Listener) error {
	go func() { <-ctx.Done(); srv.Stop() }()
	if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

func (s *apiServer) authorize(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get(TokenHeader) {
		if subtle.ConstantTimeCompare([]byte(v), s.keys.token()) == 1 {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "missing or wrong channel token")
}

func (s *apiServer) GetVersion(context.Context, *agentv1.GetVersionRequest) (*agentv1.GetVersionResponse, error) {
	return (&server{}).GetVersion(context.Background(), nil)
}

// GetReadiness asks the init process, which owns the start sequence.
func (s *apiServer) GetReadiness(ctx context.Context, _ *agentv1.GetReadinessRequest) (*agentv1.GetReadinessResponse, error) {
	resp, err := AskReadiness(ctx, DefaultSocket)
	if err != nil {
		return &agentv1.GetReadinessResponse{State: agentv1.ReadinessState_READINESS_STATE_STARTING, Detail: "starting"}, nil
	}
	return resp, nil
}

// RetryPackages asks the init process (root, which runs apt-get) to retry
// the packages that couldn't be installed.
func (s *apiServer) RetryPackages(ctx context.Context, _ *agentv1.RetryPackagesRequest) (*agentv1.RetryPackagesResponse, error) {
	if err := AskRetryPackages(ctx, DefaultSocket); err != nil {
		return nil, status.Errorf(codes.Unavailable, "retry packages: %v", err)
	}
	return &agentv1.RetryPackagesResponse{}, nil
}

// RunRestic runs one restic command exactly as portenv-agent restic does.
func (s *apiServer) RunRestic(ctx context.Context, req *agentv1.RunResticRequest) (*agentv1.RunResticResponse, error) {
	var out, errb bytes.Buffer
	code, err := RunRestic(ctx, req.GetArgs(), bytes.NewReader(req.GetInput()), &out, &errb)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &agentv1.RunResticResponse{Stdout: out.Bytes(), Stderr: errb.Bytes(), ExitCode: int32(code)}, nil // #nosec G115 -- exit codes fit
}

func (s *apiServer) GetPathInfo(ctx context.Context, req *agentv1.GetPathInfoRequest) (*agentv1.GetPathInfoResponse, error) {
	info, err := pathInfoAs(ctx, s.cfg, req.GetPath())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &agentv1.GetPathInfoResponse{Exists: info["exists"], IsDir: info["is_dir"], Empty: info["empty"]}, nil
}

func (s *apiServer) Terminal(stream agentv1.AgentService_TerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "the first terminal message must be open")
	}
	if !sessionRE.MatchString(open.GetSession()) {
		return status.Error(codes.InvalidArgument, "session names are 1 to 32 letters, digits, - or _")
	}
	if v := open.GetViewer(); v != "" && !viewerRE.MatchString(v) {
		return status.Error(codes.InvalidArgument, "viewer names are 1 to 16 lower-case letters, digits or -")
	}
	return runTerminal(stream, s.cfg, open)
}

// WaitChannelListening waits until the API process listens on its port,
// which it does only after reading the secrets: init starts no other process
// before then.
func WaitChannelListening(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("the agent channel did not start in time")
}
