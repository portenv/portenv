// SPDX-License-Identifier: Apache-2.0

// Package good starts processes only through the bounded runner.
package good

import (
	"context"
	"os/exec"
	"time"
)

type runner struct{}

func (runner) Command(context.Context, time.Duration, string, ...string) *exec.Cmd { return nil }

var bounded runner

func run(ctx context.Context) {
	cmd := bounded.Command(ctx, time.Minute, "restic", "snapshots")
	var err *exec.ExitError // types from os/exec are fine
	_, _ = cmd, err
}
