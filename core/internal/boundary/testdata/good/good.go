// SPDX-License-Identifier: Apache-2.0

// Package good runs ordinary programs; the boundary test expects no
// violations here.
package good

import (
	"context"
	"os/exec"
)

const driverName = "docker" // naming a driver is fine; running its CLI is not

func f(ctx context.Context, prog string) {
	_ = exec.Command("restic", "snapshots")
	_ = exec.CommandContext(ctx, "git", "status")
	_ = exec.Command(prog)
	_ = driverName
}
