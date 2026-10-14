// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"

	"github.com/portenv/portenv/core/internal/version"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestGetVersionCarriesTheBuildID (R-0198): the app compares it with its
// own and restarts a portenvd of another build.
func TestGetVersionCarriesTheBuildID(t *testing.T) {
	saved := version.BuildID
	version.BuildID = "8e1f0c2a-build"
	t.Cleanup(func() { version.BuildID = saved })
	r, err := (&Server{}).GetVersion(context.Background(), &daemonv1.GetVersionRequest{})
	if err != nil || r.GetBuild().GetBuildId() != "8e1f0c2a-build" {
		t.Fatalf("got %v, %v", r, err)
	}
}
