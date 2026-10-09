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
	"path/filepath"
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
	// launchd runs portenvd (the app's login item, PORTENVD_LOG=file): the
	// log goes to ~/Library/Logs/Portenv/portenvd.log, which a plist inside
	// the app bundle can't name. Run by hand, stderr.
	out := os.Stderr
	if os.Getenv("PORTENVD_LOG") == "file" {
		if home, err := os.UserHomeDir(); err == nil {
			if f, err := openLog(home); err == nil {
				out = f
			}
		}
	}
	log := slog.New(slog.NewTextHandler(out, nil)).With("component", "portenvd")
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

// openLog opens portenvd's log for appending: 0600 in a 0700 folder.
func openLog(home string) (*os.File, error) {
	dir := filepath.Join(home, "Library", "Logs", "Portenv")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory: 0700 is owner-only
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "portenvd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- fixed name in the user's log folder
}
