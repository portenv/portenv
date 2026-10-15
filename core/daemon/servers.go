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
	i := &daemonv1.ServerInfo{Id: r.ID, Name: local.ServerDisplayName(r.Target), FromKnownHosts: r.ConfirmedFrom == local.ConfirmedFromKnownHosts,
		WasChanged: r.WasChanged, MadeKey: r.SignIn == local.SignInMadeKey}
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

// ProvideServerKey implements daemonv1.DaemonServiceServer: a made key,
// kept in memory only. Never logged.
func (s *Server) ProvideServerKey(_ context.Context, req *daemonv1.ProvideServerKeyRequest) (*daemonv1.ProvideServerKeyResponse, error) {
	if _, err := s.serverInfoByID(req.GetServerId()); err != nil {
		return nil, err
	}
	if err := s.env.ProvideServerKey(req.GetServerId(), req.GetKey()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	s.log.Info("server key provided", "server", req.GetServerId())
	return &daemonv1.ProvideServerKeyResponse{}, nil
}

// CheckServer implements daemonv1.DaemonServiceServer (ADR 0021 §3).
func (s *Server) CheckServer(ctx context.Context, req *daemonv1.CheckServerRequest) (*daemonv1.CheckServerResponse, error) {
	target, err := s.serverTarget(req.GetServerId())
	if err != nil {
		return nil, err
	}
	lines, err := local.CheckServer(ctx, target)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	res := &daemonv1.CheckServerResponse{Passed: local.AllChecksPassed(lines)}
	for _, l := range lines {
		res.Lines = append(res.Lines, &daemonv1.CheckLine{Id: l.ID, Title: l.Title, State: checkState(l.State), Detail: l.Detail, Fix: l.Fix})
	}
	s.log.Info("server checked", "server", req.GetServerId(), "passed", res.Passed)
	return res, nil
}

// serverTarget is the user@host for one of this Mac's servers.
func (s *Server) serverTarget(id string) (string, error) {
	rs, err := s.env.ServerRecords()
	if err != nil {
		return "", err
	}
	for _, r := range rs {
		if r.ID == id {
			return r.Target, nil
		}
	}
	return "", serverError(local.ErrNoSuchServer)
}

func checkState(s local.CheckState) typesv1.CheckState {
	switch s {
	case local.CheckPassed:
		return typesv1.CheckState_CHECK_STATE_PASSED
	case local.CheckFailed:
		return typesv1.CheckState_CHECK_STATE_FAILED
	case local.CheckInfo:
		return typesv1.CheckState_CHECK_STATE_INFO
	}
	return typesv1.CheckState_CHECK_STATE_UNSPECIFIED
}
