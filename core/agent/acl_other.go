// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// fileProjectOps: ACLs are Linux's, so outside a box open-project refuses
// (as a filesystem without ACLs).
func fileProjectOps() projectOps {
	return projectOps{
		self: os.Getuid(),
		othersWrite: func(_ *os.File, st *unix.Stat_t, _ bool) (bool, error) {
			return st.Mode&0o002 != 0 || (st.Gid != uint32(os.Getgid()) && st.Mode&0o020 != 0), nil // #nosec G115 -- a gid
		},
		readOnly: func(*os.File, bool) error { return errors.ErrUnsupported },
	}
}

// statType is the file type bits of a stat.
func statType(st *unix.Stat_t) uint32 { return uint32(st.Mode) & unix.S_IFMT }

func statDev(st *unix.Stat_t) uint64 { return uint64(st.Dev) } // #nosec G115 -- a device number
