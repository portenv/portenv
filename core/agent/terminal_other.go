// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"errors"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

func runTerminal(agentv1.AgentService_TerminalServer, Config, *agentv1.TerminalOpen) error {
	return errors.New("terminals run only inside a box (Linux)")
}
