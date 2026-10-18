// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every known way a team start fails reaches the owner as one plain line,
// what happened then what to do, with none of the raw text (R-0323).
func TestTeamStartFailuresArePlainLines(t *testing.T) {
	const again = " Click Start again; if it happens again, restart the workspace."
	fp := func(msg string) error { return status.Error(codes.FailedPrecondition, msg) }
	for _, c := range []struct {
		name string
		step teamStep
		err  error
		want string
		code codes.Code
	}{
		{"setfacl refused (2026-10-18)", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: give the project to group team: exit status 1: setfacl: /home/work/w/.git/index: Operation not permitted"),
			"Portenv wasn't allowed to share some of the project's files with the team." + again, codes.FailedPrecondition},
		{"chown refused (2026-10-18)", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: give . to the team: lchownat .: operation not permitted"),
			"Portenv wasn't allowed to share some of the project's files with the team." + again, codes.FailedPrecondition},
		{"a protected path is a link", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: .git/hooks in the project is a link; Portenv won't open the project to the team until it's a real file or folder"),
			".git/hooks in the project is a link, so Portenv won't share the project with the team. Make it a real file or folder, then click Start again.", codes.FailedPrecondition},
		{"a protected path isn't work's", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: .portenv/agents/lead.md in the project belongs to uid 2001, not to you, so an agent could have changed it. Portenv won't open the project to the team until you check it and make it yours again"),
			".portenv/agents/lead.md in the project isn't yours, so an agent could have changed it. Check it and make it yours again, then click Start again.", codes.FailedPrecondition},
		{"a protected path changed mid-walk", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: .git/config changed while Portenv opened the project to the team; try again"),
			".git/config changed while Portenv was sharing the project with the team. Click Start again.", codes.FailedPrecondition},
		{"the project folder isn't work's", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: /home/work/w belongs to uid 0, not to you"),
			"The project folder isn't yours in this workspace, so Portenv won't share it with the team. Make it yours again, then click Start again.", codes.FailedPrecondition},
		{"no ACLs", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: this workspace's files don't keep ACLs, so agents can't share the project. Portenv won't start the team"),
			"This workspace's files don't keep the permissions agents need to share the project, so Portenv won't start the team.", codes.FailedPrecondition},
		{"no project folder", stepProject,
			fp("check /home/work/w: lstat /home/work/w: no such file or directory"),
			"This workspace has no project folder (~/w) to share with the team. Make it, then click Start again.", codes.FailedPrecondition},
		{"tightening the home failed", stepProject,
			fp("make ~/.ssh private: chmod: operation not permitted"),
			"Portenv wasn't allowed to share some of the project's files with the team." + again, codes.FailedPrecondition},
		{"git config failed", stepProject,
			fp("let agents use the project's git repository: exit status 255: error: could not lock config file /etc/gitconfig"),
			"Portenv couldn't share the project with the team." + again, codes.FailedPrecondition},
		{"out of disk", stepProject,
			fp("open the project to the team: exit status 1: portenv-agent: write .git/index: no space left on device"),
			"This workspace is out of disk space. Free some space in it, then click Start again.", codes.ResourceExhausted},
		{"read-only", stepStart,
			fp("lead: useradd: cannot lock /etc/passwd; try again later: read-only file system"),
			"This workspace's files can't be changed right now. Restart the workspace, then click Start again.", codes.FailedPrecondition},
		{"the team is full", stepStart,
			fp("lead: the team is full: 8 agents. Remove one and delete its files first"),
			"This workspace already has 8 agents, the most it can run. Remove one and delete its files, then click Start again.", codes.FailedPrecondition},
		{"users not restored", stepStart,
			fp("lead: agent users weren't restored when the workspace started: read /home/users.json: unexpected end of JSON input"),
			"The agents' accounts weren't set up when the workspace started. Restart the workspace, then click Start again.", codes.FailedPrecondition},
		{"instructions too long", stepStart,
			status.Error(codes.InvalidArgument, "lead's AGENTS.md is over 64 KB"),
			"This agent's instructions are over 64 KB. Shorten them in its agent file, then click Start again.", codes.FailedPrecondition},
		{"useradd refused", stepStart,
			fp("lead: useradd: exit status 1: useradd: Permission denied."),
			"Portenv wasn't allowed to set up this agent in the workspace." + again, codes.FailedPrecondition},
		{"the terminal didn't start", stepStart,
			status.Error(codes.Internal, "start lead's terminal: tmux: exit status 1: server exited unexpectedly"),
			"Portenv couldn't start this agent's terminal." + again, codes.FailedPrecondition},
		{"the helper's channel is down", stepStart,
			status.Error(codes.Unavailable, "connection error: desc = \"transport: Error while dialing: dial unix /run/portenv.sock: connect: connection refused\""),
			"The workspace helper isn't answering. Restart the workspace, then click Start again.", codes.Unavailable},
		{"init doesn't answer", stepStart,
			fp("lead: connect to init: dial unix /run/portenv/init.sock: connect: no such file or directory"),
			"The workspace helper isn't answering. Restart the workspace, then click Start again.", codes.Unavailable},
		{"no helper", stepHelper,
			status.Error(codes.Unavailable, AgentUnavailable),
			"The workspace helper isn't answering. Restart the workspace, then click Start again.", codes.Unavailable},
		{"workspace not open", stepHelper,
			errors.New(`box "w" isn't open`), // workspace-word: ok (an internal error, never shown)
			"The workspace helper isn't answering. Restart the workspace, then click Start again.", codes.Unavailable},
		{"agent files unreadable", stepLoadTeam,
			fmt.Errorf("read the agent files: %w", status.Error(codes.Internal, "read the agent files: open /home/work/w/.portenv/agents: permission denied")),
			"Portenv couldn't read this workspace's agent files." + again, codes.FailedPrecondition},
		{"timed out", stepProject,
			context.DeadlineExceeded,
			"The workspace helper isn't answering. Restart the workspace, then click Start again.", codes.Unavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logged bytes.Buffer
			e := &teamEngine{log: slog.New(slog.NewTextHandler(&logged, nil))}
			err := e.startError("w", "lead", c.step, c.err)
			if err.Error() != c.want {
				t.Errorf("line = %q\nwant   %q", err.Error(), c.want)
			}
			if st := status.Convert(err); st.Code() != c.code || st.Message() != c.want {
				t.Errorf("status = %v %q, want %v and the line", st.Code(), st.Message(), c.code)
			}
			// What the owner sees GUIDELINES §10: one line, a sentence,
			// no internal words.
			line := err.Error()
			if strings.Contains(line, "\n") || !strings.HasSuffix(line, ".") {
				t.Errorf("not one plain sentence: %q", line)
			}
			for _, raw := range []string{"exit status", "setfacl", "lchownat", "chmod", "useradd", "uid", "rpc error", "not permitted", "Permission denied", "permission denied", "/home/", "init", "tmux", "box", "AGENTS.md", "ACL", "dial", "lstat"} {
				if strings.Contains(line, raw) {
					t.Errorf("the line shows %q: %q", raw, line)
				}
			}
			// The raw detail is in portenvd's log.
			if !strings.Contains(logged.String(), "team start failed") || !strings.Contains(logged.String(), "agent=lead") {
				t.Errorf("not logged: %s", logged.String())
			}
			if msg := status.Convert(c.err).Message(); !strings.Contains(logged.String(), strings.Fields(msg)[0]) {
				t.Errorf("the log lacks the raw detail %q: %s", msg, logged.String())
			}
			// A line already made isn't wrapped or logged twice.
			logged.Reset()
			if again := e.startError("w", "lead", stepStart, err); !errors.Is(again, err) || logged.Len() != 0 {
				t.Errorf("a plain line was handled again: %v, logged %q", again, logged.String())
			}
		})
	}
}

// A start that fails through the engine shows only the plain line, in the
// agent's row and on the board, and the raw text only in the log.
func TestAFailedStartLogsTheRawDetailOnly(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	var logged bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&logged, nil))
	approveFirstStart(t, e)
	ctx := context.Background()
	h.prepareErr = "open the project to the team: exit status 1: portenv-agent: give . to the team: lchownat .: operation not permitted"
	_, err := e.startAgent(ctx, "w", "lead")
	want := "Portenv wasn't allowed to share some of the project's files with the team. Click Start again; if it happens again, restart the workspace."
	if err == nil || err.Error() != want {
		t.Fatalf("start = %v, want %q", err, want)
	}
	if st := teamStatus(err); status.Convert(st).Message() != want {
		t.Errorf("the app gets %q", status.Convert(st).Message())
	}
	if !strings.Contains(logged.String(), "lchownat .: operation not permitted") {
		t.Errorf("the raw detail isn't in portenvd's log: %s", logged.String())
	}
}
