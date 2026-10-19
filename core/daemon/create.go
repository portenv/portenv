// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// The calls that make and restore boxes (ADR 0019) carry keys: like
// ProvideKeys they reach portenvd only over its owner-only socket, and
// neither the request nor any key is ever logged. The log says the method,
// the box's name and ID, and the outcome.

// createStatus turns CreateBox's and RecoverBox's errors into their codes,
// each one plain line.
func createStatus(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "The request was cancelled.")
	}
	var ce *local.CreateError
	if !errors.As(err, &ce) {
		return status.Error(codes.Internal, err.Error())
	}
	code := map[local.CreateErrorKind]codes.Code{
		local.ErrInvalid:     codes.InvalidArgument,
		local.ErrNameTaken:   codes.AlreadyExists,
		local.ErrMismatch:    codes.FailedPrecondition,
		local.ErrUnreachable: codes.Unavailable,
		local.ErrUnsupported: codes.Unimplemented,
		local.ErrDeadline:    codes.DeadlineExceeded,
		local.ErrInternal:    codes.Internal,
	}[ce.Kind]
	return status.Error(code, ce.Msg)
}

func createRequest(name, boxID, image, storage, hostKey, rest string, ks map[string][]byte, recoveryPassword []byte) local.CreateRequest {
	return local.CreateRequest{Name: name, BoxID: boxID, Image: image, Storage: storage,
		StorageHostKey: hostKey, StorageREST: rest, Keys: ks, RecoveryPassword: recoveryPassword}
}

// CreateBox implements daemonv1.DaemonServiceServer.
func (s *Server) CreateBox(ctx context.Context, req *daemonv1.CreateBoxRequest) (*daemonv1.CreateBoxResponse, error) {
	// Docker Desktop answers and the disk has room, before anything is
	// written (R-0323).
	if err := s.checkNewBox(ctx); err != nil {
		s.log.Info("box not created", "box", req.GetName(), "id", req.GetBoxId(), "code", status.Code(err).String())
		return nil, err
	}
	// The team chosen in New workspace (ADR 0025): approved, file by
	// file, within the owner's limit, or nothing is made.
	if t := req.GetTeam(); t != nil {
		if err := checkTeamChoice(t, readAgentsAllowed(s.env.Dir)); err != nil {
			s.log.Info("box not created", "box", req.GetName(), "id", req.GetBoxId(), "code", status.Code(err).String())
			return nil, err
		}
	}
	already, err := s.env.CreateBox(ctx, createRequest(req.GetName(), req.GetBoxId(), req.GetImage(), req.GetStorage(),
		req.GetStorageHostKey(), req.GetStorageRest(), req.GetKeys(), req.GetRecoveryPassword()), local.CreateOptions{})
	if err != nil {
		st := createStatus(err)
		s.log.Info("box not created", "box", req.GetName(), "id", req.GetBoxId(), "code", status.Code(st).String())
		return nil, st
	}
	if t := req.GetTeam(); t != nil {
		// Kept for ApplyTeamPlan, which writes it once the workspace runs.
		// A retry (already created) keeps it again.
		if err := keepTeamPlan(filepath.Join(s.env.Dir, "state", req.GetBoxId()), t); err != nil {
			return nil, status.Error(codes.Internal, "Portenv couldn\u2019t keep the workspace\u2019s team. Try again.")
		}
	}
	s.log.Info("box created", "box", req.GetName(), "id", req.GetBoxId(), "already", already, "team", req.GetTeam().GetTemplate())
	return &daemonv1.CreateBoxResponse{BoxId: req.GetBoxId(), AlreadyCreated: already}, nil
}

// DiscardCreation implements daemonv1.DaemonServiceServer.
func (s *Server) DiscardCreation(ctx context.Context, req *daemonv1.DiscardCreationRequest) (*daemonv1.DiscardCreationResponse, error) {
	if err := s.env.DiscardCreation(ctx, req.GetBoxId()); err != nil {
		return nil, createStatus(err)
	}
	s.log.Info("creation discarded", "id", req.GetBoxId())
	return &daemonv1.DiscardCreationResponse{}, nil
}

// RecoverBox implements daemonv1.DaemonServiceServer.
func (s *Server) RecoverBox(ctx context.Context, req *daemonv1.RecoverBoxRequest) (*daemonv1.RecoverBoxResponse, error) {
	already, err := s.env.RecoverBox(ctx, createRequest(req.GetName(), req.GetBoxId(), req.GetImage(), req.GetStorage(),
		req.GetStorageHostKey(), req.GetStorageRest(), req.GetKeys(), req.GetRecoveryPassword()))
	if err != nil {
		st := createStatus(err)
		s.log.Info("box not recovered", "box", req.GetName(), "id", req.GetBoxId(), "code", status.Code(st).String())
		return nil, st
	}
	s.log.Info("box recovered", "box", req.GetName(), "id", req.GetBoxId(), "already", already)
	return &daemonv1.RecoverBoxResponse{BoxId: req.GetBoxId(), AlreadyRecovered: already}, nil
}

// CheckRecoveryKey implements daemonv1.DaemonServiceServer.
func (s *Server) CheckRecoveryKey(ctx context.Context, req *daemonv1.CheckRecoveryKeyRequest) (*daemonv1.CheckRecoveryKeyResponse, error) {
	res, err := s.env.CheckRecoveryPasswords(ctx, req.GetRecoveryPasswords())
	if err != nil {
		st := createStatus(err)
		s.log.Info("recovery key not checked", "code", status.Code(st).String())
		return nil, st
	}
	s.log.Info("recovery key checked", "checked", res.Checked, "opened", res.Opened)
	return &daemonv1.CheckRecoveryKeyResponse{Checked: int32(res.Checked), Opened: res.Opened}, nil // #nosec G115 -- a count of this Mac's boxes
}

// minFreeForNewBox is the free disk a new workspace needs on this Mac: the
// toolbox image, its home and its first saves (R-0323).
const minFreeForNewBox = 5_000_000_000

// newBoxEngineWait bounds CreateBox's engine check (tests shorten it).
var newBoxEngineWait = 5 * time.Second

// The lines CreateBox refuses with before it writes anything; the app shows
// them under Create (GUIDELINES §10).
const (
	NewBoxEngineNotRunning   = "Portenv uses Docker Desktop to run workspaces on this Mac, and it isn’t running. Start Docker Desktop, then create the workspace again."
	NewBoxEngineUnresponsive = "Docker Desktop is still starting, or isn’t answering. Wait a moment, then create the workspace again."
)

// newBoxNoDisk says how much is free and what a new workspace needs.
func newBoxNoDisk(free uint64) string {
	return fmt.Sprintf("This Mac has %s free, and a new workspace needs at least %s. Free some space, then create the workspace again.",
		gigabytes(free), gigabytes(minFreeForNewBox))
}

// gigabytes is "3.2 GB" (GUIDELINES §10's numbers), in macOS's decimal units.
func gigabytes(n uint64) string {
	g := float64(n) / 1e9
	if g >= 10 || g == float64(int(g)) {
		return fmt.Sprintf("%.0f GB", g)
	}
	return fmt.Sprintf("%.1f GB", g)
}

// checkNewBox refuses a new workspace while Docker Desktop doesn't answer
// or the disk holding Portenv's folder is short of minFreeForNewBox. A
// disk that can't be measured isn't a refusal: the create's own errors
// still say so.
func (s *Server) checkNewBox(ctx context.Context) error {
	if s.engine != nil {
		ectx, cancel := context.WithTimeout(ctx, newBoxEngineWait)
		err := s.engine(ectx)
		cancel()
		if err != nil {
			// Docker's socket is a unix:// address in the person's home.
			s.log.Info("engine check before create", "err", noPaths(strings.ReplaceAll(err.Error(), "unix://", "unix: ")))
			if ctx.Err() != nil {
				return createStatus(ctx.Err())
			}
			if errors.Is(ectx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrEngineUnresponsive) {
				return status.Error(codes.Unavailable, NewBoxEngineUnresponsive)
			}
			return status.Error(codes.Unavailable, NewBoxEngineNotRunning)
		}
	}
	if s.freeBytes != nil && s.env != nil {
		free, err := s.freeBytes(s.env.Dir)
		if err != nil {
			s.log.Info("disk check before create", "err", noPaths(err.Error()))
		} else if free < minFreeForNewBox {
			return status.Error(codes.ResourceExhausted, newBoxNoDisk(free))
		}
	}
	return nil
}
