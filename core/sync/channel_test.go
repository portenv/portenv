// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// TestNoChannelIsAnErrorNotACrash: with no agent connection (the box was
// just closed), the executor says the agent is unavailable; it never
// dereferences nil.
func TestNoChannelIsAnErrorNotACrash(t *testing.T) {
	ex := ChannelExecutor{Client: func() agentv1.AgentServiceClient { return nil }}
	_, err := ex.Restic(context.Background(), []string{"snapshots"}, Credentials{Password: []byte("pw")})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "the box agent is unavailable") {
		t.Fatalf("Restic with no channel: %v", err)
	}
	_, err = ex.PathInfo(context.Background(), "/home")
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "the box agent is unavailable") {
		t.Fatalf("PathInfo with no channel: %v", err)
	}
	if _, err := (ChannelExecutor{}).PathInfo(context.Background(), "/home"); status.Code(err) != codes.Unavailable {
		t.Fatalf("an empty executor: %v", err)
	}
}
