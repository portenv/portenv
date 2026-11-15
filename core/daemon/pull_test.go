// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/portenv/portenv/core/driver"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestTheFirstOpenShowsItsPull (R-0211): while an open pulls the toolbox
// image, the box's state carries how far it has got, for the window's
// "Downloading the workspace image… 312 of 900 MB"; once the open ends,
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

// TestTheWatchPushesEachPullStep (R-0427): the app follows a pull on the
// state stream it already watches. Each step reaches it within a tick, so
// the window's line moves ("Downloading the workspace image… 120 MB", then
// "… 120 of 283 MB") and every step counts as the open's activity.
func TestTheWatchPushesEachPullStep(t *testing.T) {
	t.Setenv("PORTENV_KEYS", "file")
	s, c := serving(t)
	boxWithState(t, s, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := c.WatchBoxState(ctx, &daemonv1.WatchBoxStateRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := stream.Recv(); err != nil || m.GetState().GetImagePull() != nil {
		t.Fatalf("before the pull: %v, %v", m, err)
	}
	pctx, done := s.followPull(context.Background(), "acme-api")
	for _, step := range [][2]int64{{0, 0}, {120_000_000, 0}, {120_000_000, 283_000_000}, {283_000_000, 283_000_000}} {
		start := time.Now()
		driver.ReportPull(pctx, step[0], step[1])
		m, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		p := m.GetState().GetImagePull()
		if p == nil || p.GetDoneBytes() != step[0] || p.GetTotalBytes() != step[1] {
			t.Fatalf("pushed %v, want %v", p, step)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("the step took %v to arrive, want a tick", d)
		}
	}
	done()
	m, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if m.GetState().GetImagePull() != nil {
		t.Fatalf("after the open: %v", m.GetState().GetImagePull())
	}
}
