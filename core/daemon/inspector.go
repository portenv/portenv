// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"path/filepath"
	"strings"
)

// storagePlace is where a box's saves go, as the inspector says it ("Saves
// go to <place>", GUIDELINES.md §6): a place name only, never a path, a
// user, a host name or an IP address (R-0019). A server or a bucket is
// "your server" or "your bucket" until servers have names (2.3).
func storagePlace(storage string) string {
	switch {
	case storage == "":
		return "this Mac"
	case filepath.IsAbs(storage):
		// A drive mounted on this Mac is named by its volume.
		if rest, ok := strings.CutPrefix(storage, "/Volumes/"); ok {
			if vol, _, _ := strings.Cut(rest, "/"); vol != "" {
				return vol
			}
		}
		return "this Mac"
	case strings.HasPrefix(storage, "sftp:"):
		return "your server"
	case strings.HasPrefix(storage, "s3:"):
		return "your bucket"
	}
	return "your storage"
}
