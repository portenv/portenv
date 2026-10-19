// SPDX-License-Identifier: Apache-2.0

package teamkit

import "slices"

// The start pointer (R-0327): agents must know Portenv's commands from
// their first step, not by chance. The helper writes AGENTS.md in the
// agent's home, but a runner reads its own user-level file (Claude Code
// reads ~/.claude/CLAUDE.md, and AGENTS.md only as a fallback in recent
// versions), so at each start init writes a short, fixed pointer there,
// root's and 0444, and leaves a file the agent wrote alone.

// PointerMarker is the pointer's first line. A file is Portenv's own, and
// replaced at each start, only when root owns it and it starts with this
// line; any other file at that path is the agent's and is kept.
const PointerMarker = "<!-- Portenv writes this file at each start, read-only. Your own notes go elsewhere. -->"

// PointerFiles are the user-level instruction files init may write, as
// paths under the agent's home: the runner table (core/daemon/runners.go)
// names one per known runner, and init refuses any other path.
var PointerFiles = []string{".claude/CLAUDE.md", ".codex/AGENTS.md"}

// IsPointerFile reports whether rel is one of PointerFiles.
func IsPointerFile(rel string) bool { return slices.Contains(PointerFiles, rel) }

// FirstStepLine is the fixed line every agent gets at its start: in its
// pointer file, or typed once into its pane when it has none. id is a
// checked agent id.
func FirstStepLine(id string) string {
	return "You are " + id + " in a Portenv team. Run `portenv whoami` and `portenv guide` first. Check `portenv inbox` after each step."
}

// PointerText is the whole pointer for rel: fixed text, the agent's id the
// only variable. Claude Code imports ~/AGENTS.md (an @ import in a
// user-level file loads with no approval dialog); Codex has no imports, so
// its pointer names the file.
func PointerText(id, rel string) string {
	s := PointerMarker + "\n\n" + FirstStepLine(id) + "\n\n"
	if rel == ".claude/CLAUDE.md" {
		return s + "Your instructions from the team's agent file are in ~/AGENTS.md:\n\n@~/AGENTS.md\n"
	}
	return s + "Your instructions from the team's agent file are in ~/AGENTS.md. Read it before you start.\n"
}
