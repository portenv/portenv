// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// tabsFor checks the session name and returns its tabs, run as the main
// user.
func (s *apiServer) tabsFor(session string) (tabs, error) {
	if !sessionRE.MatchString(session) {
		return tabs{}, status.Error(codes.InvalidArgument, "session names are 1 to 32 letters, digits, - or _")
	}
	run, err := s.tmux(s.cfg)
	if err != nil {
		return tabs{}, status.Error(codes.FailedPrecondition, err.Error())
	}
	return tabs{tmux: run, session: session}, nil
}

func (s *apiServer) ListTabs(ctx context.Context, req *agentv1.ListTabsRequest) (*agentv1.ListTabsResponse, error) {
	tb, err := s.tabsFor(req.GetSession())
	if err != nil {
		return nil, err
	}
	ts, err := tb.list(ctx)
	if err != nil {
		return nil, err
	}
	return &agentv1.ListTabsResponse{Tabs: ts}, nil
}

func (s *apiServer) NewTab(ctx context.Context, req *agentv1.NewTabRequest) (*agentv1.NewTabResponse, error) {
	tb, err := s.tabsFor(req.GetSession())
	if err != nil {
		return nil, err
	}
	t, err := tb.open(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &agentv1.NewTabResponse{Tab: t}, nil
}

func (s *apiServer) CloseTab(ctx context.Context, req *agentv1.CloseTabRequest) (*agentv1.CloseTabResponse, error) {
	tb, err := s.tabsFor(req.GetSession())
	if err != nil {
		return nil, err
	}
	return &agentv1.CloseTabResponse{}, tb.close(ctx, req.GetId())
}

func (s *apiServer) RenameTab(ctx context.Context, req *agentv1.RenameTabRequest) (*agentv1.RenameTabResponse, error) {
	tb, err := s.tabsFor(req.GetSession())
	if err != nil {
		return nil, err
	}
	return &agentv1.RenameTabResponse{}, tb.rename(ctx, req.GetId(), req.GetName())
}

func (s *apiServer) SelectTab(ctx context.Context, req *agentv1.SelectTabRequest) (*agentv1.SelectTabResponse, error) {
	tb, err := s.tabsFor(req.GetSession())
	if err != nil {
		return nil, err
	}
	return &agentv1.SelectTabResponse{}, tb.selectTab(ctx, req.GetId())
}

func (s *apiServer) WatchTabs(req *agentv1.WatchTabsRequest, stream agentv1.AgentService_WatchTabsServer) error {
	tb, err := s.tabsFor(req.GetSession())
	if err != nil {
		return err
	}
	return tb.watch(stream.Context(), tabsPoll, func(ts []*typesv1.Tab) error {
		return stream.Send(&agentv1.WatchTabsResponse{Tabs: ts})
	})
}
