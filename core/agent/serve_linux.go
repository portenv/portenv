// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

// ServeChannelProcess is portenv-agent serve: it makes itself non-dumpable
// before reading the channel token or any password, then serves the agent
// channel.
func ServeChannelProcess(ctx context.Context, cfg Config) error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("make agent channel non-dumpable: %w", err)
	}
	return ServeChannel(ctx, cfg, ChannelDir, ChannelPort)
}
