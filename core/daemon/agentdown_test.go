// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The state poll's readiness question marks a dropped channel as the agent
// being unavailable without waiting for the next action to fail (R-0110),
// clears it when the agent answers, and leaves it alone on a slow answer.
func TestStatePollNoticesADroppedAgentChannel(t *testing.T) {
	ob := &openBox{}
	ob.noteReadiness(status.Error(codes.Unavailable, "connection refused"))
	if !ob.agentDown.Load() {
		t.Fatal("a dropped channel left the agent marked available")
	}
	ob.noteReadiness(context.DeadlineExceeded)
	if !ob.agentDown.Load() {
		t.Fatal("a slow answer cleared the agent being unavailable")
	}
	ob.noteReadiness(nil)
	if ob.agentDown.Load() {
		t.Fatal("an answer left the agent marked unavailable")
	}
	ob.noteReadiness(status.Error(codes.DeadlineExceeded, "slow"))
	if ob.agentDown.Load() {
		t.Fatal("a slow answer marked the agent unavailable")
	}
}
