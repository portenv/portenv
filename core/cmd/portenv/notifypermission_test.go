// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

func runNotify(t *testing.T, sock, home, stdin string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"PORTENV_SOCKET": sock, "HOME": home}
	code := runTeam(context.Background(), []string{"notify-permission"}, func(k string) string { return env[k] }, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// R-0412 item 18: Claude Code's hooks run `portenv notify-permission` with
// their JSON on stdin. PermissionRequest sends the wait with a one-line
// summary; the events that end it send "not waiting" once, and only after
// a wait (PostToolUse runs on every tool call). It always exits 0 and
// prints nothing, so Claude Code shows its prompt as before.
func TestNotifyPermissionSendsTheWaitAndItsEndOnce(t *testing.T) {
	sock, got := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true})
	home := t.TempDir()
	steps := []struct {
		stdin string
		sent  *agentv1.PermissionWait
	}{
		{`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"/x"}}`, nil},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"npm install left-pad\nnpm test"}}`, &agentv1.PermissionWait{Waiting: true, Summary: "npm install left-pad npm test"}},
		{`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"npm install left-pad"}}`, &agentv1.PermissionWait{}},
		{`{"hook_event_name":"Stop"}`, nil},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Write","tool_input":{"file_path":"/home/builder/a.txt","content":"secret"}}`, &agentv1.PermissionWait{Waiting: true, Summary: "Write /home/builder/a.txt"}},
		{`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, &agentv1.PermissionWait{}},
		{`not json`, nil},
	}
	for _, s := range steps {
		before := len(*got)
		code, out, errOut := runNotify(t, sock, home, s.stdin)
		if code != 0 || out != "" || errOut != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", s.stdin, code, out, errOut)
		}
		switch {
		case s.sent == nil && len(*got) != before:
			t.Errorf("%s sent %v", s.stdin, (*got)[before:])
		case s.sent != nil && (len(*got) != before+1 || !proto.Equal((*got)[before].GetPermission(), s.sent)):
			t.Errorf("%s sent %v, want %v", s.stdin, (*got)[before:], s.sent)
		}
	}
}

// Portenv unreachable, or refusing: still exit 0 and silent, so the
// runner's prompt is never held up or marked as a hook error.
func TestNotifyPermissionNeverFailsTheHook(t *testing.T) {
	code, out, errOut := runNotify(t, "/nonexistent/portenv.sock", t.TempDir(), `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	if code != 0 || out != "" || errOut != "" {
		t.Errorf("exit %d, %q, %q", code, out, errOut)
	}
	sock, _ := fakeHelper(t, &agentv1.TeamCallsRequest{Line: "lead isn't running"})
	if code, out, errOut := runNotify(t, sock, t.TempDir(), `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`); code != 0 || out != "" || errOut != "" {
		t.Errorf("refused: exit %d, %q, %q", code, out, errOut)
	}
}
