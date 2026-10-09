// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// dirLock is a daemon's exclusive hold on its Portenv directory: one
// portenvd (or runner) at a time, so a second one can never re-key a box
// the first still serves (ADR 0014). The kernel drops it when the process
// ends, however it ends.
type dirLock struct{ f *os.File }

func lockDir(dir string) (*dirLock, error) {
	f, err := os.OpenFile(filepath.Join(dir, "portenvd.lock"), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the daemon's own directory
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { // #nosec G115 -- a file descriptor
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another portenvd is already running for %s", dir)
		}
		return nil, err
	}
	return &dirLock{f: f}, nil
}

func (l *dirLock) release() { _ = l.f.Close() }
