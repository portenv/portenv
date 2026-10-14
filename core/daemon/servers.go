// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// serverInfo is a record as the app sees it: by ID and name, never the
// address.
func serverInfo(r local.ServerRecord) *daemonv1.ServerInfo {
	i := &daemonv1.ServerInfo{Id: r.ID, Name: local.ServerDisplayName(r.Target), FromKnownHosts: r.ConfirmedFrom == local.ConfirmedFromKnownHosts, WasChanged: r.WasChanged}
	switch r.State {
	case local.ServerConfirmed:
		i.State, i.Fingerprint = typesv1.ServerKeyState_SERVER_KEY_STATE_CONFIRMED, local.Fingerprint(r.Key)
	case local.ServerKeyChanged:
		i.State = typesv1.ServerKeyState_SERVER_KEY_STATE_CHANGED
	default:
		i.State = typesv1.ServerKeyState_SERVER_KEY_STATE_UNCONFIRMED
		if r.Scanned != "" {
			i.Fingerprint = local.Fingerprint(r.Scanned)
		}
	}
	return i
}

// serverError: an unknown server is NotFound; any other refusal is the
// person's to read (FailedPrecondition).
func serverError(err error) error {
	if errors.Is(err, local.ErrNoSuchServer) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}

func (s *Server) serverInfoByID(id string) (*daemonv1.ServerInfo, error) {
	rs, err := s.env.ServerRecords()
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if r.ID == id {
			return serverInfo(r), nil
		}
	}
	return nil, serverError(local.ErrNoSuchServer)
}

// ScanServerKey implements daemonv1.DaemonServiceServer.
func (s *Server) ScanServerKey(ctx context.Context, req *daemonv1.ScanServerKeyRequest) (*daemonv1.ScanServerKeyResponse, error) {
	scan, err := s.env.ScanServerKey(ctx, req.GetServerId())
	if err != nil {
		return nil, serverError(err)
	}
	info, err := s.serverInfoByID(req.GetServerId())
	if err != nil {
		return nil, err
	}
	s.log.Info("server key scanned", "server", req.GetServerId(), "type", scan.KeyType, "known_hosts", scan.KnownHosts, "state", string(scan.State))
	return &daemonv1.ScanServerKeyResponse{Info: info, KeyType: scan.KeyType}, nil
}

// TrustServerKey implements daemonv1.DaemonServiceServer.
func (s *Server) TrustServerKey(_ context.Context, req *daemonv1.TrustServerKeyRequest) (*daemonv1.TrustServerKeyResponse, error) {
	if err := s.env.TrustServerKey(req.GetServerId(), req.GetFingerprint()); err != nil {
		return nil, serverError(err)
	}
	info, err := s.serverInfoByID(req.GetServerId())
	if err != nil {
		return nil, err
	}
	s.log.Info("server key trusted", "server", req.GetServerId(), "fingerprint", info.GetFingerprint())
	return &daemonv1.TrustServerKeyResponse{Info: info}, nil
}

// ForgetServerKey implements daemonv1.DaemonServiceServer.
func (s *Server) ForgetServerKey(_ context.Context, req *daemonv1.ForgetServerKeyRequest) (*daemonv1.ForgetServerKeyResponse, error) {
	if err := s.env.ForgetServerKey(req.GetServerId()); err != nil {
		return nil, serverError(err)
	}
	info, err := s.serverInfoByID(req.GetServerId())
	if err != nil {
		return nil, err
	}
	s.log.Info("server key forgotten", "server", req.GetServerId())
	return &daemonv1.ForgetServerKeyResponse{Info: info}, nil
}
