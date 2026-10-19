// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// wakeOwner is the board's name for the owner, the one sender that isn't
// an agent id.
const wakeOwner = "owner"

// wakeLine is the only thing a wake-up types (CC-0498): a fixed Portenv
// line naming who wrote, never what. An interactive runner such as Claude
// Code doesn't poll its inbox, so a message to an idle agent would wait
// for nobody; this line asks it to read.
func wakeLine(from string, count int32) string {
	who := from
	if from == wakeOwner {
		who = "the owner"
	}
	if count > 1 {
		return "[Portenv] new messages, the latest from " + who + "; run portenv inbox"
	}
	return "[Portenv] new message from " + who + "; run portenv inbox"
}

// WakeAgent is portenvd's call when an idle agent has unread board lines
// (CC-0498). The helper writes the line itself from a sender it checks
// (an agent id or "owner", so no control or bidi character can reach the
// pane) and types it through typeWhenIdle, with every check a routine
// gets: busy is refused, and portenvd tries again later.
//
// first_start types the fixed first-step line instead (R-0330), naming
// the running agent by its own checked id; from and count are ignored.
func (s *apiServer) WakeAgent(ctx context.Context, req *agentv1.WakeAgentRequest) (*agentv1.WakeAgentResponse, error) {
	if req.GetFirstStart() {
		a, ok := s.agents.get(req.GetAgentId())
		if !ok {
			return nil, status.Errorf(codes.FailedPrecondition, "%s isn't running", req.GetAgentId())
		}
		if err := s.typeWhenIdle(ctx, a, teamkit.FirstStepTyped(a.user.ID), "first-step line"); err != nil {
			return nil, err
		}
		return &agentv1.WakeAgentResponse{}, nil
	}
	from := req.GetFrom()
	if from != wakeOwner && !agentIDRE.MatchString(from) {
		return nil, status.Error(codes.InvalidArgument, "a wake-up's sender is an agent id or owner")
	}
	a, ok := s.agents.get(req.GetAgentId())
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "%s isn't running", req.GetAgentId())
	}
	if err := s.typeWhenIdle(ctx, a, wakeLine(from, req.GetCount()), "wake-up"); err != nil {
		return nil, err
	}
	return &agentv1.WakeAgentResponse{}, nil
}
