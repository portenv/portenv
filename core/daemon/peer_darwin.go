// SPDX-License-Identifier: Apache-2.0

package daemon

import "golang.org/x/sys/unix"

func socketPeerUID(fd int) (int, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}
