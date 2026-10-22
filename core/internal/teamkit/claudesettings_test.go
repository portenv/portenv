// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func settingsAllow(t *testing.T, b []byte) []string {
	t.Helper()
	var s struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
		// Checked by claudehooks_test.go.
		Hooks json.RawMessage `json:"hooks"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return s.Permissions.Allow
}

var teamCommands = []string{
	"Bash(portenv msg:*)",
	"Bash(portenv inbox:*)",
	"Bash(portenv ask-owner:*)",
	"Bash(portenv whoami)",
	"Bash(portenv guide:*)",
	"Bash(portenv status set:*)",
	"Bash(portenv status clear)",
}

// A builder with its own clone and no lenses: the team commands and its
// own home, nothing else.
func TestClaudeSettingsNoLenses(t *testing.T) {
	b, err := ClaudeSettings("/home/builder", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(teamCommands), "Read(//home/builder/**)")
	if got := settingsAllow(t, b); !slices.Equal(got, want) {
		t.Fatalf("allow = %q\nwant  %q", got, want)
	}
}

// A reviewer that reads builder's and design's clones: those two paths
// too, and still nothing else.
func TestClaudeSettingsWithLenses(t *testing.T) {
	b, err := ClaudeSettings("/home/reviewer", []string{"/home/design/acme-api", "/home/builder/acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(teamCommands), "Read(//home/reviewer/**)", "Read(//home/builder/acme-api/**)", "Read(//home/design/acme-api/**)")
	got := settingsAllow(t, b)
	if !slices.Equal(got, want) {
		t.Fatalf("allow = %q\nwant  %q", got, want)
	}
	for _, r := range got {
		for _, never := range []string{"WebFetch", "WebSearch", "curl", "wget", "Bash(*)", "Bash(portenv:*)", "Bash(portenv *)", "/home/work", "Edit", "Write", "split", "tab", "handoff"} {
			if strings.Contains(r, never) {
				t.Errorf("rule %q contains %q", r, never)
			}
		}
	}
}

// Anything but an agent's home, and a clone directly in another agent's
// home, is refused rather than allowed.
func TestClaudeSettingsRefusesOtherPaths(t *testing.T) {
	for _, home := range []string{"", "/", "/home", "/home/work", "/home/work/acme-api", "/home/../etc", "home/builder", "/home/builder/", "/home/Builder", "/home/b*", "/root"} {
		if _, err := ClaudeSettings(home, nil); err == nil {
			t.Errorf("home %q accepted", home)
		}
	}
	for _, lens := range []string{"/home/builder", "/home/work/acme-api", "/home/builder/../work", "/home/builder/a/b", "/home/builder/*", "/home/builder/.claude", "/etc", "/home/reviewer/acme-api", "/home/builder/acme api", "/home/builder/a)b"} {
		if _, err := ClaudeSettings("/home/reviewer", []string{lens}); err == nil {
			t.Errorf("lens %q accepted", lens)
		}
	}
}
