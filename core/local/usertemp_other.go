// SPDX-License-Identifier: Apache-2.0

//go:build !darwin || !cgo

package local

import (
	"fmt"
	"os"
)

// socketDirPath on a server (the runner), or a build without cgo: a folder
// of this user's own in /tmp, checked before every use like the Mac's
// (ensurePrivateDir refuses it if someone else made it first).
func socketDirPath() (string, error) {
	return fmt.Sprintf("/tmp/portenv-%d", os.Getuid()), nil
}
