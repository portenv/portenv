// SPDX-License-Identifier: Apache-2.0

package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// SocketDir is the private folder for Portenv's sockets: SSH's shared
// connection (ControlMaster), the REST forwards and 1b's per-run agent. On
// a Mac it's `pv` in the per-user temp folder (ADR 0021): /tmp is a folder
// anyone can create a name in first. Made 0700, and checked before every
// use: it must be a real folder, this user's, and open to nobody else.
func SocketDir() (string, error) {
	dir, err := socketDirPath()
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ensurePrivateDir makes dir (0700) if it isn't there, then refuses it
// unless it's a folder (not a symlink), owned by this user, mode 0700.
func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder Portenv made; remove it and try again", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("%s is open to other users (mode %o); Portenv won't put its connections there", dir, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user; Portenv won't put its connections there", dir)
	}
	return nil
}

// ControlPersist bounds how long SSH's shared connection outlives its last
// use. While it's up, any process of this user can reach the server through
// it without a Keychain prompt, so the window stays short (ADR 0021): 10
// minutes.
const ControlPersist = "600"

// controlOptions are the ssh options for the shared connection to the
// server with this ID, in the checked folder. The socket is named by the
// ID's 12 hex digits, not ssh's %C: its 40, with the 17-character suffix ssh
// adds while it makes the socket, are too long for a Mac's 104-byte socket
// path.
func controlOptions(serverID string) ([]string, error) {
	dir, err := SocketDir()
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "c-"+strings.TrimPrefix(serverID, "srv-"))
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + sock, "-o", "ControlPersist=" + ControlPersist}, nil
}
