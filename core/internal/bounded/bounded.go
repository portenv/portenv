// SPDX-License-Identifier: Apache-2.0

// Package bounded is the only way the daemon side (sync, local, daemon, the
// drivers and the commands) starts an external process: restic, ssh or
// anything else. Every process has a limit and is stopped cleanly when it
// passes (ADR 0012). internal/boundary fails CI on any other process start
// there.
package bounded

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// KillGrace is how long a process has to exit after SIGINT before SIGKILL.
const KillGrace = 15 * time.Second

// Cmd is an exec.Cmd stopped when its context ends or its limit passes,
// whichever comes first: SIGINT, then SIGKILL after KillGrace. Run, Output,
// CombinedOutput and Wait release the limit's timer when the process ends.
type Cmd struct {
	*exec.Cmd
	cancel context.CancelFunc
}

// ErrNoLimit is returned for a process without a positive limit.
var ErrNoLimit = errors.New("bounded: every process needs a positive limit")

// Command returns name with args, limited to limit (and to ctx). It starts
// nothing; a limit of zero or less is refused, so no process runs unbounded.
func Command(ctx context.Context, limit time.Duration, name string, args ...string) (*Cmd, error) {
	if limit <= 0 {
		return nil, ErrNoLimit
	}
	lctx, cancel := context.WithTimeout(ctx, limit)
	cmd := exec.CommandContext(lctx, name, args...) // #nosec G204 -- callers pass fixed programs or the user's own configuration
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = KillGrace
	return &Cmd{Cmd: cmd, cancel: cancel}, nil
}

// Limit is the time left before ctx's deadline, or fallback when ctx has
// none: for a caller whose context already carries the real deadline.
func Limit(ctx context.Context, fallback time.Duration) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		if left := time.Until(d); left > 0 {
			return left
		}
		return time.Millisecond // already passed: stop at once
	}
	return fallback
}

// Run starts the command and waits for it.
func (c *Cmd) Run() error {
	defer c.cancel()
	return c.Cmd.Run()
}

// Output runs the command and returns its standard output.
func (c *Cmd) Output() ([]byte, error) {
	defer c.cancel()
	return c.Cmd.Output()
}

// CombinedOutput runs the command and returns its combined output.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	defer c.cancel()
	return c.Cmd.CombinedOutput()
}

// Wait waits for a command started with Start.
func (c *Cmd) Wait() error {
	defer c.cancel()
	return c.Cmd.Wait()
}
