// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The New-workspace team's files (ADR 0025 §5): written new-only, never
// through a link; a missing project is made, with git, on main.

func recordInitGit(calls *[][]string) gitFunc {
	return func(_ context.Context, dir string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{dir}, args...))
		return nil, nil
	}
}

func TestWriteAgentFilesMakesTheProject(t *testing.T) {
	project := filepath.Join(t.TempDir(), "acme")
	var calls [][]string
	res, err := writeAgentFiles(context.Background(), project, []teamIOFile{
		{Name: "lead.md", Content: []byte("lead")},
		{Name: "off/social.md", Content: []byte("social")},
	}, recordInitGit(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if !res.MadeProject || !slices.Equal(res.Written, []string{"lead.md", "off/social.md"}) || len(res.Existing) != 0 {
		t.Fatalf("%+v", res)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], []string{project, "init", "--quiet", "--initial-branch=main"}) {
		t.Fatalf("git calls %v", calls)
	}
	for name, want := range map[string]string{"lead.md": "lead", "off/social.md": "social"} {
		p := filepath.Join(project, ".portenv", "agents", filepath.FromSlash(name))
		b, err := os.ReadFile(p)
		fi, _ := os.Stat(p)
		if err != nil || string(b) != want || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: %q %v %v", name, b, fi.Mode(), err)
		}
	}
}

// An existing project gets no git init; a file already there is left as
// it is and listed (the project's wins, ADR 0025 §4).
func TestWriteAgentFilesNeverOverwrites(t *testing.T) {
	project := filepath.Join(t.TempDir(), "acme")
	agents := filepath.Join(project, ".portenv", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "lead.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	res, err := writeAgentFiles(context.Background(), project, []teamIOFile{
		{Name: "lead.md", Content: []byte("theirs")},
		{Name: "builder.md", Content: []byte("b")},
	}, recordInitGit(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if res.MadeProject || len(calls) != 0 || !slices.Equal(res.Written, []string{"builder.md"}) || !slices.Equal(res.Existing, []string{"lead.md"}) {
		t.Fatalf("%+v %v", res, calls)
	}
	if b, _ := os.ReadFile(filepath.Join(agents, "lead.md")); string(b) != "mine" {
		t.Fatalf("lead.md is %q", b)
	}
}

// A link anywhere on the way (the agents folder, off/, the file itself)
// is never followed.
func TestWriteAgentFilesNeverFollowsALink(t *testing.T) {
	outside := t.TempDir()
	for _, tc := range []struct {
		name string
		set  func(project string) error
		file string
	}{
		{"agents folder", func(p string) error {
			if err := os.MkdirAll(filepath.Join(p, ".portenv"), 0o755); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(p, ".portenv", "agents"))
		}, "lead.md"},
		{"off folder", func(p string) error {
			if err := os.MkdirAll(filepath.Join(p, ".portenv", "agents"), 0o755); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(p, ".portenv", "agents", "off"))
		}, "off/social.md"},
		{"the file", func(p string) error {
			if err := os.MkdirAll(filepath.Join(p, ".portenv", "agents"), 0o755); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(p, ".portenv", "agents", "lead.md"))
		}, "lead.md"},
		{"the project", func(p string) error { return os.Symlink(outside, p) }, "lead.md"},
	} {
		project := filepath.Join(t.TempDir(), "acme")
		if err := tc.set(project); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		res, err := writeAgentFiles(context.Background(), project, []teamIOFile{{Name: tc.file, Content: []byte("x")}}, recordInitGit(&calls))
		if err == nil && slices.Contains(res.Written, tc.file) {
			t.Errorf("%s: written through a link", tc.name)
		}
		ents, _ := os.ReadDir(outside)
		if len(ents) != 0 {
			t.Fatalf("%s: something landed outside: %v", tc.name, ents)
		}
	}
}

func TestWriteAgentFilesRefusesBadNames(t *testing.T) {
	for _, name := range []string{"../x.md", "off/../../x.md", "Lead.md", "lead", "a/b.md", "off/off/x.md", ".md", "/lead.md"} {
		project := filepath.Join(t.TempDir(), "acme")
		var calls [][]string
		if _, err := writeAgentFiles(context.Background(), project, []teamIOFile{{Name: name, Content: []byte("x")}}, recordInitGit(&calls)); err == nil {
			t.Errorf("%q: not refused", name)
		}
		if _, err := os.Lstat(project); err == nil {
			t.Errorf("%q: the project was made before the names were checked", name)
		}
	}
	project := filepath.Join(t.TempDir(), "acme")
	var calls [][]string
	if _, err := writeAgentFiles(context.Background(), project, []teamIOFile{{Name: "lead.md", Content: make([]byte, 64<<10+1)}}, recordInitGit(&calls)); err == nil {
		t.Error("a file over 64 KB was written")
	}
}

// Through the RPC: only a project folder in work's home.
func TestWriteAgentFilesRPC(t *testing.T) {
	s, home := newBoardServer(t)
	project := filepath.Join(home, "work", "acme")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := s.WriteAgentFiles(context.Background(), &agentv1.WriteAgentFilesRequest{ProjectDir: "/home/work/acme",
		Files: []*agentv1.AgentFileContent{{Name: "lead.md", Content: []byte("---\nid: lead\n---\n")}}})
	if err != nil || !slices.Equal(r.GetWritten(), []string{"lead.md"}) {
		t.Fatalf("%v %v", r, err)
	}
	for _, bad := range []string{"/home/work", "/home/work/../x", "/home/other/acme", "/home/work/a/b", "/home/work/.x"} {
		_, err := s.WriteAgentFiles(context.Background(), &agentv1.WriteAgentFilesRequest{ProjectDir: bad,
			Files: []*agentv1.AgentFileContent{{Name: "lead.md", Content: []byte("x")}}})
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "project folder") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
