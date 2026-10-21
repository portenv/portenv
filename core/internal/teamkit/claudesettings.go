// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Claude Code's settings for an agent (R-0412 item 17, ADR 0025 §9's
// settings row). Claude Code's auto mode sends a tool call to its
// classifier unless a permission rule decides it; the classifier blocked
// Portenv's own team commands and the agent reading its own clone as
// "Data Exfiltration". These rules allow exactly the routine teamwork:
// the team's message commands, and reading (Read, which Claude Code also
// applies to Glob and Grep) the agent's own home and the clones its grant
// lets it read. Nothing else: no network tool, no other home, no Bash
// wildcard beyond these portenv subcommands, no Edit or Write.
//
// The helper writes them to a file in the agent's run folder, which only
// the helper can write, and the runner gets it with --settings: Claude
// Code ranks command-line settings above every file the agent can edit,
// and combines allow lists, so the agent can add rules of its own but
// can't remove these.

// claudeTeamCommands are the portenv subcommands an agent runs to work
// with its team (core/cmd/portenv/team.go): messages, its inbox,
// questions to the owner, and its own identity, guide and status line.
// Commands that run other programs (split, tab), move files (handoff,
// share) or change the team (team, propose-agent-change,
// request-package) aren't here: the classifier still sees them.
var claudeTeamCommands = []string{
	"Bash(portenv msg:*)",
	"Bash(portenv inbox:*)",
	"Bash(portenv ask-owner:*)",
	"Bash(portenv whoami)",
	"Bash(portenv guide:*)",
	"Bash(portenv status set:*)",
	"Bash(portenv status clear)",
}

var (
	agentIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// cloneNameRE is a clone's folder (the workspace's name), limited to
	// characters with no meaning in a permission rule.
	cloneNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// agentHome checks /home/<agent id>; never work's.
func agentHome(p string) (string, bool) {
	id, ok := strings.CutPrefix(p, "/home/")
	if !ok || path.Clean(p) != p || !agentIDRE.MatchString(id) || id == "work" {
		return "", false
	}
	return id, true
}

// ClaudeSettings is the --settings JSON for the agent whose home is home,
// with lenses the other agents' clones its grant lets it read
// (/home/<writer>/<workspace>).
func ClaudeSettings(home string, lenses []string) ([]byte, error) {
	self, ok := agentHome(home)
	if !ok {
		return nil, fmt.Errorf("%q isn't an agent's home", home)
	}
	var reads []string
	for _, l := range lenses {
		dir, name := path.Split(l)
		writer, ok := agentHome(strings.TrimSuffix(dir, "/"))
		if !ok || writer == self || path.Clean(l) != l || !cloneNameRE.MatchString(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("%q isn't another agent's clone", l)
		}
		reads = append(reads, readRule(l))
	}
	slices.Sort(reads)
	allow := append(slices.Clone(claudeTeamCommands), readRule(home))
	allow = append(allow, slices.Compact(reads)...)
	var s struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	s.Permissions.Allow = allow
	return json.MarshalIndent(s, "", "  ")
}

// readRule is Claude Code's rule for reading everything under an
// absolute path: "//" starts a path at the filesystem's root (a single
// "/" would be relative to the settings file).
func readRule(abs string) string {
	return "Read(/" + abs + "/**)"
}
