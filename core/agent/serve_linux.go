// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// ServeChannelProcess is portenv-agent serve: it makes itself non-dumpable
// before reading the channel token or any password, then serves the agent
// channel.
func ServeChannelProcess(ctx context.Context, cfg Config) error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("make agent channel non-dumpable: %w", err)
	}
	// The first line on stdin holds the secrets; stdin stays open for
	// later lines, each a re-key from a restarted portenvd or runner (ADR
	// 0014). The engine holds the pipe's other end; nothing in the box can
	// write to it.
	listen := func() (net.Listener, error) {
		return (&net.ListenConfig{}).Listen(ctx, "tcp", fmt.Sprintf(":%d", ChannelPort))
	}
	return serveStdinChannel(ctx, cfg, os.Stdin, listen, slog.Default())
}
