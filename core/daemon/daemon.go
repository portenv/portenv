// SPDX-License-Identifier: Apache-2.0

// Package daemon is portenvd: it owns the boxes open on this Mac and serves
// portenv.daemon.v1 to the app and the CLI over a Unix socket only this user
// can reach. Everything it does inside a box goes through the box agent's
// channel (ADR 0010), never docker exec.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/internal/version"
	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// SocketName is the daemon's socket in the Portenv directory.
const SocketName = "portenvd.sock"

// Server is portenvd's API.
type Server struct {
	daemonv1.UnimplementedDaemonServiceServer
	env *local.Env
	log *slog.Logger

	mu   sync.Mutex
	open map[string]*openBox // by box name
}

// openBox is a box this daemon opened: its session, sync engine and agent
// channel.
type openBox struct {
	mu    sync.Mutex // one box operation at a time
	sess  *local.Session
	sb    *boxsync.Box
	conn  *grpc.ClientConn
	agent agentv1.AgentServiceClient
}

// New returns a daemon for this machine's Portenv directory.
func New(env *local.Env, log *slog.Logger) *Server {
	return &Server{env: env, log: log, open: map[string]*openBox{}}
}

// Serve listens on the socket in the Portenv directory (mode 0600 in a 0700
// directory) until ctx ends, then closes every box it opened (save and
// release).
func (s *Server) Serve(ctx context.Context) error {
	sock := filepath.Join(s.env.Dir, SocketName)
	if len(sock) >= 104 {
		return fmt.Errorf("socket path %s is longer than the 103 bytes Unix sockets allow; use a shorter PORTENV_HOME", sock)
	}
	_ = os.Remove(sock)
	lis, err := (&net.ListenConfig{}).Listen(ctx, "unix", sock)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(sock) }()
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	srv := grpc.NewServer()
	daemonv1.RegisterDaemonServiceServer(srv, s)
	go func() {
		<-ctx.Done()
		// Save and release first, while the agent channels are up; then end
		// every call, open terminals included (a graceful stop would wait
		// for terminals forever).
		s.closeAll()
		srv.Stop()
	}()
	s.log.Info("serving", "socket", sock)
	err = srv.Serve(lis)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

func (s *Server) closeAll() {
	s.mu.Lock()
	names := make([]string, 0, len(s.open))
	for n := range s.open {
		names = append(names, n)
	}
	s.mu.Unlock()
	for _, n := range names {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := s.closeBox(ctx, n); err != nil {
			s.log.Error("close on shutdown", "box", n, "err", err)
		}
		cancel()
	}
}

// GetVersion implements daemonv1.DaemonServiceServer.
func (s *Server) GetVersion(context.Context, *daemonv1.GetVersionRequest) (*daemonv1.GetVersionResponse, error) {
	return &daemonv1.GetVersionResponse{Build: &typesv1.BuildInfo{Version: version.Version, Commit: version.Commit}}, nil
}

// ListBoxes lists the boxes configured on this machine.
func (s *Server) ListBoxes(context.Context, *daemonv1.ListBoxesRequest) (*daemonv1.ListBoxesResponse, error) {
	entries, err := os.ReadDir(filepath.Join(s.env.Dir, "boxes"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	resp := &daemonv1.ListBoxesResponse{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		c, err := s.env.LoadBox(name)
		if err != nil {
			continue
		}
		state := typesv1.BoxState_BOX_STATE_STOPPED
		s.mu.Lock()
		if _, open := s.open[name]; open {
			state = typesv1.BoxState_BOX_STATE_RUNNING
		}
		s.mu.Unlock()
		resp.Boxes = append(resp.Boxes, &daemonv1.BoxStatus{Box: &typesv1.Box{Id: c.ID, Name: c.Name, ToolboxImage: c.Image}, State: state})
	}
	return resp, nil
}

// OpenBox applies the resume rules and starts the box on this Mac.
func (s *Server) OpenBox(ctx context.Context, req *daemonv1.OpenBoxRequest) (*daemonv1.OpenBoxResponse, error) {
	s.mu.Lock()
	if _, open := s.open[req.GetName()]; open {
		s.mu.Unlock()
		return &daemonv1.OpenBoxResponse{Summary: "already open"}, nil
	}
	s.mu.Unlock()
	ob, res, err := s.openBox(ctx, req.GetName())
	if err != nil {
		var held *boxsync.LeaseHeldError
		if errors.As(err, &held) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, err
	}
	s.mu.Lock()
	s.open[req.GetName()] = ob
	s.mu.Unlock()
	summary := fmt.Sprintf("open on %s (resume rule %d: %s)", s.env.Machine, res.Rule, res.Action)
	if res.Offline {
		summary = "Offline · will save later"
	}
	return &daemonv1.OpenBoxResponse{Summary: summary, Rule: int32(res.Rule)}, nil // #nosec G115 -- rules 1 to 5
}

func (s *Server) openBox(ctx context.Context, name string) (*openBox, boxsync.ResumeResult, error) {
	var none boxsync.ResumeResult
	sess, err := s.env.Open(name)
	if err != nil {
		return nil, none, err
	}
	if err := sess.EnsureCreated(ctx, func(msg string) { s.log.Info(msg, "box", name) }); err != nil {
		return nil, none, err
	}
	reach := make(chan bool, 1)
	go func() { reach <- sess.StorageReachable() }()

	// A box left running without this process's channel secrets (for
	// example after portenvd restarted) is restarted to open a new channel.
	if _, err := sess.Drv.AgentChannel(ctx, sess.ID()); errors.Is(err, driver.ErrNoChannel) {
		if _, err := sess.Drv.Stop(ctx, sess.ID(), 0); err != nil {
			return nil, none, err
		}
	}
	if _, err := sess.Drv.Start(ctx, sess.ID()); err != nil {
		return nil, none, err
	}
	ob := &openBox{sess: sess}
	// An empty home makes the agent report FAILED; restic still runs.
	if _, err := ob.connect(ctx, true); err != nil {
		return nil, none, agentError(err)
	}
	if ob.sb, err = sess.SyncWith(boxsync.ChannelExecutor{Agent: ob.agent}); err != nil {
		ob.stop(ctx)
		return nil, none, err
	}
	var res boxsync.ResumeResult
	if !<-reach {
		if res, err = ob.sb.ResumeOffline(); err != nil {
			ob.stop(ctx)
			return nil, none, fmt.Errorf("storage is unreachable and %w", err)
		}
	} else {
		if err := ob.sb.Init(ctx); err != nil {
			ob.stop(ctx)
			return nil, none, err
		}
		if res, err = ob.sb.Resume(ctx, boxsync.ResumeOptions{}); err != nil {
			ob.stop(ctx)
			return nil, none, err
		}
	}
	switch res.Action {
	case boxsync.ActionNewBox, boxsync.ActionRestored, boxsync.ActionRestoredKeptLocal:
		// The start sequence runs again on the new home: a fresh one from
		// the skeleton, or the one just restored.
		ob.closeConn()
		if err := sess.Restart(ctx, res.Action == boxsync.ActionNewBox); err != nil {
			return nil, none, err
		}
	}
	if state, err := ob.connect(ctx, false); err != nil {
		return nil, none, err
	} else if state != agentv1.ReadinessState_READINESS_STATE_READY {
		return nil, none, errors.New("the box failed to start")
	}
	if ob.sb, err = sess.SyncWith(boxsync.ChannelExecutor{Agent: ob.agent}); err != nil {
		return nil, none, err
	}
	return ob, res, nil
}

// connect opens the agent channel for the box's current start and waits
// until the agent reports READY (or FAILED, when allowFailed is set).
func (ob *openBox) connect(ctx context.Context, allowFailed bool) (agentv1.ReadinessState, error) {
	ob.closeConn()
	ch, err := ob.sess.Drv.AgentChannel(ctx, ob.sess.ID())
	if err != nil {
		return 0, err
	}
	conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		return 0, err
	}
	ob.conn, ob.agent = conn, agentv1.NewAgentServiceClient(conn)
	deadline := time.Now().Add(15 * time.Minute)
	wait := 50 * time.Millisecond
	for time.Now().Before(deadline) {
		r, err := ob.agent.GetReadiness(ctx, &agentv1.GetReadinessRequest{})
		if err != nil && strings.Contains(err.Error(), "the certificate is not this box's") {
			return 0, err // something else answers on the agent's port: never retry into it
		}
		if err == nil {
			switch r.GetState() {
			case agentv1.ReadinessState_READINESS_STATE_READY:
				return r.GetState(), nil
			case agentv1.ReadinessState_READINESS_STATE_FAILED:
				if allowFailed {
					return r.GetState(), nil
				}
				return r.GetState(), fmt.Errorf("the box failed to start: %s", r.GetDetail())
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Second)
	}
	return 0, errors.New("the box did not finish starting within 15 minutes")
}

func (ob *openBox) closeConn() {
	if ob.conn != nil {
		_ = ob.conn.Close()
		ob.conn, ob.agent = nil, nil
	}
}

func (ob *openBox) stop(ctx context.Context) {
	ob.closeConn()
	_, _ = ob.sess.Drv.Stop(ctx, ob.sess.ID(), 0)
}

func (s *Server) get(name string) (*openBox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ob, ok := s.open[name]
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is not open on this Mac", name)
	}
	return ob, nil
}

// CloseBox saves, releases and stops the box.
func (s *Server) CloseBox(ctx context.Context, req *daemonv1.CloseBoxRequest) (*daemonv1.CloseBoxResponse, error) {
	if err := s.closeBox(ctx, req.GetName()); err != nil {
		return nil, err
	}
	return &daemonv1.CloseBoxResponse{}, nil
}

func (s *Server) closeBox(ctx context.Context, name string) error {
	ob, err := s.get(name)
	if err != nil {
		return err
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if _, err := ob.sb.Save(ctx, boxsync.SaveOptions{Kind: boxsync.SaveRelease}); err != nil {
		return agentError(err)
	}
	ob.stop(ctx)
	s.mu.Lock()
	delete(s.open, name)
	s.mu.Unlock()
	return nil
}

// MakeSavePoint saves the home as a save point.
func (s *Server) MakeSavePoint(ctx context.Context, req *daemonv1.MakeSavePointRequest) (*daemonv1.MakeSavePointResponse, error) {
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	snap, err := ob.sb.Save(ctx, boxsync.SaveOptions{Kind: boxsync.SavePoint})
	if err != nil {
		return nil, agentError(err)
	}
	return &daemonv1.MakeSavePointResponse{Snapshot: ref(ob.sess.Cfg.ID, snap)}, nil
}

// RevertToLastSavePoint saves the home, then restores the newest save
// point. Terminals keep running; their files change underneath them.
func (s *Server) RevertToLastSavePoint(ctx context.Context, req *daemonv1.RevertToLastSavePointRequest) (*daemonv1.RevertToLastSavePointResponse, error) {
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	res, err := ob.sb.RevertToLastSavePoint(ctx)
	if errors.Is(err, boxsync.ErrNoSavePoint) {
		return nil, status.Error(codes.FailedPrecondition, "there is no save point yet")
	}
	if err != nil {
		return nil, agentError(err)
	}
	return &daemonv1.RevertToLastSavePointResponse{Restored: ref(ob.sess.Cfg.ID, res.Restored), SavedBefore: ref(ob.sess.Cfg.ID, res.SavedBefore)}, nil
}

// MoveBox closes the box where it is open and opens it on the target:
// "this-mac", or a server from the machine's servers list.
func (s *Server) MoveBox(ctx context.Context, req *daemonv1.MoveBoxRequest) (*daemonv1.MoveBoxResponse, error) {
	name, target := req.GetName(), req.GetTarget()
	mc, err := s.env.MachineConfig()
	if err != nil {
		return nil, err
	}
	out := logWriter{s.log.With("box", name, "target", target)}
	if target == "this-mac" {
		// Close it on whichever server has it open; a server where it is
		// not open refuses, which is fine.
		for _, srv := range mc.Servers {
			if local.KnownOn(ctx, srv, name) {
				_ = local.CloseOn(ctx, srv, name, out)
			}
		}
		res, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: name})
		if err != nil {
			return nil, err
		}
		return &daemonv1.MoveBoxResponse{Summary: res.GetSummary()}, nil
	}
	if !slices.Contains(mc.Servers, target) {
		return nil, status.Errorf(codes.InvalidArgument, "%s is not in this Mac's servers list", target)
	}
	c, err := s.env.LoadBox(name)
	if err != nil {
		return nil, err
	}
	if _, err := s.get(name); err == nil {
		if err := s.closeBox(ctx, name); err != nil {
			return nil, err
		}
	}
	if !local.KnownOn(ctx, target, name) {
		if err := local.EnrolOn(ctx, s.env, c, target, joinStorage(c, target), out); err != nil {
			return nil, err
		}
	}
	if err := local.ResumeOn(ctx, target, name, out); err != nil {
		return nil, err
	}
	return &daemonv1.MoveBoxResponse{Summary: "open on " + target}, nil
}

// joinStorage is where a server reaches the box's storage: itself, through
// host.portenv.internal, when the storage is on that server.
func joinStorage(c local.BoxConfig, target string) string {
	_, host, _ := strings.Cut(target, "@")
	if host == "" {
		host = target
	}
	if strings.HasPrefix(c.Storage, "sftp:") && strings.Contains(c.Storage, "@"+host+":") {
		return strings.Replace(c.Storage, "@"+host+":", "@host.portenv.internal:", 1)
	}
	return c.Storage
}

// Terminal relays a terminal to the box agent's Terminal call.
func (s *Server) Terminal(stream daemonv1.DaemonService_TerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "the first terminal message must be open")
	}
	ob, err := s.get(open.GetBox())
	if err != nil {
		return err
	}
	at, err := ob.agent.Terminal(stream.Context())
	if err != nil {
		return agentError(err)
	}
	size := &agentv1.TerminalSize{Cols: open.GetSize().GetCols(), Rows: open.GetSize().GetRows()}
	if err := at.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Open{Open: &agentv1.TerminalOpen{Session: open.GetSession(), Size: size}}}); err != nil {
		return err
	}
	go func() {
		for {
			r, err := stream.Recv()
			if err != nil {
				_ = at.CloseSend()
				return
			}
			var m agentv1.TerminalRequest
			switch v := r.GetMsg().(type) {
			case *daemonv1.TerminalRequest_Input:
				m.Msg = &agentv1.TerminalRequest_Input{Input: v.Input}
			case *daemonv1.TerminalRequest_Resize:
				m.Msg = &agentv1.TerminalRequest_Resize{Resize: &agentv1.TerminalSize{Cols: v.Resize.GetCols(), Rows: v.Resize.GetRows()}}
			default:
				continue
			}
			if err := at.Send(&m); err != nil {
				return
			}
		}
	}()
	for {
		r, err := at.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return agentError(err)
		}
		var m daemonv1.TerminalResponse
		switch v := r.GetMsg().(type) {
		case *agentv1.TerminalResponse_Output:
			m.Msg = &daemonv1.TerminalResponse_Output{Output: v.Output}
		case *agentv1.TerminalResponse_ExitCode:
			m.Msg = &daemonv1.TerminalResponse_ExitCode{ExitCode: v.ExitCode}
		}
		if err := stream.Send(&m); err != nil {
			return err
		}
	}
}

func ref(boxID string, s boxsync.Snapshot) *typesv1.SnapshotRef {
	return &typesv1.SnapshotRef{Id: s.ID, BoxId: boxID, Time: timestamppb.New(s.Time), MachineId: s.Machine}
}

// logWriter turns remote command output into log lines.
type logWriter struct{ log *slog.Logger }

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.log.Info(line)
		}
	}
	return len(p), nil
}

// agentError reports a channel that is down or fails verification (for
// example something else answering on the agent's port) as the box agent
// being unavailable: nothing was sent to it.
func agentError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if status.Code(err) == codes.Unavailable || strings.Contains(msg, "the certificate is not this box's") {
		return status.Error(codes.Unavailable, "the box agent is unavailable (its channel is down or failed verification); restart the box")
	}
	return err
}

// CheckBox reports whether the box agent answers on its channel.
func (s *Server) CheckBox(ctx context.Context, req *daemonv1.CheckBoxRequest) (*daemonv1.CheckBoxResponse, error) {
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	if ob.agent == nil {
		return &daemonv1.CheckBoxResponse{Detail: "the box agent is unavailable"}, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := ob.agent.GetReadiness(cctx, &agentv1.GetReadinessRequest{}); err != nil {
		return &daemonv1.CheckBoxResponse{Detail: status.Convert(agentError(err)).Message()}, nil
	}
	return &daemonv1.CheckBoxResponse{AgentAvailable: true}, nil
}

// RestartBox stops the box without saving (with its agent gone, nothing in
// it can be saved) and opens it again through the resume rules. Its home
// stays on its volume and this machine's state still says it has unsaved
// changes, so nothing is restored over it (rule 3 on this machine); the
// next save includes everything.
func (s *Server) RestartBox(ctx context.Context, req *daemonv1.RestartBoxRequest) (*daemonv1.RestartBoxResponse, error) {
	name := req.GetName()
	ob, err := s.get(name)
	if err != nil {
		return nil, err
	}
	ob.mu.Lock()
	ob.stop(ctx)
	s.mu.Lock()
	delete(s.open, name)
	s.mu.Unlock()
	ob.mu.Unlock()
	res, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return &daemonv1.RestartBoxResponse{Summary: res.GetSummary(), Rule: res.GetRule()}, nil
}
