// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeHelper serves one agent socket: it records each request and answers
// with answer.
func fakeHelper(t *testing.T, answer *agentv1.TeamCallsRequest) (string, *[]*agentv1.TeamCallsResponse) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "portenv.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	var got []*agentv1.TeamCallsResponse
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var req agentv1.TeamCallsResponse
			if err := protojson.Unmarshal(line, &req); err != nil {
				t.Errorf("request %q: %v", line, err)
			}
			got = append(got, &req)
			b, _ := protojson.Marshal(answer)
			_, _ = c.Write(append(b, '\n'))
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = lis.Close(); <-done })
	return sock, &got
}

func runTeamCmd(t *testing.T, sock string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"PORTENV_SOCKET": sock, "TMUX_PANE": "%7"}
	code := runTeam(context.Background(), args, func(k string) string { return env[k] }, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestTeamCommandsSendTheRightCall(t *testing.T) {
	ok := &agentv1.TeamCallsRequest{Ok: true}
	for _, tc := range []struct {
		args []string
		want *agentv1.TeamCallsResponse
	}{
		{[]string{"split", "--down", "--", "npm", "test"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Split{Split: &agentv1.Split{PaneId: "%7", Down: true, Argv: []string{"npm", "test"}}}}},
		{[]string{"split", "--pane", "%3", "--", "make"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Split{Split: &agentv1.Split{PaneId: "%3", Argv: []string{"make"}}}}},
		{[]string{"tab", "new", "--name", "tests", "--", "npm", "run", "test:watch"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Tab{Tab: &agentv1.NewTeamTab{Name: "tests", Argv: []string{"npm", "run", "test:watch"}}}}},
		{[]string{"focus", "%4"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Focus{Focus: &agentv1.PaneRef{PaneId: "%4"}}}},
		{[]string{"close", "%4"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Close{Close: &agentv1.PaneRef{PaneId: "%4"}}}},
		{[]string{"msg", "reviewer", "ready for review", "--thread", "t1"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Msg{Msg: &agentv1.Msg{To: "reviewer", Text: "ready for review", ThreadId: "t1", Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE}}}},
		{[]string{"msg", "--type", "question", "lead", "which branch?"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Msg{Msg: &agentv1.Msg{To: "lead", Text: "which branch?", Type: typesv1.MessageType_MESSAGE_TYPE_QUESTION}}}},
		{[]string{"ask-owner", "May I add a dependency?"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: "May I add a dependency?"}}}},
		{[]string{"inbox", "--wait", "--since", "c9"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Inbox{Inbox: &agentv1.Inbox{Wait: true, SinceCursor: "c9"}}}},
		{[]string{"share", "feature/x", "--text", "done"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Share{Share: &agentv1.Share{Branch: "feature/x", Text: "done"}}}},
		{[]string{"request-package", "ffmpeg", "--reason", "video tests"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_RequestPackage{RequestPackage: &agentv1.RequestPackage{Name: "ffmpeg", Reason: "video tests"}}}},
		{[]string{"whoami"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}}},
		{[]string{"status", "set", "Running the payments tests", "--progress", "3/8", "--blocked", "needs the key"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_SetStatus{SetStatus: &agentv1.SetStatus{Text: "Running the payments tests", ProgressDone: 3, ProgressTotal: 8, Blocked: "needs the key"}}}},
		{[]string{"status", "clear"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_SetStatus{SetStatus: &agentv1.SetStatus{Clear: true}}}},
		{[]string{"team", "status"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_TeamStatus{TeamStatus: &agentv1.TeamStatus{}}}},
		{[]string{"handoff", "lead", "/home/builder/design/v2", "--note", "revision 2"}, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Handoff{Handoff: &agentv1.Handoff{To: "lead", Path: "/home/builder/design/v2", Note: "revision 2"}}}},
	} {
		sock, got := fakeHelper(t, ok)
		code, _, errOut := runTeamCmd(t, sock, tc.args...)
		if code != 0 {
			t.Errorf("%q: exit %d, %s", tc.args, code, errOut)
			continue
		}
		if len(*got) != 1 || !proto.Equal((*got)[0], tc.want) {
			t.Errorf("%q sent %v, want %v", tc.args, *got, tc.want)
		}
	}
}

func TestProposeAgentChangeSendsTheFile(t *testing.T) {
	sock, got := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true})
	f := filepath.Join(t.TempDir(), "builder.md")
	if err := os.WriteFile(f, []byte("---\nid: builder\n---\nBuild.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runTeamCmd(t, sock, "propose-agent-change", f); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	p := (*got)[0].GetPropose()
	if p.GetAgentId() != "builder" || !strings.Contains(p.GetContent(), "Build.") {
		t.Errorf("sent %v", p)
	}
}

func TestARefusalIsItsLineAndExitOne(t *testing.T) {
	sock, _ := fakeHelper(t, &agentv1.TeamCallsRequest{Line: "builder has 4 panes open, its limit. Close one first."})
	code, out, errOut := runTeamCmd(t, sock, "split", "--", "make")
	if code != 1 || out != "" || strings.TrimSpace(errOut) != "portenv: builder has 4 panes open, its limit. Close one first." {
		t.Errorf("exit %d, out %q, err %q", code, out, errOut)
	}
}

func TestJSONPrintsTheResult(t *testing.T) {
	sock, _ := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true, Result: &agentv1.TeamCallsRequest_Whoami{Whoami: &agentv1.WhoAmIResult{Id: "builder", Name: "Builder"}}})
	code, out, _ := runTeamCmd(t, sock, "whoami", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var w agentv1.WhoAmIResult
	if err := protojson.Unmarshal([]byte(out), &w); err != nil || w.GetId() != "builder" {
		t.Errorf("--json printed %q (%v)", out, err)
	}
}

func TestUsageMistakesNeverReachTheHelper(t *testing.T) {
	sock, got := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true})
	for _, args := range [][]string{
		{"split"}, // no command
		{"split", "--right", "--down", "--", "x"},
		{"tab", "new", "--", "x"}, // no name
		{"tab", "open", "--name", "x", "--", "y"},
		{"msg", "reviewer"}, // no text
		{"msg", "--type", "shout", "reviewer", "hi"},
		{"ask-owner"},
		{"request-package"},
		{"request-package", "a", "b"},
		{"focus"},
		{"whoami", "extra"},
		{"split", "--bogus", "--", "x"},
		{"status"},
		{"status", "set"},
		{"status", "set", "x", "--progress", "9/8"},
		{"status", "set", "x", "--progress", "three"},
		{"status", "clear", "--blocked", "x"},
		{"team"},
		{"team", "list"},
		{"handoff", "lead"},
	} {
		if code, _, _ := runTeamCmd(t, sock, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
	if len(*got) != 0 {
		t.Errorf("sent %d calls for usage mistakes", len(*got))
	}
}

func TestOutsideAWorkspaceTheCommandsSaySo(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runTeam(context.Background(), []string{"whoami"}, func(string) string { return "" }, strings.NewReader(""), &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "inside a workspace") {
		t.Errorf("exit %d, %q", code, errOut.String())
	}
}

func TestIsTeamCommand(t *testing.T) {
	for _, c := range []string{"split", "tab", "focus", "close", "msg", "ask-owner", "inbox", "share", "propose-agent-change", "request-package", "whoami", "team", "handoff"} {
		if !isTeamCommand(c) {
			t.Errorf("%s isn't a team command", c)
		}
	}
	for _, c := range []string{"init", "app", "attach", "status"} {
		if isTeamCommand(c) {
			t.Errorf("%s is a team command", c)
		}
	}
}

// R-0260 item 10: what other agents wrote is never raw on the terminal.
func TestInboxEscapesControlCharacters(t *testing.T) {
	sock, _ := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true, Result: &agentv1.TeamCallsRequest_Inbox{Inbox: &agentv1.InboxResult{
		Messages: []*typesv1.BoardMessage{{From: "lead\x1b[31m", To: "builder", Text: "hi\x1b]52;c;cHduZWQ=\x07 there\u202e", Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE}},
	}}})
	code, out, _ := runTeamCmd(t, sock, "inbox")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, bad := range []string{"\x1b", "\x07", "\u202e"} {
		if strings.Contains(out, bad) {
			t.Errorf("printed %q raw: %q", bad, out)
		}
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "there") {
		t.Errorf("the text itself is gone: %q", out)
	}
}

func TestAHugeReplyIsRefused(t *testing.T) {
	sock, _ := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true, Result: &agentv1.TeamCallsRequest_Inbox{Inbox: &agentv1.InboxResult{
		Messages: []*typesv1.BoardMessage{{From: "lead", To: "builder", Text: strings.Repeat("x", maxTeamReply+1)}},
	}}})
	if code, _, errOut := runTeamCmd(t, sock, "inbox"); code != 1 || !strings.Contains(errOut, "too large") {
		t.Errorf("exit %d, %q", code, errOut)
	}
}

// "status" is the person's command outside a workspace, an agent's inside.
func TestStatusInsideAndOutside(t *testing.T) {
	inside := func(k string) string {
		if k == "PORTENV_SOCKET" {
			return "/run/portenv-team/builder/portenv.sock"
		}
		return ""
	}
	if !isAgentCommand("status", inside) {
		t.Error("status isn't an agent's command inside a workspace")
	}
	if isAgentCommand("status", func(string) string { return "" }) {
		t.Error("status is an agent's command outside a workspace")
	}
}
