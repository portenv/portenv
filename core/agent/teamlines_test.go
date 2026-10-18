// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Each refusal the helper makes, as portenvd receives it (team-io's words
// inside the command's failure), has the owner's plain line, with no path
// prefix, uid or exit status left in it (R-0323).
func TestTeamStartLineFindsEveryRefusal(t *testing.T) {
	wrapped := func(err error) string {
		return cmdError("open the project to the team", []byte("portenv-agent: "+err.Error()), errors.New("exit status 1")).Error()
	}
	for _, c := range []struct {
		raw, want string
	}{
		{wrapped(errNoACL), "This workspace's files don't keep the permissions agents need to share the project, so Portenv won't start the team."},
		{wrapped(refuseLink(".git/hooks")), ".git/hooks in the project is a link, so Portenv won't share the project with the team. Make it a real file or folder, then click Start again."},
		{wrapped(refuseNotYours(".portenv/agents/lead.md", 2001)), ".portenv/agents/lead.md in the project isn't yours, so an agent could have changed it. Check it and make it yours again, then click Start again."},
		{wrapped(refuseChanged(".git/config")), ".git/config changed while Portenv was sharing the project with the team. Click Start again."},
		{wrapped(refuseTopNotYours("/home/work/acme", 0)), "The project folder isn't yours in this workspace, so Portenv won't share it with the team. Make it yours again, then click Start again."},
		{fmt.Sprintf("lead: %v", fmt.Errorf("%w: %d agents. Remove one and delete its files first", ErrTeamFull, 8)), "This workspace already has 8 agents, the most it can run. Remove one and delete its files, then click Start again."},
		{fmt.Sprintf("lead: %v", fmt.Errorf("%s: %w", restoreRefusal, errors.New("bad json"))), "The agents' accounts weren't set up when the workspace started. Restart the workspace, then click Start again."},
		{fmt.Sprintf("%s%s%d KB", "lead", agentsMDRefusal, maxAgentsMD>>10), "This agent's instructions are over 64 KB. Shorten them in its agent file, then click Start again."},
	} {
		got, ok := TeamStartLine(c.raw)
		if !ok || got != c.want {
			t.Errorf("TeamStartLine(%q)\n = %q, %v\nwant %q", c.raw, got, ok, c.want)
		}
		for _, bad := range []string{"exit status", "portenv-agent", "uid", "/home/"} {
			if strings.Contains(got, bad) {
				t.Errorf("the line shows %q: %q", bad, got)
			}
		}
	}
	for _, raw := range []string{
		"give . to the team: lchownat .: operation not permitted",
		"start lead's terminal: tmux exited",
		"",
	} {
		if got, ok := TeamStartLine(raw); ok {
			t.Errorf("TeamStartLine(%q) = %q, want no line (portenvd's own table handles it)", raw, got)
		}
	}
}
