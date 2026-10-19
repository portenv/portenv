// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/portenv/portenv/core/internal/teamkit"
)

// newPointerTeam is a team with builder made; the test's uid stands in
// for both root and the agent, and owners are faked by name.
func newPointerTeam(t *testing.T) (*TeamUsers, string, map[string]int, *[]chownCall) {
	t.Helper()
	team, _, chowns := newTestTeam(t)
	if _, err := team.Ensure(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	owners := map[string]int{} // a file's name → its faked owner
	team.pointerRoot = 0
	team.pointerOwner = func(f *os.File, _ os.FileInfo) int {
		if uid, ok := owners[filepath.Base(f.Name())]; ok {
			return uid
		}
		return 2001
	}
	return team, filepath.Join(team.Cfg.HomeRoot, "builder"), owners, chowns
}

func TestThePointerIsWrittenRootsAndReadOnly(t *testing.T) {
	team, home, owners, chowns := newPointerTeam(t)
	kept, err := team.WritePointer("builder", ".claude/CLAUDE.md")
	if err != nil || kept != "" {
		t.Fatalf("WritePointer = %q, %v", kept, err)
	}
	p := filepath.Join(home, ".claude", "CLAUDE.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := teamkit.PointerText("builder", ".claude/CLAUDE.md")
	if string(b) != want {
		t.Errorf("pointer:\n%s\nwant:\n%s", b, want)
	}
	for _, s := range []string{teamkit.PointerMarker, "You are builder in a Portenv team. Run `portenv whoami` and `portenv guide` first. Check `portenv inbox` after each step.", "@~/AGENTS.md"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("pointer lacks %q", s)
		}
	}
	if fi, err := os.Lstat(p); err != nil || fi.Mode().Perm() != 0o444 {
		t.Errorf("pointer mode: %v, %v; want 0444", fi, err)
	}
	if fi, err := os.Lstat(filepath.Dir(p)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("~/.claude: %v, %v; want 0700", fi, err)
	}
	if !slices.Contains(*chowns, chownCall{"CLAUDE.md", 0, 0}) || !slices.Contains(*chowns, chownCall{".claude", 2001, 2001}) {
		t.Errorf("chowns: %v; want CLAUDE.md to root and .claude to the agent", *chowns)
	}
	// The next start replaces Portenv's own pointer (root's, marked).
	owners["CLAUDE.md"] = 0
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(teamkit.PointerMarker+"\nan older pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if kept, err := team.WritePointer("builder", ".claude/CLAUDE.md"); err != nil || kept != "" {
		t.Fatalf("second WritePointer = %q, %v", kept, err)
	}
	if b, _ := os.ReadFile(p); string(b) != want {
		t.Errorf("Portenv's own pointer wasn't replaced: %s", b)
	}
	if fi, _ := os.Lstat(p); fi.Mode().Perm() != 0o444 {
		t.Errorf("replaced pointer's mode %v", fi.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("~/.claude holds %v; a temporary file was left", entries)
	}
}

func TestThePointerForCodexNamesAgentsMD(t *testing.T) {
	team, home, _, _ := newPointerTeam(t)
	if kept, err := team.WritePointer("builder", ".codex/AGENTS.md"); err != nil || kept != "" {
		t.Fatalf("WritePointer = %q, %v", kept, err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if !strings.Contains(string(b), "portenv guide") || !strings.Contains(string(b), "~/AGENTS.md") || strings.Contains(string(b), "@~/") {
		t.Errorf("codex pointer:\n%s", b)
	}
}

// The agent's own file is never replaced: not root's, not marked, or not
// a regular file.
func TestThePointerKeepsTheAgentsOwnFile(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string, owners map[string]int){
		"its own text": func(t *testing.T, dir string, _ map[string]int) {
			write(t, filepath.Join(dir, "CLAUDE.md"), "my notes\n")
		},
		"the marker, but the agent's": func(t *testing.T, dir string, owners map[string]int) {
			write(t, filepath.Join(dir, "CLAUDE.md"), teamkit.PointerMarker+"\nmine now\n")
			owners["CLAUDE.md"] = 2001
		},
		"root's, without the marker": func(t *testing.T, dir string, owners map[string]int) {
			write(t, filepath.Join(dir, "CLAUDE.md"), "root wrote this\n")
			owners["CLAUDE.md"] = 0
		},
		"a symlink": func(t *testing.T, dir string, _ map[string]int) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			write(t, target, "not to be written\n")
			if err := os.Symlink(target, filepath.Join(dir, "CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if b, _ := os.ReadFile(target); string(b) != "not to be written\n" {
					t.Errorf("wrote through the link: %q", b)
				}
			})
		},
		"a FIFO": func(t *testing.T, dir string, _ map[string]int) {
			if err := syscall.Mkfifo(filepath.Join(dir, "CLAUDE.md"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a folder": func(t *testing.T, dir string, _ map[string]int) {
			if err := os.Mkdir(filepath.Join(dir, "CLAUDE.md"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			team, home, owners, _ := newPointerTeam(t)
			dir := filepath.Join(home, ".claude")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			setup(t, dir, owners)
			before, _ := os.Lstat(filepath.Join(dir, "CLAUDE.md"))
			kept, err := team.WritePointer("builder", ".claude/CLAUDE.md")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(kept, "the agent's own file") {
				t.Errorf("kept = %q", kept)
			}
			after, _ := os.Lstat(filepath.Join(dir, "CLAUDE.md"))
			if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
				t.Errorf("the agent's file changed: %v → %v", before, after)
			}
		})
	}
}

// A link or file in the folder's place, or a folder that isn't the
// agent's, is left alone.
func TestThePointerLeavesAFolderThatIsntTheAgents(t *testing.T) {
	t.Run("a symlink", func(t *testing.T) {
		team, home, _, _ := newPointerTeam(t)
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(home, ".claude")); err != nil {
			t.Fatal(err)
		}
		kept, err := team.WritePointer("builder", ".claude/CLAUDE.md")
		if err != nil || !strings.Contains(kept, "isn't a folder") {
			t.Errorf("WritePointer = %q, %v", kept, err)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("wrote through the link: %v", entries)
		}
	})
	t.Run("someone else's", func(t *testing.T) {
		team, home, owners, _ := newPointerTeam(t)
		if err := os.Mkdir(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		owners[".claude"] = 1000
		kept, err := team.WritePointer("builder", ".claude/CLAUDE.md")
		if err != nil || !strings.Contains(kept, "isn't the agent's folder") {
			t.Errorf("WritePointer = %q, %v", kept, err)
		}
	})
}

func TestThePointerRefusesOtherPathsAndAgents(t *testing.T) {
	team, _, _, _ := newPointerTeam(t)
	for _, rel := range []string{".bashrc", "../work/.claude/CLAUDE.md", ".claude/../.bashrc", ".claude/settings.json", "/etc/passwd", ""} {
		if _, err := team.WritePointer("builder", rel); err == nil {
			t.Errorf("WritePointer(%q) = nil", rel)
		}
	}
	if _, err := team.WritePointer("reviewer", ".claude/CLAUDE.md"); err == nil {
		t.Error("a pointer for an agent with no user")
	}
	if _, err := team.WritePointer("../work", ".claude/CLAUDE.md"); err == nil {
		t.Error("a pointer for a bad id")
	}
}
