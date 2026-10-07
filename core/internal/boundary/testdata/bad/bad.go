// SPDX-License-Identifier: Apache-2.0

// Package bad crosses the driver boundary on purpose; the boundary test
// expects exactly five violations here.
package bad

import (
	"context"
	run "os/exec"

	_ "github.com/docker/docker/client"
)

func f(ctx context.Context) {
	_ = run.Command("docker", "exec", "box", "sh")
	_ = run.CommandContext(ctx, "/usr/local/bin/docker", "ps")
	_ = run.Command("container", "list")
	_, _ = run.LookPath("podman")
}
