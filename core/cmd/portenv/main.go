// SPDX-License-Identifier: Apache-2.0

// Command portenv is the Portenv CLI for agents and power users, a thin
// client of the same APIs. It sets boxes up and drives them through
// portenvd (on servers, the runner); it never reaches into a box itself
// (ADR 0010 condition 4):
//
//	portenv init <box> [--image IMAGE] [--storage DIR|s3:URL]
//	portenv status <box>                       what this machine recorded
//	portenv join <box> --id ID --storage DIR   enrol a box (keys on stdin)
//	portenv ssh-config <box> --host SERVER     print the "ssh portenv" entry
//	portenv attach <box> [--session NAME]      this terminal in the box's tmux session
//	portenv app ACTION BOX [TARGET]            the box's actions through portenvd
//	portenv version
//
// The owner's team commands, through portenvd as the app makes them (ADR
// 0027 §5): msg, wake, tab new, team start|stop, deploy and decision list|
// answer; portenv help --json prints the command list (ADR 0027 §7).
//
// Inside a workspace, an agent's own commands go through its socket
// ($PORTENV_SOCKET) and portenvd's grant check (ADR 0022 §4): split, tab
// new, focus, close, msg, ask-owner, inbox, share, propose-agent-change,
// request-package and whoami.
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
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
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
	"app":        {"app open|close|save|point|revert|history|housekeep|check|restart|placement|servers BOX | app move BOX this-mac|USER@HOST | app fetch-agent BOX AGENT   (through portenvd)", cmdApp},
	"ssh-config": {"ssh-config <box> --host SERVER [--user USER] [--alias portenv]", cmdSSHConfig},
}

func run(ctx context.Context, args []string) int {
	if len(args) == 2 && args[0] == "help" && args[1] == "--json" {
		return printCommandList(os.Stdout, os.Stderr)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Println("portenv", version.String())
		return 0
	}
	// The owner's team commands (ADR 0027 §5): never an agent's, whose
	// portenv has only its grant socket (msg and tab new there are its own
	// calls), and never in the workspace's own shell, which has no owner
	// socket. Both are refused before anything is dialled.
	if ownerTeamCommand(args) {
		sock := os.Getenv("PORTENV_SOCKET")
		switch {
		case sharedWithAgents(args[0]) && (sock != "" || insideWorkspace()):
			return runTeam(ctx, args, os.Getenv, os.Stdin, os.Stdout, os.Stderr)
		case sock != "":
			fmt.Fprintln(os.Stderr, "This is the owner's command; an agent can't run it.")
			return 2
		case insideWorkspace():
			fmt.Fprintln(os.Stderr, "This is the owner's command; run it on your Mac.")
			return 2
		}
		return runOwner(ctx, args, func() (daemonv1.DaemonServiceClient, func(), error) {
			e, err := local.NewEnv()
			if err != nil {
				return nil, nil, err
			}
			return dialDaemon(e)
		}, os.Stdout, os.Stderr)
	}
	// Inside a workspace only the in-box commands run. The owner's would
	// make a Portenv folder in the home, which every save carries; portenv
	// init would write a box's repository key there (CC-0453).
	if ownersCommand(args[0], os.Getenv) && insideWorkspace() {
		fmt.Fprintln(os.Stderr, "This is the owner's command; run it on your Mac.")
		return 2
	}
	// This machine's ID, as a save's lease names it: a Mac asks its servers
	// so the lease sheet can say which one holds a workspace (2.3). Not
	// secret.
	if args[0] == "machine-id" {
		e, err := local.NewEnv()
		if err != nil {
			fmt.Fprintln(os.Stderr, "portenv:", err)
			return 1
		}
		fmt.Println(e.Machine)
		return 0
	}
	// An agent's own commands, inside a workspace (ADR 0022 §4).
	if isAgentCommand(args[0], os.Getenv) {
		return runTeam(ctx, args, os.Getenv, os.Stdin, os.Stdout, os.Stderr)
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
                                          move BOX this-mac|USER@HOST,
                                          delete BOX --yes-delete-everything, …
  version

Your team, on this Mac (each through portenvd, as the app does it; approvals stay in the app):
  msg AGENT TEXT [--thread ID]            message an agent, as you
  wake AGENT                              ask an idle agent to read its inbox (never a busy one)
  tab new --name NAME                     open a shell tab
  team start [AGENT...] | team stop [AGENT...]
  deploy TARGET                           run a target of the project's deploy runbook
  decision list [--all] [--json]          what's waiting for you
  decision answer ID approve|decline|N    check a request; you answer it in Portenv
  add --workspace NAME when more than one workspace is open
  help --json                             this command list, for programs

Inside a workspace, for agents (each through Portenv's grant check):
  split, tab new, focus, close, msg, ask-owner, inbox, share,
  propose-agent-change, request-package, whoami     add --json for data
`)
}

// ownersCommand is whether a command is the owner's, for a Mac or a
// server: machine-id and every command in commands that isn't an agent's
// in-box one ("status" with PORTENV_SOCKET set is the agent's).
func ownersCommand(name string, getenv func(string) string) bool {
	if isAgentCommand(name, getenv) {
		return false
	}
	_, ok := commands[name]
	return ok || name == "machine-id"
}

// promptsAllowed is whether a command may ask the person to approve a
// Keychain read: only with a terminal attached, and never for the app's own
// helper calls (app …), which run with no one watching this process.
func promptsAllowed(command string, terminal bool) bool {
	return terminal && command != "app"
}
