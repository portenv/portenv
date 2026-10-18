// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// fileProjectOps: ACLs are Linux's, so outside a box open-project refuses
// (as a filesystem without ACLs).
func fileProjectOps() projectOps {
	return projectOps{
		self:     os.Getuid(),
		chown:    func(f *os.File, gid int) error { return f.Chown(-1, gid) },
		addACL:   func(*os.File, bool) error { return errors.ErrUnsupported },
		readACL:  func(*os.File, bool) error { return errors.ErrUnsupported },
		clearACL: func(*os.File, bool) (fs.FileMode, bool, error) { return 0, false, errors.ErrUnsupported },
	}
}

// statType is the file type bits of a stat.
func statType(st *unix.Stat_t) uint32 { return uint32(st.Mode) & unix.S_IFMT }

func statDev(st *unix.Stat_t) uint64 { return uint64(st.Dev) } // #nosec G115 -- a device number
