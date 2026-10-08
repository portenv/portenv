// SPDX-License-Identifier: Apache-2.0

package agent

import "fmt"

// The only users the agent ever switches to. Root is never one of them.
const (
	WorkUID    = 1000 // the main user
	LaneUIDMin = 2000 // agent lanes (Phase 4)
	LaneUIDMax = 2999
)

// Purpose says why the agent starts a process as another user.
type Purpose int

const (
	// ForUser is a terminal or command for a person or an agent: work or
	// a lane, never anyone else.
	ForUser Purpose = iota
	// ForRestic is a restic run: portenv-sync only (ADR 0005).
	ForRestic
)

// CheckSwitch allows a switch to uid only for its purpose: work or a lane
// for user processes, portenv-sync for restic. Every setuid in the agent
// goes through it; 0 and every other uid are refused.
func CheckSwitch(uid int, p Purpose) error {
	switch {
	case uid == 0:
		return fmt.Errorf("refusing to start a process as root")
	case p == ForUser && (uid == WorkUID || (uid >= LaneUIDMin && uid <= LaneUIDMax)):
		return nil
	case p == ForRestic && uid == syncUID:
		return nil
	}
	return fmt.Errorf("refusing to start a process as uid %d for this purpose", uid)
}
