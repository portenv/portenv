// SPDX-License-Identifier: Apache-2.0

package driver

import "context"

// PullProgress hears how far a toolbox image's pull has got: the bytes
// fetched over every layer, and the total once every layer's size is known
// (0 until then, R-0427).
type PullProgress func(done, total int64)

type pullKey struct{}

// WithPullProgress lets whoever opens a box follow its image's pull (the
// first open's "Downloading the workspace image… 120 of 283 MB", R-0211,
// R-0427).
func WithPullProgress(ctx context.Context, f PullProgress) context.Context {
	return context.WithValue(ctx, pullKey{}, f)
}

// ReportPull is how a driver reports a pull's progress; with nobody
// following, it does nothing.
func ReportPull(ctx context.Context, done, total int64) {
	if f, ok := ctx.Value(pullKey{}).(PullProgress); ok && f != nil {
		f(done, total)
	}
}
