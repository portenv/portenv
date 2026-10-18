// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// newBoardServer serves the board and agent files from a temporary /home,
// running team-io in this process as work would.
func newBoardServer(t *testing.T) (*apiServer, string) {
	t.Helper()
	home := t.TempDir()
	s := &apiServer{
		cfg: Config{User: "work", UID: 1000, HomeRoot: home},
		runTeamIO: func(_ context.Context, args []string, stdin io.Reader) ([]byte, error) {
			var out strings.Builder
			err := TeamIO(args, stdin, &out)
			return []byte(out.String()), err
		},
	}
	if err := os.MkdirAll(filepath.Join(home, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	return s, home
}

func boardMsg(text string) *agentv1.AppendBoardRequest {
	return &agentv1.AppendBoardRequest{Line: &agentv1.AppendBoardRequest_Message{Message: &typesv1.BoardMessage{
		Id: "01JAB", From: "builder", To: "lead", ThreadId: "01JAB", Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE,
		Text: text, CreatedAt: timestamppb.New(time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)),
	}}}
}

func boardDecision(summary, detail string) *agentv1.AppendBoardRequest {
	return &agentv1.AppendBoardRequest{Line: &agentv1.AppendBoardRequest_Decision{Decision: &typesv1.Decision{
		Id: "01JAC", From: "builder", Kind: typesv1.DecisionKind_DECISION_KIND_ASK_OWNER, Summary: summary, Detail: detail,
		State: typesv1.DecisionState_DECISION_STATE_PENDING, CreatedAt: timestamppb.New(time.Date(2026, 10, 17, 9, 1, 0, 0, time.UTC)),
	}}}
}

type readBoardStream struct {
	agentv1.AgentService_ReadBoardServer
	ctx   context.Context
	lines []*agentv1.ReadBoardResponse
}

func (r *readBoardStream) Context() context.Context { return r.ctx }
func (r *readBoardStream) Send(l *agentv1.ReadBoardResponse) error {
	r.lines = append(r.lines, l)
	return nil
}
func (r *readBoardStream) SetHeader(metadata.MD) error { return nil }

func TestTheBoardIsWrittenAndReadBack(t *testing.T) {
	s, home := newBoardServer(t)
	ctx := context.Background()
	for _, l := range []*agentv1.AppendBoardRequest{boardMsg("ready for review\nsee the diff"), boardDecision("May I add left-pad?", "It's small.")} {
		if _, err := s.AppendBoard(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(home, "work", ".portenv", "board")
	for _, f := range []string{"messages.jsonl", "decisions.jsonl"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil || strings.Count(string(b), "\n") != 1 {
			t.Errorf("%s: %q, %v; want one line", f, b, err)
		}
	}
	st := &readBoardStream{ctx: ctx}
	if err := s.ReadBoard(&agentv1.ReadBoardRequest{}, st); err != nil {
		t.Fatal(err)
	}
	if len(st.lines) != 2 || st.lines[0].GetMessage().GetText() != "ready for review\nsee the diff" || st.lines[1].GetDecision().GetSummary() != "May I add left-pad?" {
		t.Errorf("read back: %v", st.lines)
	}
}

func TestTheHelperChecksBoardLinesAgain(t *testing.T) {
	s, home := newBoardServer(t)
	ctx := context.Background()
	for name, l := range map[string]*agentv1.AppendBoardRequest{
		"message over 8 KB":    boardMsg(strings.Repeat("x", 8<<10+1)),
		"empty message":        boardMsg(""),
		"escape sequence":      boardMsg("hi \x1b[31mred"),
		"bidi override":        boardMsg("abc\u202Edcb"),
		"invalid UTF-8":        boardMsg("bad \xff"),
		"detail over 32 KB":    boardDecision("ok", strings.Repeat("x", 32<<10+1)),
		"summary over 200":     boardDecision(strings.Repeat("s", 201), ""),
		"newline in summary":   boardDecision("one\ntwo", ""),
		"no line":              {},
		"bad sender":           {Line: &agentv1.AppendBoardRequest_Message{Message: &typesv1.BoardMessage{Id: "1", From: "../x", To: "lead", Type: typesv1.MessageType_MESSAGE_TYPE_NOTICE, Text: "hi"}}},
		"unspecified type":     {Line: &agentv1.AppendBoardRequest_Message{Message: &typesv1.BoardMessage{Id: "1", From: "lead", To: "owner", Text: "hi"}}},
		"unspecified decision": {Line: &agentv1.AppendBoardRequest_Decision{Decision: &typesv1.Decision{Id: "1", From: "lead", Summary: "x", State: typesv1.DecisionState_DECISION_STATE_PENDING}}},
	} {
		if _, err := s.AppendBoard(ctx, l); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "work", ".portenv", "board")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(home, "work", ".portenv", "board"))
		for _, e := range entries {
			if info, _ := e.Info(); info != nil && info.Size() > 0 {
				t.Errorf("%s was written", e.Name())
			}
		}
	}
}

func TestTheBoardIsNeverWrittenThroughASymlink(t *testing.T) {
	s, home := newBoardServer(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "work", ".portenv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "work", ".portenv", "board")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendBoard(context.Background(), boardMsg("hi")); err == nil {
		t.Error("the board was written through a symlinked folder")
	}
	if entries, _ := os.ReadDir(outside); len(entries) > 0 {
		t.Errorf("wrote into the symlink's target: %v", entries)
	}
	// And a symlinked file inside a real folder.
	if err := os.Remove(filepath.Join(home, "work", ".portenv", "board")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "work", ".portenv", "board"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "victim")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "work", ".portenv", "board", "messages.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendBoard(context.Background(), boardMsg("hi")); err == nil {
		t.Error("the board was written through a symlinked file")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Errorf("the symlink's target changed: %q", b)
	}
}

func TestReadAgentFiles(t *testing.T) {
	s, home := newBoardServer(t)
	project := filepath.Join(home, "work", "shop")
	agents := filepath.Join(project, ".portenv", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"builder.md": "---\nid: builder\n---\nBuild.\n",
		"lead.md":    "---\nid: lead\n---\nLead.\n",
		"notes.txt":  "not an agent file",
		"Big.md":     "bad name",
	} {
		if err := os.WriteFile(filepath.Join(agents, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(agents, "huge.md"), make([]byte, 64<<10+1), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(home, "work", ".ssh-key")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(agents, "spy.md")); err != nil {
		t.Fatal(err)
	}
	r, err := s.ReadAgentFiles(context.Background(), &agentv1.ReadAgentFilesRequest{ProjectDir: "/home/work/shop"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range r.GetFiles() {
		names = append(names, f.GetName())
		if strings.Contains(string(f.GetContent()), "secret") {
			t.Errorf("%s carried the symlink's target", f.GetName())
		}
	}
	if strings.Join(names, ",") != "builder.md,lead.md" {
		t.Errorf("files = %v, want builder.md and lead.md", names)
	}
	for _, bad := range []string{"/etc", "/home/work/../root", "/home/work", "relative", "/home/team/x", "/home/work/a/b"} {
		if _, err := s.ReadAgentFiles(context.Background(), &agentv1.ReadAgentFilesRequest{ProjectDir: bad}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("project %q: %v, want InvalidArgument", bad, err)
		}
	}
	// A project without agent files is an empty team, not an error.
	if r, err := s.ReadAgentFiles(context.Background(), &agentv1.ReadAgentFilesRequest{ProjectDir: "/home/work/none"}); err != nil || len(r.GetFiles()) != 0 {
		t.Errorf("no team: %v, %v", r, err)
	}
}
