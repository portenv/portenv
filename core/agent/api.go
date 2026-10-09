// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The agent channel (ADR 0010). The driver hands each start's secrets to
// the box on its stdin (attach, never exec, never a file); init passes that
// stdin on to the API process, which reads them.
const (
	ChannelPort = 7700
	// ChannelEnv set to "stdin" tells init that the secrets come on stdin.
	ChannelEnv = "PORTENV_CHANNEL"
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
}

// newChannelServer serves the API with the channel's current secrets.
func newChannelServer(cfg Config, keys *channelKeys) *grpc.Server {
	s := &apiServer{cfg: cfg, keys: keys}
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
