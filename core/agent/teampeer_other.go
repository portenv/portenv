// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"errors"
	"net"
)

// socketPeerUID exists only in a box (Linux).
func socketPeerUID(*net.UnixConn) (int, error) {
	return 0, errors.New("agents run only inside a workspace")
}
