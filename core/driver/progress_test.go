// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"testing"
)

// TestPullProgressRidesTheContext: whoever opens a box can follow its
// image's pull; with no one following, reporting is a no-op.
func TestPullProgressRidesTheContext(t *testing.T) {
	ReportPull(context.Background(), 1, 2) // nobody listening: fine
	var got [2]int64
	ctx := WithPullProgress(context.Background(), func(done, total int64) { got = [2]int64{done, total} })
	ReportPull(ctx, 312, 900)
	if got != [2]int64{312, 900} {
		t.Fatalf("got %v", got)
	}
}
