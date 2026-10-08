// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The agent channel (ADR 0010). The driver writes these before the box
// starts; they live on the fresh root file system, root-only.
const (
	ChannelDir  = "/run/portenv/agent"
	ChannelPort = 7700
	TokenFile   = "token"
	CertFile    = "cert.pem"
	KeyFile     = "key.pem"
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
	cfg   Config
	token []byte
}

// ServeChannel serves the agent API on the channel described by dir until
// ctx ends. The caller must already be non-dumpable (passwords pass
// through this process).
func ServeChannel(ctx context.Context, cfg Config, dir string, port int) error {
	srv, err := newChannelServer(cfg, dir)
	if err != nil {
		return err
	}
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	return serveOn(ctx, srv, lis)
}

// newChannelServer loads the channel's token, key and certificate from dir
// into memory, then removes the files and dir: after this, nothing in the
// box can read them (ADR 0010, conditions).
func newChannelServer(cfg Config, dir string) (*grpc.Server, error) {
	files := map[string][]byte{}
	for _, name := range []string{TokenFile, CertFile, KeyFile} {
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- fixed root-only path
		if err != nil {
			_ = removeChannelFiles(dir)
			return nil, fmt.Errorf("read channel %s: %w", name, err)
		}
		files[name] = data
	}
	if err := removeChannelFiles(dir); err != nil {
		return nil, fmt.Errorf("remove channel files: %w", err)
	}
	token := bytes.TrimSpace(files[TokenFile])
	if len(token) < 32 {
		return nil, errors.New("channel token is too short")
	}
	cert, err := tls.X509KeyPair(files[CertFile], files[KeyFile])
	if err != nil {
		return nil, fmt.Errorf("load channel certificate: %w", err)
	}
	s := &apiServer{cfg: cfg, token: token}
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})),
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
	return srv, nil
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
		if subtle.ConstantTimeCompare([]byte(v), s.token) == 1 {
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
	state, detail, err := CheckReadiness(ctx, DefaultSocket)
	if err != nil {
		return &agentv1.GetReadinessResponse{State: agentv1.ReadinessState_READINESS_STATE_STARTING, Detail: "starting"}, nil
	}
	return &agentv1.GetReadinessResponse{State: state, Detail: detail}, nil
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

func (s *apiServer) GetPathInfo(_ context.Context, req *agentv1.GetPathInfoRequest) (*agentv1.GetPathInfoResponse, error) {
	info, err := PathInfo(req.GetPath())
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

// removeChannelFiles deletes the channel's files and their directory.
func removeChannelFiles(dir string) error {
	var first error
	for _, name := range []string{TokenFile, CertFile, KeyFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
		first = err
	}
	return first
}

// WaitChannelLoaded waits until the API process has read and removed the
// channel files, so init starts no other process while they exist. On
// timeout it removes them itself (the channel is then unavailable) and
// reports it.
func WaitChannelLoaded(dir string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, TokenFile)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = removeChannelFiles(dir)
	return errors.New("the agent channel did not load its secrets in time; removed them")
}
