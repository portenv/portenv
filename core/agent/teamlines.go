// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"fmt"
	"strings"
)

// The refusals a team start makes on purpose. Their text names paths and
// uids, which portenvd keeps in its log; TeamStartLine turns each into the
// one plain line the owner sees (GUIDELINES §10, R-0323). Each refusal's
// text is made only here, so the two can't drift apart.

const (
	linkRefusal     = " in the project is a link; Portenv won't open the project to the team until it's a real file or folder"
	notYoursRefusal = ", not to you, so an agent could have changed it. Portenv won't open the project to the team until you check it and make it yours again"
	changedRefusal  = " changed while Portenv opened the project to the team; try again"
	topNotYours     = ", not to you"
)

func refuseLink(p string) error { return errors.New(p + linkRefusal) }

func refuseNotYours(p string, uid int) error {
	return fmt.Errorf("%s in the project belongs to uid %d%s", p, uid, notYoursRefusal)
}

func refuseChanged(p string) error { return errors.New(p + changedRefusal) }

func refuseTopNotYours(project string, uid int) error {
	return fmt.Errorf("%s belongs to uid %d%s", project, uid, topNotYours)
}

// TeamStartLine is the owner's plain line for a refusal the helper made
// while opening the project or starting an agent, found in raw (which may
// carry the command and exit status around it), and false for anything
// else.
func TeamStartLine(raw string) (string, bool) {
	switch {
	case strings.Contains(raw, errNoACL.Error()):
		return "This workspace's files don't keep the permissions agents need to share the project, so Portenv won't start the team.", true
	case strings.Contains(raw, linkRefusal):
		return lastPart(raw, linkRefusal) + " in the project is a link, so Portenv won't share the project with the team. Make it a real file or folder, then click Start again.", true
	case strings.Contains(raw, notYoursRefusal):
		p := lastPart(raw, notYoursRefusal)
		if i := strings.LastIndex(p, " in the project belongs to uid "); i >= 0 {
			p = p[:i]
		}
		return p + " in the project isn't yours, so an agent could have changed it. Check it and make it yours again, then click Start again.", true
	case strings.Contains(raw, changedRefusal):
		return lastPart(raw, changedRefusal) + " changed while Portenv was sharing the project with the team. Click Start again.", true
	case strings.Contains(raw, ErrTeamFull.Error()):
		return fmt.Sprintf("This workspace already has %d agents, the most it can run. Remove one and delete its files, then click Start again.", AgentUIDLast-AgentUIDFirst+1), true
	case strings.Contains(raw, restoreRefusal):
		return "The agents' accounts weren't set up when the workspace started. Restart the workspace, then click Start again.", true
	case strings.Contains(raw, agentsMDRefusal):
		return fmt.Sprintf("This agent's instructions are over %d KB. Shorten them in its agent file, then click Start again.", maxAgentsMD>>10), true
	case strings.HasSuffix(strings.TrimSpace(raw), topNotYours) && strings.Contains(raw, " belongs to uid "):
		return "The project folder isn't yours in this workspace, so Portenv won't share it with the team. Make it yours again, then click Start again.", true
	}
	return "", false
}

const (
	restoreRefusal  = "agent users weren't restored when the workspace started"
	agentsMDRefusal = "'s AGENTS.md is over "
)

// lastPart is the text just before suffix, after the last ": " that wraps
// it (the command, its exit status and the program's name).
func lastPart(raw, suffix string) string {
	s := raw[:strings.LastIndex(raw, suffix)]
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return strings.TrimSpace(s)
}
