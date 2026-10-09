// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	boxsync "github.com/portenv/portenv/core/sync"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// The Phase 0 CLI's save, history and housekeep, moved into portenvd in
// 1.1 so they go over the agent channel, never docker exec (ADR 0010
// condition 4).

// SaveNow implements daemonv1.DaemonServiceServer: an autosave now.
func (s *Server) SaveNow(ctx context.Context, req *daemonv1.SaveNowRequest) (*daemonv1.SaveNowResponse, error) {
	if _, err := s.onServer(ctx, req.GetName(), "save"); !errors.Is(err, errNotRemote) {
		if err != nil {
			return nil, err
		}
		return &daemonv1.SaveNowResponse{}, nil
	}
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	ob.saving.Store(true)
	snap, err := ob.sb.Save(ctx, boxsync.SaveOptions{Kind: boxsync.SaveAutosave})
	ob.saving.Store(false)
	ob.failed.Store(err != nil)
	if err != nil {
		return nil, ob.noteErr(err)
	}
	ob.offline.Store(false)
	return &daemonv1.SaveNowResponse{Snapshot: ref(ob.sess.Cfg.ID, snap)}, nil
}

// ListSaves implements daemonv1.DaemonServiceServer: the box's saves.
func (s *Server) ListSaves(ctx context.Context, req *daemonv1.ListSavesRequest) (*daemonv1.ListSavesResponse, error) {
	if s.location(req.GetName()) != "" {
		return nil, status.Error(codes.FailedPrecondition, "the box runs on a server: list its saves there (portenv app history)")
	}
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	hist, err := ob.sb.History(ctx)
	if err != nil {
		return nil, ob.noteErr(err)
	}
	r := &daemonv1.ListSavesResponse{}
	for _, h := range hist {
		r.Saves = append(r.Saves, &daemonv1.SaveEntry{
			Id: h.ID, Time: timestamppb.New(h.Time), Kind: h.Kind.String(), Machine: h.Machine, OpenOn: h.Active,
		})
	}
	return r, nil
}

// Housekeep implements daemonv1.DaemonServiceServer: clear old lease tags,
// apply retention, and with prune delete data no save uses.
func (s *Server) Housekeep(ctx context.Context, req *daemonv1.HousekeepRequest) (*daemonv1.HousekeepResponse, error) {
	if s.location(req.GetName()) != "" {
		return nil, status.Error(codes.FailedPrecondition, "the box runs on a server: housekeep it there (portenv app housekeep)")
	}
	ob, err := s.get(req.GetName())
	if err != nil {
		return nil, err
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if err := ob.sb.Housekeep(ctx, req.GetPrune()); err != nil {
		return nil, ob.noteErr(err)
	}
	return &daemonv1.HousekeepResponse{}, nil
}
