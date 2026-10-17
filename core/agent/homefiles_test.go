// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

func newHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "builder")
	if err := os.MkdirAll(filepath.Join(home, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "notes", "a.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestHomeRelativePaths(t *testing.T) {
	for _, p := range []string{"a.md", "notes/a.md", "notes/sub/b.txt"} {
		if _, err := homeRel(p, false); err != nil {
			t.Errorf("homeRel(%q) = %v", p, err)
		}
	}
	if _, err := homeRel("", true); err != nil {
		t.Errorf("the home itself as a folder: %v", err)
	}
	for _, p := range []string{"", "/etc/passwd", "../work/.ssh", "notes/../../x", "./a", "a//b", "a/", ".", "..", "a\x00b", "notes/./a"} {
		if _, err := homeRel(p, false); err == nil {
			t.Errorf("homeRel(%q) accepted", p)
		}
	}
}

func TestListAndReadInTheHome(t *testing.T) {
	home := newHome(t)
	var out bytes.Buffer
	if err := TeamIO([]string{"list-home", home, "notes"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"path":"notes/a.md"`) {
		t.Errorf("list: %s", out.String())
	}
	out.Reset()
	if err := TeamIO([]string{"read-home", home, "notes/a.md"}, nil, &out); err != nil || out.String() != "hello" {
		t.Errorf("read: %q, %v", out.String(), err)
	}
}

func TestNothingLeavesTheHomeThroughASymlink(t *testing.T) {
	home := newHome(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("owner's"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The agent plants links: a folder link and a file link.
	if err := os.Symlink(outside, filepath.Join(home, "drop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(home, "notes", "s")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, args := range [][]string{
		{"read-home", home, "notes/s"},
		{"read-home", home, "drop/secret"},
		{"list-home", home, "drop"},
	} {
		out.Reset()
		if err := TeamIO(args, nil, &out); err == nil {
			t.Errorf("%v followed a symlink: %q", args, out.String())
		}
	}
	// The owner's drag and drop never lands outside the home.
	for _, p := range []string{"drop/new.txt", "notes/s"} {
		if err := TeamIO([]string{"write-home", home, p, "1000000"}, strings.NewReader("dropped"), &out); err == nil {
			t.Errorf("write %s followed a symlink", p)
		}
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Errorf("something landed outside the home: %v", entries)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "owner's" {
		t.Errorf("the link's target changed: %q", b)
	}
}

func TestWriteMakesANewFileOnly(t *testing.T) {
	home := newHome(t)
	var out bytes.Buffer
	if err := TeamIO([]string{"write-home", home, "notes/new/deep.txt", "1000000"}, strings.NewReader("brief"), &out); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "notes", "new", "deep.txt")); err != nil || string(b) != "brief" {
		t.Errorf("written: %q, %v", b, err)
	}
	if err := TeamIO([]string{"write-home", home, "notes/a.md", "1000000"}, strings.NewReader("over"), &out); err == nil {
		t.Error("an existing file was overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(home, "notes", "a.md")); string(b) != "hello" {
		t.Errorf("a.md = %q", b)
	}
}

func TestWritePastTheQuotaIsRefused(t *testing.T) {
	home := newHome(t) // 5 bytes in it
	var out bytes.Buffer
	err := TeamIO([]string{"write-home", home, "big.bin", "10"}, strings.NewReader("123456"), &out)
	if !errors.Is(err, errHomeFull) {
		t.Fatalf("write past the quota: %v, want errHomeFull", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "big.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a partial file was left: %v", err)
	}
	if err := TeamIO([]string{"write-home", home, "ok.bin", "10"}, strings.NewReader("12345"), &out); err != nil {
		t.Errorf("a write that fits: %v", err)
	}
}

func newHomeServer(t *testing.T) (*apiServer, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".portenv", "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".portenv", "team", "users.json"), []byte(`{"version":1,"users":[{"id":"builder","uid":2001}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "builder"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &apiServer{
		cfg: Config{User: "work", UID: 1000, HomeRoot: root},
		runHomeIO: func(_ context.Context, uid int, args []string, stdin io.Reader) ([]byte, error) {
			if uid != 2001 {
				return nil, fmt.Errorf("ran as uid %d, want builder's 2001", uid)
			}
			var out bytes.Buffer
			err := TeamIO(args, stdin, &out)
			return out.Bytes(), err
		},
	}
	return s, root
}

type writeHomeStream struct {
	agentv1.AgentService_WriteHomeFilesServer
	in   []*agentv1.WriteHomeFilesRequest
	resp *agentv1.WriteHomeFilesResponse
}

func (w *writeHomeStream) Context() context.Context { return context.Background() }
func (w *writeHomeStream) Recv() (*agentv1.WriteHomeFilesRequest, error) {
	if len(w.in) == 0 {
		return nil, io.EOF
	}
	m := w.in[0]
	w.in = w.in[1:]
	return m, nil
}
func (w *writeHomeStream) SendAndClose(r *agentv1.WriteHomeFilesResponse) error {
	w.resp = r
	return nil
}

func TestTheAgentFilesCallsRunAsTheAgentInItsHome(t *testing.T) {
	s, root := newHomeServer(t)
	ctx := context.Background()
	st := &writeHomeStream{in: []*agentv1.WriteHomeFilesRequest{
		{AgentId: "builder", HomeQuotaBytes: 1 << 20, Path: "briefs/one.md", Data: []byte("first "), Last: false},
		{Path: "briefs/one.md", Data: []byte("brief"), Last: true},
		{Path: "two.md", Data: []byte("second"), Last: true},
	}}
	if err := s.WriteHomeFiles(st); err != nil {
		t.Fatal(err)
	}
	if len(st.resp.GetAdded()) != 2 {
		t.Errorf("added = %v", st.resp.GetAdded())
	}
	if b, _ := os.ReadFile(filepath.Join(root, "builder", "briefs", "one.md")); string(b) != "first brief" {
		t.Errorf("one.md = %q", b)
	}
	r, err := s.ListHomeFiles(ctx, &agentv1.ListHomeFilesRequest{AgentId: "builder"})
	if err != nil || len(r.GetFiles()) != 2 {
		t.Errorf("list = %v, %v", r, err)
	}
	for _, req := range []*agentv1.ListHomeFilesRequest{{AgentId: "builder", Dir: "../work"}, {AgentId: "builder", Dir: "/etc"}, {AgentId: "Bad"}} {
		if _, err := s.ListHomeFiles(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v, want InvalidArgument", req, err)
		}
	}
	if _, err := s.ListHomeFiles(ctx, &agentv1.ListHomeFilesRequest{AgentId: "lead"}); status.Code(err) != codes.NotFound {
		t.Errorf("an unknown agent: %v", err)
	}
	full := &writeHomeStream{in: []*agentv1.WriteHomeFilesRequest{{AgentId: "builder", HomeQuotaBytes: 20, Path: "big.bin", Data: make([]byte, 64), Last: true}}}
	if err := s.WriteHomeFiles(full); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("past the quota: %v, want ResourceExhausted", err)
	}
	bad := &writeHomeStream{in: []*agentv1.WriteHomeFilesRequest{{AgentId: "builder", HomeQuotaBytes: 1 << 20, Path: "../work/x", Data: []byte("x"), Last: true}}}
	if err := s.WriteHomeFiles(bad); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a path out of the home: %v", err)
	}
}
