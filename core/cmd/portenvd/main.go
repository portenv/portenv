// SPDX-License-Identifier: Apache-2.0

// Command portenvd is the Portenv Mac daemon. It owns the boxes on this Mac:
// driver calls, saves, leases and terminals, all through the box agent's
// channel (ADR 0010). It serves portenv.daemon.v1 on a Unix socket in the
// Portenv directory that only this user can reach.
//
// Milestone 1.0 (walking skeleton): open, close, save point, revert to the
// last save point, move to a server and back, and terminals. Autosave and
// port relays come in later milestones.
//
//	portenvd            serve until SIGTERM or SIGINT, then close open boxes
//	portenvd version
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/portenv/portenv/core/daemon"
	"github.com/portenv/portenv/core/internal/version"
	"github.com/portenv/portenv/core/local"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("portenvd", version.String())
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "portenvd")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env, err := local.NewEnv()
	if err != nil {
		log.Error("portenv directory", "err", err)
		os.Exit(1)
	}
	log.Info("starting", "version", version.String(), "machine", env.Machine)
	if err := daemon.New(env, log).Serve(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
