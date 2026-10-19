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

// runnerInstructionsFiles is the runner table's user-level instructions
// file for each known runner, under the agent's home (R-0327): init writes
// the start pointer there (teamkit.PointerText: run portenv whoami and
// portenv guide first, check portenv inbox after each step, read
// ~/AGENTS.md). Claude Code reads ~/.claude/CLAUDE.md, and ~/AGENTS.md
// only as a fallback in recent versions; Codex reads $CODEX_HOME
// (~/.codex)/AGENTS.md, and AGENTS.md in its working folder (the agent's
// home). A runner not here gets no pointer; its AGENTS.md block and its
// template's instructions still say to run portenv guide first. Each
// value must be one of teamkit.PointerFiles, or init refuses it.
var runnerInstructionsFiles = map[string]string{
	"claude": ".claude/CLAUDE.md",
	"codex":  ".codex/AGENTS.md",
}

// runnerInstructionsFile is where a runner's argv reads user-level
// instructions, by its program's name, or "" for one Portenv doesn't know.
func runnerInstructionsFile(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return runnerInstructionsFiles[path.Base(argv[0])]
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
