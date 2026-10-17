// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// DeliverRoutine is portenvd's call for a routine (R-0258, R-0285): only
// a wrapper, so the RPC runs exactly lane A's deliverRoutine, with every
// check of readyForRoutine and every refusal unchanged.
func (s *apiServer) DeliverRoutine(ctx context.Context, req *agentv1.DeliverRoutineRequest) (*agentv1.DeliverRoutineResponse, error) {
	if err := s.deliverRoutine(ctx, req.GetAgentId(), req.GetText()); err != nil {
		return nil, err
	}
	return &agentv1.DeliverRoutineResponse{}, nil
}
