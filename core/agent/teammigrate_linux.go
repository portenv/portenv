// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// defaultMigrateOps are the move's real calls, as root (init).
func defaultMigrateOps(cfg Config) *migrateOps {
	work := uint32(cfg.UID) // #nosec G115 -- 1000
	return &migrateOps{
		othersWrite: func(f *os.File, st *unix.Stat_t, dir bool) (bool, error) { return othersWrite(f, st, dir, work) },
		reown: func(f *os.File, _ bool) error {
			if err := f.Chown(cfg.UID, cfg.UID); err != nil {
				return err
			}
			for _, a := range []string{aclXattrAccess, aclXattrDefault} {
				err := withFD(f, func(fd int) error { return unix.Fremovexattr(fd, a) })
				if err != nil && !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.EOPNOTSUPP) {
					return err
				}
			}
			fi, err := f.Stat()
			if err != nil {
				return err
			}
			// No group or other write; setgid (for group team) goes too.
			return f.Chmod(fi.Mode().Perm() &^ 0o022)
		},
		reownLink: func(dir *os.File, name string) error {
			return unix.Fchownat(fdOf(dir), name, cfg.UID, cfg.UID, unix.AT_SYMLINK_NOFOLLOW)
		},
		asAgent: runTeamIOAsAgent,
		running: agentUIDsRunning,
	}
}

// agentUIDsRunning are the agent uids (real, effective, saved or
// filesystem) of every process in the box.
func agentUIDsRunning() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	var out []int
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status")) // #nosec G304 -- /proc
		if err != nil {
			continue // gone
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 || f[0] != "Uid:" {
				continue
			}
			for _, v := range f[1:] {
				if uid, err := strconv.Atoi(v); err == nil && uid >= AgentUIDFirst && uid <= AgentUIDLast && !seen[uid] {
					seen[uid] = true
					out = append(out, uid)
				}
			}
			break
		}
	}
	return out, nil
}
