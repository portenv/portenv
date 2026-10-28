// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// Where a box is open, when it isn't this Mac (2.3, the lease sheet).

const (
	// RefusalTrailer marks why OpenBox refused, for the app (never the
	// words, R-0168).
	RefusalTrailer = "portenv-refusal"
	// RefusalOpenElsewhere: another machine holds the box open.
	RefusalOpenElsewhere = "open-elsewhere"
	// RefusalMoving: a Move To is in progress, so CloseBox refused
	// (R-0249); the app says so, and offers no Close or Quit Anyway.
	RefusalMoving = "moving"
	// RefusalTeamStartPending: StartAgent refused because the team's first
	// start waits for the owner's Approve (R-0413); the app shows its own
	// hint, and the CLI the words.
	RefusalTeamStartPending = "team-start-pending"
)

// refuseOpenElsewhere is OpenBox's answer when another machine holds the
// box: FailedPrecondition, marked with the trailer, and the lease kept for
// GetPlacement.
func (s *Server) refuseOpenElsewhere(ctx context.Context, name string, held *boxsync.LeaseHeldError) error {
	s.holdElsewhere(name, held.Lease)
	_ = grpc.SetTrailer(ctx, metadata.Pairs(RefusalTrailer, RefusalOpenElsewhere))
	return status.Error(codes.FailedPrecondition, held.Error())
}

// holdElsewhere records the lease another machine holds on a box.
func (s *Server) holdElsewhere(name string, l boxsync.Lease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.elsewhere == nil {
		s.elsewhere = map[string]boxsync.Lease{}
	}
	s.elsewhere[name] = l
}

// openedHere forgets another machine's lease once the box opens here.
func (s *Server) openedHere(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.elsewhere, name)
}

func (s *Server) heldElsewhere(name string) (boxsync.Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.elsewhere[name]
	return l, ok
}

// holdingServer is the known server that holds the box, if one does: the
// one this Mac put it on, else the one the last refusal's lease names.
func (s *Server) holdingServer(ctx context.Context, name string) string {
	if host := s.location(name); host != "" {
		return host
	}
	l, ok := s.heldElsewhere(name)
	if !ok {
		return ""
	}
	servers, err := s.env.ServerTargets()
	if err != nil {
		return ""
	}
	host, _ := s.env.ServerForMachine(ctx, servers, l.Machine)
	return host
}

// connectThere is OpenBox's Connect to it there: attach to the known server
// that holds the box, which keeps running there.
func (s *Server) connectThere(ctx context.Context, name string) (*daemonv1.OpenBoxResponse, error) {
	host := s.holdingServer(ctx, name)
	if host == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "%s isn't open on one of this Mac's servers, so it can't be reached there.", name)
	}
	st, err := remoteState(ctx, host, name)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%s isn't answering.", local.ServerDisplayName(host))
	}
	if st.GetState() == daemonv1.SaveState_SAVE_STATE_CLOSED {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is no longer open on %s.", name, local.ServerDisplayName(host))
	}
	s.attachRemote(name, host)
	s.openedHere(name)
	return &daemonv1.OpenBoxResponse{Summary: "open on " + host, Location: host}, nil
}

// GetPlacement implements daemonv1.DaemonServiceServer.
func (s *Server) GetPlacement(ctx context.Context, req *daemonv1.GetPlacementRequest) (*daemonv1.GetPlacementResponse, error) {
	if _, err := s.env.LoadBox(req.GetName()); err != nil {
		var u local.UsageError
		if errors.As(err, &u) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return nil, status.Error(codes.NotFound, err.Error())
	}
	l, held := s.heldElsewhere(req.GetName())
	host := s.holdingServer(ctx, req.GetName())
	resp := &daemonv1.GetPlacementResponse{Holder: daemonv1.Holder_HOLDER_NONE}
	if held {
		resp.Machine = l.Machine
		resp.NewestSaveAt = timestamppb.New(l.LastSeen)
	}
	switch {
	case host != "":
		resp.Holder = daemonv1.Holder_HOLDER_KNOWN_SERVER
		resp.ServerId, resp.ServerName = local.ServerID(host), local.ServerDisplayName(host)
		if req.GetProbe() {
			resp.Answers = daemonv1.Answers_ANSWERS_NO
			if local.ServerAnswers(ctx, host) {
				resp.Answers = daemonv1.Answers_ANSWERS_YES
			}
		}
	case held:
		resp.Holder, resp.Answers = daemonv1.Holder_HOLDER_OTHER_MACHINE, daemonv1.Answers_ANSWERS_CANNOT_ASK
	}
	return resp, nil
}
