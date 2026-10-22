// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Claude Code's permission prompt as a Needs-you item (R-0412 item 18,
// phase 1). Claude Code asks for a tool's permission in its own terminal
// ("Do you want to proceed?"), which the owner sees only on that tab. Its
// hooks tell Portenv: PermissionRequest runs as the prompt is about to
// show, with the tool and its input; PostToolUse (the owner said yes and
// it ran), UserPromptSubmit, Stop and the idle_prompt notification mean
// it's no longer waiting. Each runs `portenv notify-permission`, which
// reads the hook's JSON and tells portenvd through the agent's socket.
// The owner still answers in the terminal: the hook prints nothing, so
// Claude Code shows its prompt as before (phase 2, answering from the app,
// is not built).

// PermissionHookCommand is the one command the hooks run.
const PermissionHookCommand = "portenv notify-permission"

// permissionHookEvents are the events the hooks run on, with their
// matcher ("" for every one).
var permissionHookEvents = []struct{ event, matcher string }{
	{"PermissionRequest", ""},
	{"PostToolUse", ""},
	{"UserPromptSubmit", ""},
	{"Stop", ""},
	{"Notification", "idle_prompt"},
}

// permissionHookTimeout is each hook's limit in seconds: the command only
// makes one call to the agent's socket.
const permissionHookTimeout = 10

type claudeHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type claudeHookGroup struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []claudeHook `json:"hooks"`
}

// claudePermissionHooks is the settings' "hooks" value.
func claudePermissionHooks() map[string][]claudeHookGroup {
	out := map[string][]claudeHookGroup{}
	for _, e := range permissionHookEvents {
		out[e.event] = []claudeHookGroup{{Matcher: e.matcher, Hooks: []claudeHook{{Type: "command", Command: PermissionHookCommand, Timeout: permissionHookTimeout}}}}
	}
	return out
}

// PermissionSummaryMax is the longest summary, in characters.
const PermissionSummaryMax = 80

// PermissionHook is what one hook run says.
type PermissionHook struct {
	// Waiting: a permission prompt is showing. Otherwise it isn't (any
	// more).
	Waiting bool
	// Summary is one plain line, at most PermissionSummaryMax characters:
	// the command's start, or the tool and its file or address, or the
	// tool's name. Nothing else of the input.
	Summary string
}

// ParsePermissionHook reads a hook's JSON from Claude Code.
func ParsePermissionHook(b []byte) (PermissionHook, error) {
	var in struct {
		Event     string          `json:"hook_event_name"`
		Tool      string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return PermissionHook{}, err
	}
	if in.Event != "PermissionRequest" {
		return PermissionHook{}, nil
	}
	return PermissionHook{Waiting: true, Summary: permissionSummary(in.Tool, in.ToolInput)}, nil
}

// permissionSummary is what the prompt asks, in one short line.
func permissionSummary(tool string, input json.RawMessage) string {
	var f struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Notebook string `json:"notebook_path"`
		URL      string `json:"url"`
	}
	_ = json.Unmarshal(input, &f) // a tool without these fields is named alone
	var s string
	switch {
	case tool == "Bash" && strings.TrimSpace(f.Command) != "":
		s = f.Command
	case strings.HasPrefix(tool, "mcp__"):
		s = tool
	case f.FilePath != "":
		s = tool + " " + f.FilePath
	case f.Notebook != "":
		s = tool + " " + f.Notebook
	case f.URL != "":
		s = tool + " " + f.URL
	default:
		s = tool
	}
	if s = OneLine(s, PermissionSummaryMax); s == "" {
		return "a tool"
	}
	return s
}

// OneLine is s as one plain line of at most max characters: control
// characters (C0, C1, DEL), escape sequences' introducers and bidi
// controls removed, line breaks, tabs and runs of spaces as one space,
// and "…" at the end when it was cut.
func OneLine(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), isBidiControl(r), r == unicode.ReplacementChar:
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := []rune(b.String())
	if len(out) <= max {
		return string(out)
	}
	if max < 1 {
		return ""
	}
	return strings.TrimRight(string(out[:max-1]), " ") + "…"
}

func isBidiControl(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}
