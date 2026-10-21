// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// newAttachServer is a workspace with builder's home (its clone in it),
// lead's home and clone, work's project and /home/team, and builder's
// files to attach.
func newAttachServer(t *testing.T) (*apiServer, string) {
	t.Helper()
	s, root, _ := newHandoffServer(t)
	for _, d := range []string{"builder/shots", "builder/shop/docs", "lead/shop", "team", "work/shop"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for f, c := range map[string]string{
		"builder/shots/login.png":     "\x89PNG pixels",
		"builder/shop/docs/design.md": "# Design",
		"lead/shop/plan.md":           "lead's plan",
		"team/shared.md":              "team",
		"work/shop/README.md":         "the owner's",
		"work/.bashrc":                "the owner's shell",
	} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s, root
}

// readSource is ReadAttachmentSource's answer, joined.
func readSource(s *apiServer, agent, path string, max int64) (size int64, data []byte, sum string, err error) {
	first := true
	err = s.readAttachmentSource(context.Background(), &agentv1.ReadAttachmentSourceRequest{AgentId: agent, Path: path, MaxBytes: max}, func(r *agentv1.ReadAttachmentSourceResponse) error {
		if first {
			size, first = r.GetSize(), false
		}
		data = append(data, r.GetData()...)
		if r.GetSha256() != "" {
			sum = r.GetSha256()
		}
		return nil
	})
	return size, data, sum, err
}

func refusedAs(err error) string {
	if err == nil {
		return ""
	}
	_, kind, _ := strings.Cut(status.Convert(err).Message(), "attach refused: ")
	return kind
}

// ADR 0030 point 2: the helper reads the agent's own file, as the agent,
// with the size first and the SHA-256 last.
func TestTheHelperReadsTheAgentsOwnFile(t *testing.T) {
	s, _ := newAttachServer(t)
	for _, p := range []string{"/home/builder/shots/login.png", "/home/builder/shop/docs/design.md"} {
		size, data, sum, err := readSource(s, "builder", p, 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		h := sha256.Sum256(data)
		if size != int64(len(data)) || sum != hex.EncodeToString(h[:]) || len(data) == 0 {
			t.Errorf("%s: size %d, %d bytes, sha %s", p, size, len(data), sum)
		}
	}
}

// Every path the agent doesn't own in its home is refused, links at every
// step included, with the kind of refusal portenvd turns into its line.
func TestTheHelperRefusesWhatIsntTheAgentsOwnFile(t *testing.T) {
	s, root := newAttachServer(t)
	b := filepath.Join(root, "builder")
	link := func(target, at string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(b, at)); err != nil {
			t.Fatal(err)
		}
	}
	link(filepath.Join(root, "work", ".bashrc"), "link-to-work-file")
	link(filepath.Join(root, "lead", "shop"), "lens")
	link(filepath.Join(b, "shots"), "shots-link")
	link("login.png", "shots/latest.png")
	if err := os.Link(filepath.Join(root, "lead", "shop", "plan.md"), filepath.Join(b, "hard-plan.md")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(b, "pipe.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "big.png"), bytes.Repeat([]byte("x"), 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file another user owns: the check is on the opened descriptor, so
	// a hard link to it in the agent's home is refused too.
	var leadIno uint64
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(root, "lead", "shop", "plan.md"), &st); err != nil {
		t.Fatal(err)
	}
	leadIno = uint64(st.Ino) //nolint:unconvert // uint64 on Linux, not on every platform
	prev := attachOwned
	attachOwned = func(st *unix.Stat_t) bool { return uint64(st.Ino) != leadIno && prev(st) } //nolint:unconvert // as above
	t.Cleanup(func() { attachOwned = prev })

	for _, c := range []struct{ path, want string }{
		{"/home/builder/link-to-work-file", "link"},
		{"/home/builder/lens/plan.md", "link"},
		{"/home/builder/shots-link/login.png", "link"},
		{"/home/builder/shots/latest.png", "link"},
		{"/home/builder/hard-plan.md", "not-own"},
		{"/home/builder/shots", "folder"},
		{"/home/builder/pipe.md", "special"},
		{"/home/builder/missing.png", "missing"},
		{"/home/builder/nowhere/x.png", "missing"},
		{"/home/builder/big.png", "too-large"},
		{"/home/work/shop/README.md", "outside"},
		{"/home/work/.bashrc", "outside"},
		{"/home/team/shared.md", "outside"},
		{"/home/lead/shop/plan.md", "outside"},
		{"/home/builder/../lead/shop/plan.md", "outside"},
		{"/home/builder/", "outside"},
		{"/etc/shadow", "outside"},
		{"shots/login.png", "outside"},
	} {
		_, data, _, err := readSource(s, "builder", c.path, 1024)
		if got := refusedAs(err); got != c.want {
			t.Errorf("%s: refused as %q (%v), want %q", c.path, got, err, c.want)
		}
		if len(data) > 0 {
			t.Errorf("%s: %d bytes came back", c.path, len(data))
		}
	}
	// The mutation: with the ownership check gone, the hard link would be
	// read; the test above is what catches it.
	attachOwned = func(*unix.Stat_t) bool { return true }
	if _, _, _, err := readSource(s, "builder", "/home/builder/hard-plan.md", 1024); err != nil {
		t.Errorf("the mutation check's own premise: %v", err)
	}
}

// The read takes at most the limit plus one byte, so a file that grows
// past the limit while it's read is refused, not trimmed.
func TestAFileGrowingPastTheLimitIsRefused(t *testing.T) {
	var out bytes.Buffer
	err := copyAtMost(&out, strings.NewReader(strings.Repeat("x", 100)), 10)
	if !strings.Contains(errString(err), "attach refused: too-large") {
		t.Errorf("err = %v", err)
	}
	if out.Len() > 11 {
		t.Errorf("read %d bytes, want at most the limit plus one", out.Len())
	}
	out.Reset()
	if err := copyAtMost(&out, strings.NewReader("1234567890"), 10); err != nil || out.String() != "1234567890" {
		t.Errorf("exactly the limit: %q, %v", out.String(), err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The read runs as the agent: its uid, its own group only.
func TestTheReadRunsAsTheAgent(t *testing.T) {
	s, _ := newAttachServer(t)
	run := s.runHomeIO
	var got []string
	s.runHomeIO = func(ctx context.Context, uid int, g []uint32, args []string, stdin io.Reader) ([]byte, error) {
		got = append(got, args[0])
		if uid != 2001 || len(g) != 1 || g[0] != 2001 {
			t.Errorf("ran as uid %d, groups %v", uid, g)
		}
		return run(ctx, uid, g, args, stdin)
	}
	if _, _, _, err := readSource(s, "builder", "/home/builder/shots/login.png", 1<<20); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "attach-read" {
		t.Errorf("team-io calls %v", got)
	}
}

// ADR 0030 point 8: a dropped file's path is typed at a shell prompt and
// pasted into the agent's runner, quoted and never with Enter; anywhere
// else nothing is typed.
func TestInsertingADroppedPath(t *testing.T) {
	const p = "/home/builder/inbox/owner/20261019T142800Z/SPEC.md"
	for _, c := range []struct {
		name  string
		state dropPaneState
		main  bool
		want  typesv1.InsertHow
	}{
		{"at a prompt", dropPaneState{known: true, last: markPrompt}, false, typesv1.InsertHow_INSERT_HOW_TYPED},
		{"at the input mark", dropPaneState{known: true, last: markInput}, false, typesv1.InsertHow_INSERT_HOW_TYPED},
		{"a command running", dropPaneState{known: true, last: markCommand}, false, typesv1.InsertHow_INSERT_HOW_NONE},
		{"a command done, no new prompt", dropPaneState{known: true, last: markDone}, false, typesv1.InsertHow_INSERT_HOW_NONE},
		{"no marks", dropPaneState{known: true}, false, typesv1.InsertHow_INSERT_HOW_NONE},
		{"a full-screen program over a prompt", dropPaneState{known: true, last: markPrompt, alt: true}, false, typesv1.InsertHow_INSERT_HOW_NONE},
		{"the runner with bracketed paste", dropPaneState{known: true, bracketed: true}, true, typesv1.InsertHow_INSERT_HOW_PASTED},
		{"bracketed paste, not the runner", dropPaneState{known: true, bracketed: true}, false, typesv1.InsertHow_INSERT_HOW_NONE},
		{"the runner without bracketed paste", dropPaneState{known: true}, true, typesv1.InsertHow_INSERT_HOW_NONE},
		{"no parser follows the pane", dropPaneState{}, true, typesv1.InsertHow_INSERT_HOW_NONE},
	} {
		if got := dropHow(c.state, c.main); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	for in, want := range map[string]string{
		p:                                     "'" + p + "' ",
		"/home/builder/inbox/owner/T/it's.md": `'/home/builder/inbox/owner/T/it'\''s.md' `,
		"/home/builder/inbox/owner/T/a b.md":  "'/home/builder/inbox/owner/T/a b.md' ",
		"/home/builder/inbox/owner/T/rés“umé”.md": "'/home/builder/inbox/owner/T/rés“umé”.md' ",
		"/home/builder/inbox/owner/T/; rm -rf ~":  "'/home/builder/inbox/owner/T/; rm -rf ~' ",
	} {
		got, err := quoteDropped(in)
		if err != nil || got != want {
			t.Errorf("quote %q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"/home/builder/inbox/owner/T/a\nb.md", "/home/builder/inbox/owner/T/a\x1b[201~b", "/home/builder/inbox/owner/T/a\rb", "/home/builder/inbox/owner/T/\u202eb", "/home/builder/inbox/owner/T/a\x00"} {
		if _, err := quoteDropped(bad); err == nil {
			t.Errorf("quote %q accepted", bad)
		}
	}
}

// The RPC: only the agent's own pane, only a path in its ~/inbox/owner/,
// the quoted path and never Enter.
func TestInsertDroppedPathTypesNoEnter(t *testing.T) {
	s := &apiServer{}
	var mu sync.Mutex
	var calls [][]string
	a := &agentServer{user: AgentServerSpec{ID: "builder"}, mainPane: "%1", tmux: func(_ context.Context, args ...string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, args)
		if args[0] == "list-panes" {
			return "@1 %1\n@2 %2\n", nil
		}
		return "", nil
	}}
	s.agents.by = map[string]*agentServer{"builder": a}
	state := map[string]dropPaneState{"%1": {known: true, bracketed: true}, "%2": {known: true, last: markPrompt}}
	s.dropState = func(_ *agentServer, pane string) dropPaneState { return state[pane] }
	ctx := context.Background()
	const p = "/home/builder/inbox/owner/20261019T142800Z/SPEC.md"
	sent := func() string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, c := range calls {
			if c[0] != "list-panes" {
				out = append(out, strings.Join(c, " "))
			}
		}
		calls = nil
		return strings.Join(out, " | ")
	}
	r, err := s.InsertDroppedPath(ctx, &agentv1.InsertDroppedPathRequest{AgentId: "builder", PaneId: "%2", BoxPath: p})
	if err != nil || r.GetHow() != typesv1.InsertHow_INSERT_HOW_TYPED {
		t.Fatalf("at a prompt: %v, %v", r, err)
	}
	if got := sent(); got != "send-keys -t %2 -l -- '"+p+"' " || strings.Contains(got, "Enter") {
		t.Errorf("typed: %q", got)
	}
	r, err = s.InsertDroppedPath(ctx, &agentv1.InsertDroppedPathRequest{AgentId: "builder", PaneId: "%1", BoxPath: p})
	if err != nil || r.GetHow() != typesv1.InsertHow_INSERT_HOW_PASTED {
		t.Fatalf("the runner: %v, %v", r, err)
	}
	if got := sent(); !strings.Contains(got, "paste-buffer -p") || strings.Contains(got, "Enter") || !strings.Contains(got, "'"+p+"' ") {
		t.Errorf("pasted: %q", got)
	}
	state["%2"] = dropPaneState{known: true, last: markCommand}
	if r, err := s.InsertDroppedPath(ctx, &agentv1.InsertDroppedPathRequest{AgentId: "builder", PaneId: "%2", BoxPath: p}); err != nil || r.GetHow() != typesv1.InsertHow_INSERT_HOW_NONE {
		t.Errorf("a command running: %v, %v", r, err)
	}
	if got := sent(); got != "" {
		t.Errorf("typed into a running command: %q", got)
	}
	for _, bad := range []*agentv1.InsertDroppedPathRequest{
		{AgentId: "builder", PaneId: "%9", BoxPath: p},
		{AgentId: "builder", PaneId: "%2", BoxPath: "/home/lead/inbox/owner/T/SPEC.md"},
		{AgentId: "builder", PaneId: "%2", BoxPath: "/home/builder/notes/SPEC.md"},
		{AgentId: "builder", PaneId: "%2", BoxPath: "/home/builder/inbox/owner/../../x"},
		{AgentId: "builder", PaneId: "%2", BoxPath: "/home/builder/inbox/owner/T/a\nrm -rf ~"},
		{AgentId: "builder", PaneId: "%2", BoxPath: "/home/builder/inbox/owner/T/a\x1b[201~"},
		{AgentId: "nobody", PaneId: "%2", BoxPath: p},
	} {
		state["%2"] = dropPaneState{known: true, last: markPrompt}
		if r, err := s.InsertDroppedPath(ctx, bad); err == nil && r.GetHow() != typesv1.InsertHow_INSERT_HOW_NONE {
			t.Errorf("%v: %v", bad, r)
		}
		if got := sent(); got != "" {
			t.Errorf("%v typed %q", bad, got)
		}
	}
}

func withAttachments(l *agentv1.AppendBoardRequest, as ...*typesv1.Attachment) *agentv1.AppendBoardRequest {
	if m := l.GetMessage(); m != nil {
		m.Attachments = as
	} else {
		l.GetDecision().Attachments = as
	}
	return l
}

func goodAttachment() *typesv1.Attachment {
	return &typesv1.Attachment{Id: "01JAB000000000000000000000", Name: "login-390.png", MediaType: "image/png", Size: 1200,
		Sha256: strings.Repeat("ab", 32), Width: 390, Height: 844, InboxPath: "inbox/builder/20261019T142800Z/login-390.png"}
}

// The helper checks a line's attachments again, whatever portenvd did:
// at most 5, plain names and paths, known types, a hex SHA-256.
func TestTheHelperChecksAttachmentsOnBoardLines(t *testing.T) {
	s, _ := newBoardServer(t)
	ctx := context.Background()
	if _, err := s.AppendBoard(ctx, withAttachments(boardMsg("look"), goodAttachment())); err != nil {
		t.Fatalf("a good attachment: %v", err)
	}
	if _, err := s.AppendBoard(ctx, withAttachments(boardDecision("Ship?", "Ship?"), goodAttachment())); err != nil {
		t.Fatalf("on a decision: %v", err)
	}
	bad := func(f func(a *typesv1.Attachment)) *typesv1.Attachment { a := goodAttachment(); f(a); return a }
	six := []*typesv1.Attachment{goodAttachment(), goodAttachment(), goodAttachment(), goodAttachment(), goodAttachment(), goodAttachment()}
	for name, l := range map[string]*agentv1.AppendBoardRequest{
		"six":                withAttachments(boardMsg("look"), six...),
		"six on a decision":  withAttachments(boardDecision("Ship?", ""), six...),
		"escape in a name":   withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.Name = "a\x1b[2J.png" })),
		"slash in a name":    withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.Name = "../a.png" })),
		"empty name":         withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.Name = "" })),
		"svg":                withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.MediaType = "image/svg+xml" })),
		"bad id":             withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.Id = "../x" })),
		"bad sha":            withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.Sha256 = "nope" })),
		"negative size":      withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.Size = -1 })),
		"inbox path escapes": withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.InboxPath = "../work/x" })),
		"absolute inbox":     withAttachments(boardMsg("look"), bad(func(a *typesv1.Attachment) { a.InboxPath = "/home/lead/x" })),
	} {
		if _, err := s.AppendBoard(ctx, l); status.Code(err) == 0 {
			t.Errorf("%s: written", name)
		}
	}
}

// ADR 0030 point 6: ReadBoard says the board was read whole only when no
// line was skipped, so portenvd never sweeps from a partial read.
func TestReadBoardSaysCompleteOnlyWhenNothingWasSkipped(t *testing.T) {
	s, home := newBoardServer(t)
	ctx := context.Background()
	if _, err := s.AppendBoard(ctx, withAttachments(boardMsg("look"), goodAttachment())); err != nil {
		t.Fatal(err)
	}
	st := &readBoardStream{ctx: ctx}
	if err := s.ReadBoard(&agentv1.ReadBoardRequest{}, st); err != nil {
		t.Fatal(err)
	}
	if n := len(st.lines); n != 2 || !st.lines[1].GetComplete() || st.lines[1].GetLine() != nil || st.lines[0].GetComplete() {
		t.Fatalf("a whole board: %v", st.lines)
	}
	f, err := os.OpenFile(filepath.Join(home, "work", ".portenv", "board", "messages.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{not json\n")
	_ = f.Close()
	st = &readBoardStream{ctx: ctx}
	if err := s.ReadBoard(&agentv1.ReadBoardRequest{}, st); err != nil {
		t.Fatal(err)
	}
	for _, l := range st.lines {
		if l.GetComplete() {
			t.Errorf("a board with a damaged line read as complete: %v", st.lines)
		}
	}
}

// ADR 0030 point 2: a teammate's copy lands through WriteHomeFiles, as the
// recipient with its own group only, mode 0600, its own, new files only,
// and no inbox folder is made through a link the recipient planted.
func TestAnInboxCopyIsTheRecipientsOwn0600File(t *testing.T) {
	s, root := newAttachServer(t)
	run := s.runHomeIO
	var as []string
	s.runHomeIO = func(ctx context.Context, uid int, g []uint32, args []string, stdin io.Reader) ([]byte, error) {
		as = append(as, fmt.Sprintf("%s %d %v", args[0], uid, g))
		return run(ctx, uid, g, args, stdin)
	}
	const p = "inbox/builder/20261019T142800Z/login.png"
	write := func(path string) error {
		return s.WriteHomeFiles(&writeHomeStream{in: []*agentv1.WriteHomeFilesRequest{{AgentId: "lead", HomeQuotaBytes: 1 << 20, Path: path, Data: []byte("png"), Last: true}}})
	}
	if err := write(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "lead", p))
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if fi.Mode().Perm() != 0o600 || int(st.Uid) != os.Getuid() {
		t.Errorf("the copy is %o, uid %d", fi.Mode().Perm(), st.Uid)
	}
	if want := "write-home 2002 [2002]"; len(as) != 1 || as[0] != want {
		t.Errorf("ran %v, want %q", as, want)
	}
	if err := write(p); err == nil {
		t.Error("an existing file was overwritten")
	}
	if err := os.Symlink(filepath.Join(root, "work"), filepath.Join(root, "lead", "inbox", "reviewer")); err != nil {
		t.Fatal(err)
	}
	if err := write("inbox/reviewer/20261019T142800Z/x.png"); err == nil {
		t.Error("a copy went through the recipient's link")
	}
	if _, err := os.Stat(filepath.Join(root, "work", "20261019T142800Z")); err == nil {
		t.Error("a folder was made outside the recipient's home")
	}
}
