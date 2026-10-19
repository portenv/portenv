// SPDX-License-Identifier: Apache-2.0

package daemon

import "golang.org/x/sys/unix"

// freeBytes is the space free to this user on dir's volume.
func freeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil // #nosec G115 -- block counts and sizes are never negative
}
