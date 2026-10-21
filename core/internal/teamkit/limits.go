// SPDX-License-Identifier: Apache-2.0

// Package teamkit is the agent team's shared kit (ADR 0022): the limits
// table (§7), the agent-file templates, and the guide agents read, which
// is generated from the limits and the proto so it can't drift (R-0247).
package teamkit

import (
	"io/fs"
	"path"
	"strings"
	"time"
)

// The limits (ADR 0022 §7, R-0256 to R-0258). portenvd enforces these
// values; the guide states them.
const (
	MaxTeamAgents            = 8
	DefaultTeamAgents        = 4
	MaxAgentPanes            = 8
	DefaultAgentPanes        = 4
	MaxMessageBytes          = 8 << 10
	MaxDecisionDetailBytes   = 32 << 10
	MaxDecisionSummary       = 200
	MessagesPerMinute        = 60
	PendingDecisionsPerAgent = 5
	DecisionExpiry           = 24 * time.Hour
	InboxWait                = 60 * time.Second
	MaxCallsInFlight         = 16
	MaxStatus                = 200
	StatusEvery              = 10 * time.Second
	DefaultHomeQuota         = 2 << 30
	MaxHandoffBytes          = 100 << 20
	MaxRoutines              = 4
	MinRoutineMinutes        = 10
	// An ask-owner's --choice (CC-0501): 2 to 4, each 1 to 80 characters.
	MinChoices          = 2
	MaxChoices          = 4
	MaxChoiceCharacters = 80
)

// ShareLoginOff is the guide's rule while share_login isn't in the tester
// build (ADR 0022 §2, R-0323, R-0324): nothing of the owner's is shared.
const ShareLoginOff = "share_login isn't supported in this build: nothing of the owner's sign-ins is shared with you, whatever your agent file says. Sign in yourself in your own terminal, or ask the owner."

// TemplateAgents is how many agents a template asks for: its agent files
// directly in the template, which are the ones on when it's written. A
// file in a subfolder (an agent the template ships off) isn't counted. A
// template asking for more than the owner's limit is offered the raise
// (R-0347); 0 for no such template.
func TemplateAgents(name string) int {
	entries, err := fs.ReadDir(templates, path.Join("templates", name))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			n++
		}
	}
	return n
}
