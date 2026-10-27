// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"path"
	"strings"

	"github.com/portenv/portenv/core/internal/idleinput"
)

// runnerIdlePattern is the runner table's idle-input pattern for a
// runner's argv (idleinput.Runners, R-0258), or "" for one Portenv doesn't
// know: such a runner gets no routines and no wake-ups. runnerIdle is the
// whole rule, sent with the agent's start.
func runnerIdlePattern(argv []string) string { return runnerIdle(argv).Pattern }

func runnerIdle(argv []string) idleinput.Input { return idleinput.ForRunner(argv) }

// routinesOffLine says why an agent's routines are off.
func routinesOffLine(argv []string) string {
	name := "its runner"
	if len(argv) > 0 {
		name = path.Base(argv[0])
	}
	return "Routines are off: Portenv doesn't know when " + name + " is idle, so it can't type a routine safely."
}

// share_login (ADR 0022 §2) isn't in the tester build: portenvd never asks
// the owner and the helper copies no credential, so an agent that lists it
// signs in itself (R-0323). These lines say so, in its column and in its
// whoami, instead of letting the file look obeyed.

// shareLoginOffLine is the owner's line in the agent's column; "" when its
// file asks for no shared sign-in.
func shareLoginOffLine(a *TeamAgent) string {
	if len(a.ShareLogin) == 0 {
		return ""
	}
	who := a.ID
	if a.Spec != nil && a.Spec.Name != "" {
		who = a.Spec.Name
	}
	return "Sharing your sign-in (" + strings.Join(a.ShareLogin, ", ") + ") isn't in this build, so nothing is shared. Sign " + who + " in from its own terminal if it needs to."
}

// shareLoginOffForAgent is the agent's line in whoami.
func shareLoginOffForAgent(a *TeamAgent) string {
	if len(a.ShareLogin) == 0 {
		return ""
	}
	return "share_login (" + strings.Join(a.ShareLogin, ", ") + ") isn't supported in this build: nothing of the owner's was shared with you. Sign in yourself in your own terminal, or ask the owner."
}

// idleOffLine says what an agent misses because Portenv doesn't know when
// its runner is idle, or "" when it does: no message wake-ups (CC-0498),
// and no routines when it has any. A runner with no idle pattern is never
// typed into, the same rule for both.
func idleOffLine(argv []string, routines bool) string {
	if runnerIdlePattern(argv) != "" {
		return ""
	}
	if routines {
		return routinesOffLine(argv) + " New messages don't wake it either; it reads them when it runs portenv inbox."
	}
	name := "its runner"
	if len(argv) > 0 {
		name = path.Base(argv[0])
	}
	return "Messages don't wake it: Portenv doesn't know when " + name + " is idle, so it reads them only when it runs portenv inbox."
}
