// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"

	"github.com/portenv/portenv/core/driver"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestTheFirstOpenShowsItsPull (R-0211): while an open pulls the toolbox
// image, the box's state carries how far it has got, for the window's
// "Getting the workspace image… 312 MB of 900 MB"; once the open ends,
// it's gone.
func TestTheFirstOpenShowsItsPull(t *testing.T) {
	s := placementServer(t)
	ctx, done := s.followPull(context.Background(), "acme-api")
	driver.ReportPull(ctx, 312<<20, 900<<20)
	st, err := s.GetBoxState(context.Background(), &daemonv1.GetBoxStateRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if p := st.GetImagePull(); p.GetDoneBytes() != 312<<20 || p.GetTotalBytes() != 900<<20 {
		t.Fatalf("pull %v", p)
	}
	done()
	st, _ = s.GetBoxState(context.Background(), &daemonv1.GetBoxStateRequest{Name: "acme-api"})
	if st.GetImagePull() != nil {
		t.Fatalf("still pulling: %v", st.GetImagePull())
	}
}
