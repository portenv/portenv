// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// backupExcludeAttr is the attribute NSURLIsExcludedFromBackupKey and
// tmutil addexclusion set: Time Machine skips the folder and everything
// in it (ADR 0030 point 6), so no copy of an attachment outlives its
// deletion in a backup.
const backupExcludeAttr = "com.apple.metadata:com_apple_backup_excludeItem"

// backupExcludeValue is its value: a binary property list holding the
// string "com.apple.backupd", byte for byte what tmutil writes.
var backupExcludeValue = []byte("bplist00_\x10\x11com.apple.backupd\x08\x00\x00\x00\x00\x00\x00\x01\x01\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x1c")

// excludeFromBackup sets the attribute when it isn't there.
func excludeFromBackup(dir string) error {
	if excludedFromBackup(dir) {
		return nil
	}
	return unix.Setxattr(dir, backupExcludeAttr, backupExcludeValue, 0)
}

func excludedFromBackup(dir string) bool {
	buf := make([]byte, 256)
	n, err := unix.Getxattr(dir, backupExcludeAttr, buf)
	return err == nil && bytes.Equal(buf[:n], backupExcludeValue)
}
