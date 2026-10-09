// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// ownerOnly accepts connections from this process's own user only: the
// socket is 0600 in a 0700 directory, and every connection's peer uid is
// checked too, so a mistake in either never lets another user in (ADR
// 0014). A refused connection is closed before any call.
type ownerOnly struct {
	net.Listener
	// peerUID returns the uid on the other end of a Unix socket connection.
	peerUID func(fd int) (int, error)
}

func (l ownerOnly) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if uc, ok := c.(*net.UnixConn); ok {
			if uid, err := connPeerUID(uc, l.peerUID); err == nil && uid == os.Getuid() {
				return c, nil
			}
		}
		_ = c.Close()
	}
}

func connPeerUID(c *net.UnixConn, peerUID func(int) (int, error)) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	uid, uerr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) { uid, uerr = peerUID(int(fd)) }); err != nil { // #nosec G115 -- a file descriptor
		return 0, err
	}
	return uid, uerr
}

// privateDir makes sure the Portenv directory belongs to this user and no
// one else can read it: refuse one owned by someone else, make our own 0700.
func privateDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user; portenvd serves only from its own directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700) // #nosec G302 -- tightening, not loosening
	}
	return nil
}
