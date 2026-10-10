// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"net/url"
	"path/filepath"
	"strings"
)

// storagePlace is where a box's saves go, as the inspector says it ("Saves
// go to <place>", GUIDELINES.md §6): a place name only, never a path, a
// user or a credential.
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
		host, _, _ := strings.Cut(strings.TrimPrefix(storage, "sftp:"), ":")
		if _, h, ok := strings.Cut(host, "@"); ok {
			host = h
		}
		if host != "" {
			return host
		}
	case strings.HasPrefix(storage, "s3:"):
		raw := strings.TrimPrefix(storage, "s3:")
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return "your storage"
}
