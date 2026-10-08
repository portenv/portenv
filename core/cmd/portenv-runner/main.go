// SPDX-License-Identifier: Apache-2.0

// Command portenv-runner is the Portenv server daemon, the server build of
// the core that runs boxes on developer servers and cloud hosts.
//
// Milestone 1.0b: the minimal runner that milestone 2.1 grows from. It is
// the same daemon as portenvd (core/daemon), run as root by systemd on a
// root-only socket: it opens boxes with the agent channel (ADR 0010) and
// serves save points, Revert To, checks, restarts and each box's channel to
// the Mac, which calls it over SSH through `sudo portenv app` (the only
// command the SSH user may run with sudo). Unlike portenvd it leaves boxes
// running when it stops: a runner restart stops no one's box.
//
//	portenv-runner            serve until SIGTERM or SIGINT
//	portenv-runner version
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
		fmt.Println("portenv-runner", version.String())
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "portenv-runner")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env, err := local.NewEnv()
	if err != nil {
		log.Error("portenv directory", "err", err)
		os.Exit(1)
	}
	log.Info("starting", "version", version.String(), "machine", env.Machine)
	srv := daemon.New(env, log)
	srv.KeepBoxesOnStop = true
	if err := srv.Serve(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
