// SPDX-License-Identifier: Apache-2.0

// Package bad starts processes without the bounded runner, six ways.
package bad

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func run(ctx context.Context) {
	_ = exec.Command("restic", "snapshots")
	_ = exec.CommandContext(ctx, "ssh", "host")
	_, _ = os.StartProcess("/usr/bin/docker", nil, &os.ProcAttr{})
	_ = syscall.Exec("/bin/sh", nil, nil)
	_ = &exec.Cmd{Path: "/usr/bin/ssh"}
	start := exec.Command // a reference counts too
	_ = start
}
