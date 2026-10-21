// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// runnerIdlePatterns is the runner table's idle-input line for each known
// runner (R-0258): the helper types a routine into an agent's pane only
// when its last line matches, there's no question on screen, its output
// has been quiet for 5 s and it isn't on the alternate screen. Runners
// not here get no routines.
//
// Stand-ins until each is checked against the real CLI and labelled.
var runnerIdlePatterns = map[string]string{
	// Claude Code's empty input box: "> " alone, maybe inside "│ … │".
	"claude": `^\s*[│|]?\s*>\s*[│|]?\s*$`,
	// Codex's empty prompt.
	"codex": `^\s*›\s*$`,
}

// runnerIdlePattern is the pattern for a runner's argv, by its program's
// name, or "" for one Portenv doesn't know.
func runnerIdlePattern(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	p := runnerIdlePatterns[path.Base(argv[0])]
	if _, err := regexp.Compile(p); err != nil {
		return ""
	}
	return p
}

// runnerTrustSetting is the registry's folder-trust setting for a
// runner's argv, by its program's name (R-0411), or "".
func runnerTrustSetting(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return teamkit.TrustSettingFor(path.Base(argv[0]))
}

// runnerSettings is the settings file and its flag for an agent's runner
// (R-0412 item 17): for Claude Code, permission rules for routine
// teamwork, with the clones the agent may read. A runner without a
// settings flag, or settings that can't be made, gets none.
func runnerSettings(argv []string, agentID string, readers []*agentv1.CloneReaders, workspace string) ([]byte, string, error) {
	if len(argv) == 0 {
		return nil, "", nil
	}
	flag := teamkit.SettingsFlagFor(path.Base(argv[0]))
	if flag == "" {
		return nil, "", nil
	}
	b, err := teamkit.ClaudeSettings("/home/"+agentID, lensPaths(readers, agentID, workspace))
	if err != nil {
		return nil, "", err
	}
	return b, flag, nil
}

// lensPaths are the clones agentID's grant lets it read.
func lensPaths(readers []*agentv1.CloneReaders, agentID, workspace string) []string {
	var out []string
	for _, cr := range readers {
		if slices.Contains(cr.GetReaders(), agentID) {
			out = append(out, "/home/"+cr.GetWriter()+"/"+workspace)
		}
	}
	return out
}

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
