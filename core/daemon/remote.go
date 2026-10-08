// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// A box can run on one of this Mac's servers (Move To). There its runner
// (portenv-runner, the same daemon code) opens it with the agent channel
// exactly as portenvd does here; this Mac fetches the channel's address,
// certificate and token from the runner over SSH (kept in memory only) and
// reaches the agent's loopback port through an SSH forward over the same
// connection. Save points, Revert To, checks and restarts run on the server
// through its runner, which holds the box's lease.

// remoteBox is a box open on a server.
type remoteBox struct {
	host  string
	conn  *grpc.ClientConn
	agent agentv1.AgentServiceClient
}

// locationFile records where a box runs (only the host, no secret), so a
// restarted portenvd still knows.
func (s *Server) locationFile(name string) (string, error) {
	c, err := s.env.LoadBox(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.env.Dir, "state", c.ID, "location"), nil
}

func (s *Server) setLocation(name, host string) {
	p, err := s.locationFile(name)
	if err != nil {
		return
	}
	if host == "" {
		_ = os.Remove(p)
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(host+"\n"), 0o600) // #nosec G703 -- under this machine's Portenv directory
}

func (s *Server) location(name string) string {
	s.mu.Lock()
	if rb, ok := s.remote[name]; ok {
		s.mu.Unlock()
		return rb.host
	}
	s.mu.Unlock()
	p, err := s.locationFile(name)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p) // #nosec G304 -- under this machine's Portenv directory
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// remoteState asks the server's runner for the box's state.
func remoteState(ctx context.Context, host, name string) (*daemonv1.GetBoxStateResponse, error) {
	out, err := local.AppOn(ctx, host, "state", name)
	if err != nil {
		return nil, err
	}
	var st struct {
		State   string    `json:"state"`
		SavedAt time.Time `json:"saved_at"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, fmt.Errorf("state from %s: %w", host, err)
	}
	r := &daemonv1.GetBoxStateResponse{State: daemonv1.SaveState(daemonv1.SaveState_value[st.State]), Location: host}
	if !st.SavedAt.IsZero() {
		r.SavedAt = timestamppb.New(st.SavedAt)
	}
	return r, nil
}

// attachRemote records a box open on host.
func (s *Server) attachRemote(name, host string) {
	s.mu.Lock()
	if old, ok := s.remote[name]; ok && old.conn != nil {
		_ = old.conn.Close()
	}
	s.remote[name] = &remoteBox{host: host}
	s.mu.Unlock()
	s.setLocation(name, host)
}

func (s *Server) detachRemote(name string) {
	s.mu.Lock()
	if rb, ok := s.remote[name]; ok && rb.conn != nil {
		_ = rb.conn.Close()
	}
	delete(s.remote, name)
	s.mu.Unlock()
	s.setLocation(name, "")
}

// remoteAgent connects to a server box's agent: the channel's secrets from
// the runner over SSH, the agent's port through an SSH forward.
func (s *Server) remoteAgent(ctx context.Context, name string) (agentv1.AgentServiceClient, error) {
	s.mu.Lock()
	rb, ok := s.remote[name]
	s.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is not open on a server", name)
	}
	if rb.agent != nil {
		return rb.agent, nil
	}
	out, err := local.AppOn(ctx, rb.host, "channel", name)
	if err != nil {
		return nil, agentUnavailable(err)
	}
	var ch struct {
		Address string `json:"address"`
		CertPEM []byte `json:"cert_pem"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &ch); err != nil || ch.Address == "" {
		return nil, agentUnavailable(fmt.Errorf("channel from %s: unreadable", rb.host))
	}
	sock, err := local.ForwardLocal(ctx, rb.host, ch.Address)
	if err != nil {
		return nil, agentUnavailable(err)
	}
	conn, err := agent.DialChannel(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", sock)
	}, ch.CertPEM, ch.Token)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	rb.conn, rb.agent = conn, agentv1.NewAgentServiceClient(conn)
	s.mu.Unlock()
	return rb.agent, nil
}

// dropRemoteAgent forgets a server box's channel (for example after the
// box restarted there with new secrets).
func (s *Server) dropRemoteAgent(name string) {
	s.mu.Lock()
	if rb, ok := s.remote[name]; ok && rb.conn != nil {
		_ = rb.conn.Close()
		rb.conn, rb.agent = nil, nil
	}
	s.mu.Unlock()
}

func agentUnavailable(err error) error {
	return status.Error(codes.Unavailable, "the box agent is unavailable ("+err.Error()+"); restart the box")
}

// GetChannel returns the channel of a box open here, for the owner of this
// socket only (on a server: the Mac, over SSH, as the runner's own user).
// The secrets are never logged.
func (s *Server) GetChannel(ctx context.Context, req *daemonv1.GetChannelRequest) (*daemonv1.GetChannelResponse, error) {
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	ch, err := ob.sess.Drv.AgentChannel(ctx, ob.sess.ID())
	if err != nil {
		return nil, agentError(status.Error(codes.Unavailable, err.Error()))
	}
	return &daemonv1.GetChannelResponse{Address: ch.Addr, CertPem: ch.CertPEM, Token: ch.Token}, nil
}

// saveState derives the title's save state from what is recorded only:
// the sync state (last saved snapshot and its time) and what this daemon
// knows is happening (a save running, storage unreachable, the agent's
// channel down). Opening a box successfully says nothing about saves.
func saveState(st boxsync.State, saving, offline, agentDown, retrying, failed, quitUnsaved bool) (daemonv1.SaveState, time.Time) {
	switch {
	case agentDown:
		return daemonv1.SaveState_SAVE_STATE_AGENT_UNAVAILABLE, st.SavedAt
	case retrying:
		return daemonv1.SaveState_SAVE_STATE_RETRYING, st.SavedAt
	case quitUnsaved:
		// Until the save made first thing on open completes.
		return daemonv1.SaveState_SAVE_STATE_QUIT_UNSAVED, st.SavedAt
	case saving:
		return daemonv1.SaveState_SAVE_STATE_SAVING, st.SavedAt
	case offline:
		return daemonv1.SaveState_SAVE_STATE_OFFLINE, st.SavedAt
	case failed:
		return daemonv1.SaveState_SAVE_STATE_NOT_SAVED, st.SavedAt
	case st.Snapshot == "" && st.Tree == "":
		return daemonv1.SaveState_SAVE_STATE_NOT_SAVED_YET, time.Time{}
	default:
		return daemonv1.SaveState_SAVE_STATE_SAVED, st.SavedAt
	}
}

// GetBoxState implements daemonv1.DaemonServiceServer.
func (s *Server) GetBoxState(ctx context.Context, req *daemonv1.GetBoxStateRequest) (*daemonv1.GetBoxStateResponse, error) {
	name := req.GetName()
	if host := s.location(name); host != "" {
		return remoteState(ctx, host, name)
	}
	ob, err := s.get(name)
	if err != nil {
		return &daemonv1.GetBoxStateResponse{State: daemonv1.SaveState_SAVE_STATE_CLOSED}, nil
	}
	st, _, err := ob.sb.LocalState()
	if err != nil {
		return nil, err
	}
	state, at := saveState(st, ob.saving.Load(), ob.offline.Load(), ob.agentDown.Load(), ob.retrying.Load(), ob.failed.Load(), ob.quitUnsaved.Load())
	r := &daemonv1.GetBoxStateResponse{State: state}
	if !at.IsZero() {
		r.SavedAt = timestamppb.New(at)
	}
	return r, nil
}

// errNotRemote: the box is not open on a server.
var errNotRemote = errors.New("not remote")

// onServer runs a box action through the server's runner when the box runs
// there, returning the runner's summary.
func (s *Server) onServer(ctx context.Context, name string, action string) (string, error) {
	host := s.location(name)
	if host == "" {
		return "", errNotRemote
	}
	out, err := local.AppOn(ctx, host, action, name)
	if err != nil {
		if strings.Contains(err.Error(), "the box agent is unavailable") {
			s.dropRemoteAgent(name)
			return "", status.Error(codes.Unavailable, err.Error())
		}
		return "", status.Error(codes.FailedPrecondition, err.Error())
	}
	return out, nil
}
