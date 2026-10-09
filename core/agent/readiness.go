// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/portenv/portenv/core/internal/version"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// DefaultSocket is where the agent serves portenv.agent.v1 inside the box.
// Its directory is root-only.
const DefaultSocket = "/run/portenv/agent.sock"

// Readiness tracks the box's start sequence. It is safe for concurrent use.
type Readiness struct {
	mu     sync.Mutex
	state  agentv1.ReadinessState
	detail string
	// Packages from apt-packages.txt that couldn't be installed, and the
	// last error: they never stop the box (PLAN.md 1.4).
	failed  []string
	pkgErr  string
	retryCh chan struct{}
}

// NewReadiness returns a Readiness in the starting state.
func NewReadiness() *Readiness {
	return &Readiness{state: agentv1.ReadinessState_READINESS_STATE_STARTING, detail: "starting", retryCh: make(chan struct{}, 1)}
}

// SetPackages records the packages that couldn't be installed (none clears).
func (r *Readiness) SetPackages(failed []string, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed, r.pkgErr = failed, errMsg
	if len(failed) == 0 {
		r.pkgErr = ""
	}
}

// Packages returns the packages that couldn't be installed and the error.
func (r *Readiness) Packages() ([]string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.failed...), r.pkgErr
}

// RequestRetry asks the background loop to retry the failed packages now.
func (r *Readiness) RequestRetry() {
	select {
	case r.retryCh <- struct{}{}:
	default: // one already pending
	}
}

// Retries delivers RequestRetry's requests.
func (r *Readiness) Retries() <-chan struct{} { return r.retryCh }

// Step records the step now running. A ready box stays ready: a background
// package retry never makes it "starting" again.
func (r *Readiness) Step(detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == agentv1.ReadinessState_READINESS_STATE_READY {
		return
	}
	r.state, r.detail = agentv1.ReadinessState_READINESS_STATE_STARTING, detail
}

// Ready marks the box usable.
func (r *Readiness) Ready() {
	r.set(agentv1.ReadinessState_READINESS_STATE_READY, "ready")
}

// Fail marks the start sequence failed.
func (r *Readiness) Fail(err error) {
	r.set(agentv1.ReadinessState_READINESS_STATE_FAILED, err.Error())
}

// Get returns the current state and detail.
func (r *Readiness) Get() (agentv1.ReadinessState, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.detail
}

func (r *Readiness) set(s agentv1.ReadinessState, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state, r.detail = s, detail
}

// server implements portenv.agent.v1.AgentService.
type server struct {
	agentv1.UnimplementedAgentServiceServer
	readiness *Readiness
}

func (s *server) GetVersion(context.Context, *agentv1.GetVersionRequest) (*agentv1.GetVersionResponse, error) {
	return &agentv1.GetVersionResponse{
		Build: &typesv1.BuildInfo{Version: version.Version, Commit: version.Commit},
	}, nil
}

func (s *server) GetReadiness(context.Context, *agentv1.GetReadinessRequest) (*agentv1.GetReadinessResponse, error) {
	state, detail := s.readiness.Get()
	failed, msg := s.readiness.Packages()
	return &agentv1.GetReadinessResponse{State: state, Detail: detail, FailedPackages: failed, PackagesError: msg}, nil
}

func (s *server) RetryPackages(context.Context, *agentv1.RetryPackagesRequest) (*agentv1.RetryPackagesResponse, error) {
	s.readiness.RequestRetry()
	return &agentv1.RetryPackagesResponse{}, nil
}

// Serve serves the agent API on a Unix socket until ctx is done. The socket's
// directory is created root-only (0700) and the socket itself is 0600.
func Serve(ctx context.Context, socket string, r *Readiness) error {
	// root:portenv-agent 0710: the API process (portenv-agent) reaches the
	// socket; nothing else in the box does.
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o710); err != nil {
		return fmt.Errorf("agent socket dir: %w", err)
	}
	asRoot := os.Getuid() == 0 // init in a box; tests run unprivileged
	if asRoot {
		if err := os.Chown(dir, 0, ServeUID); err != nil {
			return fmt.Errorf("agent socket dir owner: %w", err)
		}
	}
	if err := os.Chmod(dir, 0o710); err != nil { // #nosec G302 -- a directory: owner and group search only
		return fmt.Errorf("agent socket dir permissions: %w", err)
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale agent socket: %w", err)
	}
	lis, err := (&net.ListenConfig{}).Listen(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("listen on agent socket: %w", err)
	}
	if asRoot {
		if err := os.Chown(socket, 0, ServeUID); err != nil {
			_ = lis.Close()
			return fmt.Errorf("agent socket owner: %w", err)
		}
	}
	if err := os.Chmod(socket, 0o660); err != nil { // #nosec G302 -- root and portenv-agent only
		_ = lis.Close()
		return fmt.Errorf("agent socket permissions: %w", err)
	}
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, &server{readiness: r})
	go func() {
		<-ctx.Done()
		srv.Stop()
	}()
	if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("serve agent API: %w", err)
	}
	return nil
}

// CheckReadiness asks the agent on socket for its readiness.
func CheckReadiness(ctx context.Context, socket string) (agentv1.ReadinessState, string, error) {
	resp, err := AskReadiness(ctx, socket)
	if err != nil {
		return 0, "", err
	}
	return resp.GetState(), resp.GetDetail(), nil
}

// AskReadiness asks the init process for its readiness, with the packages that
// couldn't be installed.
func AskReadiness(ctx context.Context, socket string) (*agentv1.GetReadinessResponse, error) {
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to agent: %w", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := agentv1.NewAgentServiceClient(conn).GetReadiness(ctx, &agentv1.GetReadinessRequest{})
	if err != nil {
		return nil, fmt.Errorf("ask agent for readiness: %w", err)
	}
	return resp, nil
}

// AskRetryPackages asks the init process to retry the failed packages now.
func AskRetryPackages(ctx context.Context, socket string) error {
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("connect to agent: %w", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = agentv1.NewAgentServiceClient(conn).RetryPackages(ctx, &agentv1.RetryPackagesRequest{})
	return err
}
