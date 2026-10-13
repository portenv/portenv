// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	already, err := s.env.CreateBox(ctx, createRequest(req.GetName(), req.GetBoxId(), req.GetImage(), req.GetStorage(),
		req.GetStorageHostKey(), req.GetStorageRest(), req.GetKeys(), req.GetRecoveryPassword()), local.CreateOptions{})
	if err != nil {
		st := createStatus(err)
		s.log.Info("box not created", "box", req.GetName(), "id", req.GetBoxId(), "code", status.Code(st).String())
		return nil, st
	}
	s.log.Info("box created", "box", req.GetName(), "id", req.GetBoxId(), "already", already)
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
