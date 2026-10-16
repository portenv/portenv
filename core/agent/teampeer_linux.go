// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"net"

	"golang.org/x/sys/unix"
)

// socketPeerUID is the uid of the process at the other end, from the
// kernel (SO_PEERCRED): never from the request.
func socketPeerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			gerr = err
			return
		}
		uid = int(cred.Uid)
	}); err != nil {
		return 0, err
	}
	return uid, gerr
}
