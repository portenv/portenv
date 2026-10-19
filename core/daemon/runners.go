// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"path"
	"regexp"
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

// routinesOffLine says why an agent's routines are off.
func routinesOffLine(argv []string) string {
	name := "its runner"
	if len(argv) > 0 {
		name = path.Base(argv[0])
	}
	return "Routines are off: Portenv doesn't know when " + name + " is idle, so it can't type a routine safely."
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
