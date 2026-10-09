// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// Tabs (PLAN.md 1.2): portenvd relays the app's tab bar to the box agent,
// through the box's agent channel on this Mac or on a server, exactly like
// the terminal. The agent owns the tmux details.

// defaultSession is the box's terminal session.
const defaultSession = "main"

func sessionOr(s string) string {
	if s == "" {
		return defaultSession
	}
	return s
}

// agentFor is the agent client of a box open here or on a server.
func (s *Server) agentFor(ctx context.Context, box string) (agentv1.AgentServiceClient, error) {
	if s.location(box) != "" {
		return s.remoteAgent(ctx, box)
	}
	ob, err := s.get(box)
	if err != nil {
		return nil, err
	}
	a := ob.client()
	if a == nil {
		return nil, agentError(status.Error(codes.Unavailable, "no channel"))
	}
	return a, nil
}

// tabError reports an agent's tab error; a lost channel also drops a
// server's cached connection, as for the terminal.
func (s *Server) tabError(box string, err error) error {
	if status.Code(err) == codes.Unavailable && s.location(box) != "" {
		s.dropRemoteAgent(box)
	}
	return agentError(err)
}

func (s *Server) ListTabs(ctx context.Context, req *daemonv1.ListTabsRequest) (*daemonv1.ListTabsResponse, error) {
	a, err := s.agentFor(ctx, req.GetBox())
	if err != nil {
		return nil, err
	}
	r, err := a.ListTabs(ctx, &agentv1.ListTabsRequest{Session: sessionOr(req.GetSession())})
	if err != nil {
		return nil, s.tabError(req.GetBox(), err)
	}
	return &daemonv1.ListTabsResponse{Tabs: r.GetTabs()}, nil
}

func (s *Server) NewTab(ctx context.Context, req *daemonv1.NewTabRequest) (*daemonv1.NewTabResponse, error) {
	a, err := s.agentFor(ctx, req.GetBox())
	if err != nil {
		return nil, err
	}
	r, err := a.NewTab(ctx, &agentv1.NewTabRequest{Session: sessionOr(req.GetSession()), Name: req.GetName()})
	if err != nil {
		return nil, s.tabError(req.GetBox(), err)
	}
	return &daemonv1.NewTabResponse{Tab: r.GetTab()}, nil
}

func (s *Server) CloseTab(ctx context.Context, req *daemonv1.CloseTabRequest) (*daemonv1.CloseTabResponse, error) {
	a, err := s.agentFor(ctx, req.GetBox())
	if err != nil {
		return nil, err
	}
	if _, err := a.CloseTab(ctx, &agentv1.CloseTabRequest{Session: sessionOr(req.GetSession()), Id: req.GetId()}); err != nil {
		return nil, s.tabError(req.GetBox(), err)
	}
	return &daemonv1.CloseTabResponse{}, nil
}

func (s *Server) RenameTab(ctx context.Context, req *daemonv1.RenameTabRequest) (*daemonv1.RenameTabResponse, error) {
	a, err := s.agentFor(ctx, req.GetBox())
	if err != nil {
		return nil, err
	}
	if _, err := a.RenameTab(ctx, &agentv1.RenameTabRequest{Session: sessionOr(req.GetSession()), Id: req.GetId(), Name: req.GetName()}); err != nil {
		return nil, s.tabError(req.GetBox(), err)
	}
	return &daemonv1.RenameTabResponse{}, nil
}

func (s *Server) SelectTab(ctx context.Context, req *daemonv1.SelectTabRequest) (*daemonv1.SelectTabResponse, error) {
	a, err := s.agentFor(ctx, req.GetBox())
	if err != nil {
		return nil, err
	}
	if _, err := a.SelectTab(ctx, &agentv1.SelectTabRequest{Session: sessionOr(req.GetSession()), Id: req.GetId()}); err != nil {
		return nil, s.tabError(req.GetBox(), err)
	}
	return &daemonv1.SelectTabResponse{}, nil
}

// WatchTabs relays the agent's tab updates until either side ends.
func (s *Server) WatchTabs(req *daemonv1.WatchTabsRequest, stream daemonv1.DaemonService_WatchTabsServer) error {
	a, err := s.agentFor(stream.Context(), req.GetBox())
	if err != nil {
		return err
	}
	w, err := a.WatchTabs(stream.Context(), &agentv1.WatchTabsRequest{Session: sessionOr(req.GetSession())})
	if err != nil {
		return s.tabError(req.GetBox(), err)
	}
	for {
		m, err := w.Recv()
		if err != nil {
			if stream.Context().Err() != nil {
				return nil
			}
			return s.tabError(req.GetBox(), err)
		}
		if err := stream.Send(&daemonv1.WatchTabsResponse{Tabs: m.GetTabs()}); err != nil {
			return err
		}
	}
}
