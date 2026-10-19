// SPDX-License-Identifier: Apache-2.0

// Package teamkit is the agent team's shared kit (ADR 0022): the limits
// table (§7), the agent-file templates, and the guide agents read, which
// is generated from the limits and the proto so it can't drift (R-0247).
package teamkit

import "time"

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
