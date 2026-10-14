// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestNoWorkspaceSaysWorkspace: what portenvd says about a missing
// workspace is read in the app, so it says "workspace" (R-0133), while the
// CLI keeps its own wording until the CLI rename.
func TestNoWorkspaceSaysWorkspace(t *testing.T) {
	s := quietServer(t)
	_, err := s.GetKeyIDs(context.Background(), &daemonv1.GetKeyIDsRequest{Name: "nope"})
	if status.Code(err) != codes.NotFound || status.Convert(err).Message() != "There's no workspace named nope here." {
		t.Fatalf("missing workspace: %v", err)
	}
}
