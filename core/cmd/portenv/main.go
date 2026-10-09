// SPDX-License-Identifier: Apache-2.0

// Command portenv is the Portenv CLI for agents and power users, a thin
// client of the same APIs.
//
// In Phase 0 it is a temporary tool that drives a box directly through the
// docker driver and the sync engine:
//
//	portenv init <box> [--image IMAGE] [--storage DIR|s3:URL]
//	portenv resume <box> [--take-over]
//	portenv save <box> [--confirm]      autosave now
//	portenv point <box> [--confirm]     make a save point
//	portenv close <box> [--confirm]     save, release the lease, stop the box
//	portenv status <box>
//	portenv history <box>
//	portenv housekeep <box> [--prune]   clear old lease tags, apply retention
//	portenv move <box> --to SSH-HOST [--join-storage DIR]
//	                                    close here, resume on SSH-HOST
//	portenv join <box> --id ID --storage DIR    enrol a box (key on stdin)
//	portenv ssh-config <box> --host SERVER      print the "ssh portenv" entry
//	portenv version
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/portenv/portenv/core/internal/version"
	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/local"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}

type command struct {
	usage string
	run   func(ctx context.Context, e *local.Env, name string, args []string) error
}

var commands = map[string]command{
	"init":       {"init <box> [--image IMAGE] [--storage DIR|s3:URL]", cmdInit},
	"status":     {"status <box>", cmdStatus},
	"join":       {"join <box> --id BOX-ID --storage DIR [--image IMAGE]   (repository key on stdin)", cmdJoin},
	"attach":     {"attach <box> [--session NAME]   (through portenvd)", cmdAttach},
	"app":        {"app open|close|save|point|revert|history|housekeep|check|restart|servers BOX | app move BOX this-mac|USER@HOST   (through portenvd)", cmdApp},
	"ssh-config": {"ssh-config <box> --host SERVER [--user USER] [--alias portenv]", cmdSSHConfig},
}

func run(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Println("portenv", version.String())
		return 0
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "portenv: unknown command %q\n", args[0])
		usage()
		return 2
	}
	if len(args) < 2 || args[1] == "" || args[1][0] == '-' {
		fmt.Fprintf(os.Stderr, "usage: portenv %s\n", c.usage)
		return 2
	}
	e, err := local.NewEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "portenv:", err)
		return 1
	}
	// Only a person at a terminal may be asked to approve a Keychain read.
	// The app's helper calls (app …) and scripts never prompt: they get
	// "Keychain needs your approval. Open Portenv on this Mac to allow it."
	if promptsAllowed(args[0], term.IsTerminal(int(os.Stdin.Fd()))) { // #nosec G115 -- a file descriptor
		keys.AllowPrompts()
	}
	if err := c.run(ctx, e, args[1], args[2:]); err != nil {
		if errors.As(err, new(quietError)) {
			return 1
		}
		fmt.Fprintln(os.Stderr, "portenv:", err)
		var u usageError
		if errors.As(err, &u) {
			fmt.Fprintf(os.Stderr, "usage: portenv %s\n", c.usage)
			return 2
		}
		return 1
	}
	return 0
}

type usageError = local.UsageError

// quietError fails the command without printing anything: someone else
// (the app's window) already says what happened.
type quietError struct{ error }

func usage() {
	fmt.Fprint(os.Stderr, `portenv: set up boxes, and drive them through portenvd (portenv app).

Commands:
  init <box> [--image IMAGE] [--storage DIR|s3:URL]
  status <box>                            what this machine recorded (never starts the box)
  join <box> --id BOX-ID --storage DIR    enrol a box here; repository key on stdin
  ssh-config <box> --host SERVER          print the "ssh portenv" entry
  attach <box> [--session NAME]           this terminal in the box's tmux session (portenvd)
  app ACTION BOX [TARGET]                 the box's actions through portenvd: open [--take-over],
                                          close, save, point, revert, history, housekeep [--prune],
                                          move BOX this-mac|USER@HOST, …
  version
`)
}

// promptsAllowed is whether a command may ask the person to approve a
// Keychain read: only with a terminal attached, and never for the app's own
// helper calls (app …), which run with no one watching this process.
func promptsAllowed(command string, terminal bool) bool {
	return terminal && command != "app"
}
