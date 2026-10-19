// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// EnsureAgentUser makes an agent's user, group and home, and its run
// folder (ADR 0022 §1). Only
// init serves it, on the socket only the API process reaches; init checks
// the id itself.
func (s *server) EnsureAgentUser(ctx context.Context, req *agentv1.EnsureAgentUserRequest) (*agentv1.EnsureAgentUserResponse, error) {
	if s.team == nil {
		return nil, status.Error(codes.Unavailable, "agent users aren't available in this workspace")
	}
	u, err := s.team.Ensure(ctx, req.GetAgentId())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &agentv1.EnsureAgentUserResponse{
		Uid: uint32(u.UID), Home: u.Home, Created: u.Created, // #nosec G115 -- 2001..2008
		RunDir: u.RunDir, TmuxDir: u.TmuxDir, TmpDir: u.TmpDir,
	}, nil
}

// RemoveAgentUser takes an agent's user out; its files go only with
// delete_files, which portenvd sends after the owner approved it.
func (s *server) RemoveAgentUser(ctx context.Context, req *agentv1.RemoveAgentUserRequest) (*agentv1.RemoveAgentUserResponse, error) {
	if s.team == nil {
		return nil, status.Error(codes.Unavailable, "agent users aren't available in this workspace")
	}
	if err := s.team.Remove(ctx, req.GetAgentId(), req.GetDeleteFiles()); err != nil {
		return nil, teamStatus(err)
	}
	return &agentv1.RemoveAgentUserResponse{}, nil
}

func teamStatus(err error) error {
	switch {
	case errors.Is(err, ErrInvalidAgentID), errors.Is(err, ErrReservedAgentID):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrTeamFull):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, ErrNoAgent):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}

// PrepareTeamProject opens the project to the team (ADR 0022 §2); init
// only.
func (s *server) PrepareTeamProject(ctx context.Context, req *agentv1.PrepareTeamProjectRequest) (*agentv1.PrepareTeamProjectResponse, error) {
	if s.team == nil {
		return nil, status.Error(codes.Unavailable, "agent users aren't available in this workspace")
	}
	fixed, items, err := s.team.PrepareProject(ctx, req.GetProjectDir(), PrepareOptions{CloneAgents: req.GetCloneAgents(), HomeQuota: req.GetHomeQuotaBytes()})
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &agentv1.PrepareTeamProjectResponse{Fixed: fixed, Migration: items}, nil
}

// ResolveTeamMigration is the owner's answer to the move to agents' own
// clones (ADR 0022 §2a point 6); init only.
func (s *server) ResolveTeamMigration(ctx context.Context, req *agentv1.ResolveTeamMigrationRequest) (*agentv1.ResolveTeamMigrationResponse, error) {
	if s.team == nil {
		return nil, status.Error(codes.Unavailable, "agent users aren't available in this workspace")
	}
	if err := s.team.ResolveMigration(ctx, req.GetProjectDir(), req.GetRemoveItems()); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &agentv1.ResolveTeamMigrationResponse{}, nil
}
