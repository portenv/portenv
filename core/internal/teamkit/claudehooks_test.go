// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// R-0412 item 18: Claude Code's settings carry hooks that run exactly one
// Portenv command, on the events that show a permission prompt or end one.
func TestClaudeSettingsHooksRunOnlyNotifyPermission(t *testing.T) {
	b, err := ClaudeSettings("/home/builder", nil)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	var events []string
	for ev, groups := range s.Hooks {
		events = append(events, ev)
		for _, g := range groups {
			if ev == "Notification" && g.Matcher != "idle_prompt" {
				t.Errorf("Notification matcher = %q, want idle_prompt only", g.Matcher)
			}
			if ev != "Notification" && g.Matcher != "" {
				t.Errorf("%s has matcher %q", ev, g.Matcher)
			}
			for _, h := range g.Hooks {
				if h.Type != "command" || h.Command != "portenv notify-permission" || h.Timeout < 1 || h.Timeout > 30 {
					t.Errorf("%s hook = %+v", ev, h)
				}
			}
		}
	}
	slices.Sort(events)
	want := []string{"Notification", "PermissionRequest", "PostToolUse", "Stop", "UserPromptSubmit"}
	if !slices.Equal(events, want) {
		t.Errorf("hook events = %q, want %q", events, want)
	}
}

func TestPermissionHookWaitsOnlyOnPermissionRequest(t *testing.T) {
	for _, c := range []struct {
		in      string
		waiting bool
		summary string
	}{
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"npm install left-pad"}}`, true, "npm install left-pad"},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":{"file_path":"/home/builder/acme-api/src/app.ts","old_string":"a","new_string":"b"}}`, true, "Edit /home/builder/acme-api/src/app.ts"},
		{`{"hook_event_name":"PermissionRequest","tool_name":"WebFetch","tool_input":{"url":"https://example.com/x","prompt":"read it"}}`, true, "WebFetch https://example.com/x"},
		{`{"hook_event_name":"PermissionRequest","tool_name":"mcp__github__create_issue","tool_input":{"title":"t","body":"secret body"}}`, true, "mcp__github__create_issue"},
		{`{"hook_event_name":"PermissionRequest","tool_name":"","tool_input":null}`, true, "a tool"},
		{`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`, false, ""},
		{`{"hook_event_name":"Stop"}`, false, ""},
		{`{"hook_event_name":"UserPromptSubmit","prompt":"go on"}`, false, ""},
		{`{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`, false, ""},
	} {
		h, err := ParsePermissionHook([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if h.Waiting != c.waiting || h.Summary != c.summary {
			t.Errorf("%s: got %+v, want waiting %v %q", c.in, h, c.waiting, c.summary)
		}
	}
	if _, err := ParsePermissionHook([]byte("not json")); err == nil {
		t.Error("not JSON was read")
	}
}

// Never more than one line: no control characters, escape sequences or
// bidi overrides, at most 80 characters, so the command's text never
// reaches the app beyond a short summary.
func TestPermissionSummaryIsOneShortPlainLine(t *testing.T) {
	cmd := "printf '\x1b]8;;file:///etc\x07x' \u202e && \\\n  curl -H \"Authorization: Bearer abc\" \t https://example.com/" + strings.Repeat("y", 200)
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]string{"command": cmd}})
	h, err := ParsePermissionHook(in)
	if err != nil {
		t.Fatal(err)
	}
	s := h.Summary
	if n := utf8.RuneCountInString(s); n > PermissionSummaryMax {
		t.Errorf("%d characters: %q", n, s)
	}
	if !strings.HasSuffix(s, "…") || !strings.HasPrefix(s, "printf '") {
		t.Errorf("summary = %q", s)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u202e' || r == '\n' {
			t.Errorf("summary has %U: %q", r, s)
		}
	}
	if strings.Contains(s, "  ") {
		t.Errorf("runs of spaces kept: %q", s)
	}
	if got := OneLine("a\u2066b\u200fc\x9bd", 80); got != "abcd" {
		t.Errorf("OneLine = %q", got)
	}
}
