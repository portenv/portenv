// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import "errors"

// agentAs reaches an agent's tmux server as the agent: in the box only.
func agentAs(AgentServerSpec) (tmuxFunc, tmuxCmdFunc, error) {
	return nil, nil, errors.New("agents' terminals run only in the workspace")
}

// RunAsAgent becomes an agent: in the box only.
func RunAsAgent([]string) error { return errors.New("as-agent runs only in the workspace") }
