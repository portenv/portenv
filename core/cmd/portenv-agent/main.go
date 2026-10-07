// SPDX-License-Identifier: Apache-2.0

// Command portenv-agent is the in-box agent and the box's init process:
// terminals, lanes, port discovery, command audit and the outbound tunnel.
//
// Usage:
//
//	portenv-agent init      run as the box's init process (the image entrypoint)
//	portenv-agent ready     exit 0 if the box is ready, 1 otherwise (health check)
//	portenv-agent version   print the version
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/internal/version"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "version"
	if len(args) > 0 {
		cmd = args[0]
	}
	ctx := context.Background()
	switch cmd {
	case "init":
		log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "portenv-agent")
		log.Info("starting", "version", version.String())
		if err := agent.Init(ctx, agent.DefaultConfig(), log); err != nil {
			log.Error("init failed", "err", err)
			return 1
		}
		return 0
	case "ready":
		state, detail, err := agent.CheckReadiness(ctx, agent.DefaultSocket)
		if err != nil {
			fmt.Fprintln(os.Stderr, "portenv-agent:", err)
			return 1
		}
		fmt.Printf("%s: %s\n", state, detail)
		if state != agentv1.ReadinessState_READINESS_STATE_READY {
			return 1
		}
		return 0
	case "version", "--version", "-v":
		fmt.Println("portenv-agent", version.String())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "portenv-agent: unknown command %q (want init, ready or version)\n", cmd)
		return 2
	}
}
