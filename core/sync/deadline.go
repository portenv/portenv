// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"fmt"
	"time"

	"github.com/portenv/portenv/core/internal/bounded"
)

// Deadlines bound every restic run, so a save never hangs (a broken
// forward, a server that stopped answering): restic itself retries an
// unreachable backend for many minutes. A run that reaches its deadline is
// stopped (SIGINT, so restic releases its lock; SIGKILL after 15 s),
// the lock is cleared, the repository is checked when the command writes,
// and the command runs once more (ADR 0012).
type Deadlines struct {
	Short time.Duration // listing, tags, unlock, keys, init
	Check time.Duration // restic check (metadata only)
	// Backup and restore: Base plus the data at BytesPerSecond, the slowest
	// rate a working link is allowed (well below the 20 Mbit/s budget).
	Base           time.Duration
	BytesPerSecond int64
}

// KillGrace is how long restic has to exit after SIGINT before SIGKILL.
const KillGrace = bounded.KillGrace

// sshSetupLimit bounds each ssh call that sets up or checks the REST
// forward's connection.
const sshSetupLimit = 30 * time.Second

// DefaultDeadlines are the deadlines when Config leaves them unset.
var DefaultDeadlines = Deadlines{
	Short:          60 * time.Second,
	Check:          5 * time.Minute,
	Base:           2 * time.Minute,
	BytesPerSecond: 1 << 20, // 1 MiB/s, 8 Mbit/s
}

func (d Deadlines) withDefaults() Deadlines {
	def := DefaultDeadlines
	if d.Short <= 0 {
		d.Short = def.Short
	}
	if d.Check <= 0 {
		d.Check = def.Check
	}
	if d.Base <= 0 {
		d.Base = def.Base
	}
	if d.BytesPerSecond <= 0 {
		d.BytesPerSecond = def.BytesPerSecond
	}
	return d
}

// For is the deadline of restic command cmd moving about bytes of data.
func (d Deadlines) For(cmd string, bytes int64) time.Duration {
	switch cmd {
	case "backup", "restore":
		if bytes < 0 {
			bytes = 0
		}
		return d.Base + time.Duration(bytes/d.BytesPerSecond)*time.Second
	case "check":
		return d.Check
	default:
		return d.Short
	}
}

// writes reports whether a restic command changes the repository, so a run
// stopped part way is followed by a check before it is retried.
func writes(cmd string) bool {
	switch cmd {
	case "backup", "tag", "forget", "prune", "init", "key":
		return true
	}
	return false
}

// DeadlineError is a restic run stopped at its deadline.
type DeadlineError struct {
	Cmd      string
	Deadline time.Duration
	Retried  bool // stopped, then run once more (and the repository checked first when Cmd writes)
}

func (e *DeadlineError) Error() string {
	switch {
	case e.Retried && writes(e.Cmd):
		return fmt.Sprintf("restic %s did not finish within %v twice (stopped, repository checked, tried again)", e.Cmd, e.Deadline)
	case e.Retried:
		return fmt.Sprintf("restic %s did not finish within %v twice (stopped and tried again)", e.Cmd, e.Deadline)
	default:
		return fmt.Sprintf("restic %s did not finish within %v (stopped)", e.Cmd, e.Deadline)
	}
}
