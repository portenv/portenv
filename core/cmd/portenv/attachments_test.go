// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// ADR 0030: --attach on msg and ask-owner, up to 5 times, each made an
// absolute, clean path (as handoff's); msg may then have no text.
func TestAttachSendsCleanAbsolutePaths(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sock, got := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true, Result: &agentv1.TeamCallsRequest_Message{Message: &typesv1.BoardMessage{}}})
	if code, _, errOut := runTeamCmd(t, sock, "msg", "lead", "look", "--attach", "/home/builder/shots/../shots/a.png", "--attach", "notes.md"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := []string{"/home/builder/shots/a.png", filepath.Join(wd, "notes.md")}
	if p := (*got)[0].GetMsg().GetAttachPaths(); !slices.Equal(p, want) {
		t.Errorf("sent %v, want %v", p, want)
	}
	if code, _, errOut := runTeamCmd(t, sock, "msg", "lead", "--attach", "/home/builder/a.png"); code != 0 {
		t.Fatalf("attach only: exit %d: %s", code, errOut)
	}
	if m := (*got)[1].GetMsg(); m.GetText() != "" || m.GetTo() != "lead" || len(m.GetAttachPaths()) != 1 {
		t.Errorf("attach only sent %v", m)
	}
	if code, _, errOut := runTeamCmd(t, sock, "ask-owner", "Ship?", "--choice", "Ship it", "--choice", "Wait", "--attach", "/home/builder/plan.md"); code != 0 {
		t.Fatalf("ask-owner: exit %d: %s", code, errOut)
	}
	if a := (*got)[2].GetAskOwner(); !slices.Equal(a.GetAttachPaths(), []string{"/home/builder/plan.md"}) || len(a.GetChoices()) != 2 {
		t.Errorf("ask-owner sent %v", a)
	}
	six := []string{"msg", "lead", "x"}
	for range 6 {
		six = append(six, "--attach", "/home/builder/a.png")
	}
	for _, args := range [][]string{six, {"msg", "lead", "--attach"}, {"msg", "lead"}, {"handoff", "lead", "/home/builder/a", "--attach", "/x"}} {
		if code, _, _ := runTeamCmd(t, sock, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
	if len(*got) != 3 {
		t.Errorf("%d calls sent; usage mistakes must send none", len(*got))
	}
}

// inbox shows each attachment with its inbox path and SHA-256, escaped;
// whoami says how many were sent today.
func TestInboxAndWhoamiShowAttachments(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sock, _ := fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true, Result: &agentv1.TeamCallsRequest_Inbox{Inbox: &agentv1.InboxResult{Messages: []*typesv1.BoardMessage{{
		From: "builder", To: "lead", Text: "look", Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE,
		Attachments: []*typesv1.Attachment{{Name: "login\x1b[2J.png", MediaType: "image/png", Size: 1234, Sha256: sum, InboxPath: "inbox/builder/20261019T142800Z/login.png"}},
	}}}}})
	code, out, _ := runTeamCmd(t, sock, "inbox")
	if code != 0 || !strings.Contains(out, "~/inbox/builder/20261019T142800Z/login.png") || !strings.Contains(out, sum) || strings.Contains(out, "\x1b") {
		t.Errorf("inbox: %d %q", code, out)
	}
	sock, _ = fakeHelper(t, &agentv1.TeamCallsRequest{Ok: true, Result: &agentv1.TeamCallsRequest_Whoami{Whoami: &agentv1.WhoAmIResult{Id: "builder", AttachmentsToday: 3, AttachmentsPerDay: 100}}})
	if code, out, _ := runTeamCmd(t, sock, "whoami"); code != 0 || !strings.Contains(out, "Attachments today: 3 of 100") {
		t.Errorf("whoami: %d %q", code, out)
	}
}
