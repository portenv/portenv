// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyAPersonAtATerminalIsPrompted: the app's helper calls and scripts
// never raise a Keychain prompt (PLAN.md, Phase 1 item 1).
func TestOnlyAPersonAtATerminalIsPrompted(t *testing.T) {
	for _, c := range []struct {
		cmd      string
		terminal bool
		want     bool
	}{
		{"init", true, true},
		{"init", false, false}, // a script
		{"app", true, false},   // the app's helper, even from a terminal
		{"app", false, false},
	} {
		if got := promptsAllowed(c.cmd, c.terminal); got != c.want {
			t.Errorf("%s (terminal %v): %v, want %v", c.cmd, c.terminal, got, c.want)
		}
	}
}

// TestNoBackgroundProcessCanPrompt: keys.AllowPrompts is called only by the
// CLI's main, so portenvd, the runner and every other package can never
// raise a Keychain prompt.
func TestNoBackgroundProcessCanPrompt(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path) // #nosec G304 -- this repository's sources
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "AllowPrompts()") && filepath.ToSlash(path) != "../../cmd/portenv/main.go" && filepath.Base(path) != "keys.go" {
			t.Errorf("%s calls keys.AllowPrompts: only the CLI, at a terminal, may prompt", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
