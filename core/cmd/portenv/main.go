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

	"github.com/portenv/portenv/core/internal/version"
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
	"resume":     {"resume <box> [--take-over]", cmdResume},
	"save":       {"save <box> [--confirm]", cmdSave},
	"point":      {"point <box> [--confirm]", cmdPoint},
	"close":      {"close <box> [--confirm]", cmdClose},
	"status":     {"status <box>", cmdStatus},
	"history":    {"history <box>", cmdHistory},
	"housekeep":  {"housekeep <box> [--prune]", cmdHousekeep},
	"move":       {"move <box> --to SSH-HOST [--join-storage DIR]", cmdMove},
	"join":       {"join <box> --id BOX-ID --storage DIR [--image IMAGE]   (repository key on stdin)", cmdJoin},
	"attach":     {"attach <box> [--session NAME]   (through portenvd)", cmdAttach},
	"app":        {"app open|close|point|revert|check|restart|servers BOX | app move BOX this-mac|USER@HOST   (through portenvd)", cmdApp},
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
	if err := c.run(ctx, e, args[1], args[2:]); err != nil {
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

func usage() {
	fmt.Fprint(os.Stderr, `portenv (Phase 0, temporary): drive a box with the docker driver.

Commands:
  init <box> [--image IMAGE] [--storage DIR|s3:URL]
  resume <box> [--take-over]
  save <box> [--confirm]       autosave now
  point <box> [--confirm]      make a save point
  close <box> [--confirm]      save, release the lease, stop the box
  status <box>
  history <box>
  housekeep <box> [--prune]    clear old lease tags and apply retention (run when idle)
  move <box> --to SSH-HOST [--join-storage DIR]
                               close here, then resume on SSH-HOST (enrolling it first)
  join <box> --id BOX-ID --storage DIR    enrol a box here; repository key on stdin
  ssh-config <box> --host SERVER          print the "ssh portenv" entry
  attach <box> [--session NAME]           this terminal in the box's tmux session (portenvd)
  app ACTION BOX [TARGET]                 the app's actions through portenvd
  version
`)
}
