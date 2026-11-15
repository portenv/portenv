// SPDX-License-Identifier: Apache-2.0

package idleinput

import (
	"strings"
	"testing"
)

// The bottom of Claude Code 2.1.304's screen at an empty input (the
// owner's live test): a rule, the ❯ prompt with a dim placeholder, a rule,
// and the status footer as the last line.
func claude2_1_304(input string) string {
	return "⏺ Done. I updated the handler in acme-api; want me to run the tests too?\n" +
		"\n" +
		"✻ Brewed for 42s\n" +
		"\n" +
		"────────────────────────────────────────────────────────────\n" +
		input + "\n" +
		"────────────────────────────────────────────────────────────\n" +
		"  ▸▸ auto mode on (shift+tab to cycle)            Update available! Run: your package manager update command\n" +
		"\n"
}

// screenLines is what the helper reads: the last 8 non-empty lines, right
// spaces trimmed (agent.lastLines).
func screenLines(screen string) []string {
	var out []string
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0 && len(out) < MaxLines; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{strings.TrimRight(lines[i], " ")}, out...)
		}
	}
	return out
}

// Fixtures, each with whether Claude Code waits at an empty input.
var claudeScreens = []struct {
	name, screen string
	idle         bool
}{
	// 2.1.304, empty: the placeholder varies, and may be cut short.
	{"2.1.304 refactor placeholder", claude2_1_304(`❯ Try "refactor <filepath>"`), true},
	{"2.1.304 question placeholder", claude2_1_304(`❯ Try "how do I log an error?"`), true},
	{"2.1.304 edit placeholder", claude2_1_304(`❯ Try "edit <filepath> to..."`), true},
	{"2.1.304 placeholder cut short", claude2_1_304(`❯ Try "edit <filepath> to…`), true},
	{"2.1.304 no placeholder", claude2_1_304(`❯ `), true},
	{"2.1.304 no-break space", claude2_1_304("❯ Try \"refactor <filepath>\""), true},
	// 2.1.304 with something typed: never append to it.
	{"2.1.304 typed", claude2_1_304(`❯ fix the bug`), false},
	{"2.1.304 typed Try", claude2_1_304(`❯ Try harder`), false},
	{"2.1.304 typed after a quote", claude2_1_304(`❯ Try "this" and then that`), false},
	{"2.1.304 typed one letter", claude2_1_304(`❯ f`), false},
	// Busy: working, no prompt under it.
	{"spinner", "⏺ Bash(go test ./...)\n  ⎿  Running…\n\n✻ Brewing… (12s · esc to interrupt)\n", false},
	{"tool running", "⏺ Update(internal/handler.go)\n  ⎿  Updated internal/handler.go with 3 additions\n⏺ Bash(make test)\n  ⎿  ok  \tacme-api/internal\t0.41s\n", false},
	// A permission question replaces the input; its ❯ marks a choice.
	{"permission question", " Bash command\n\n   rm -rf build\n   Remove the build folder\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don't ask again for rm commands in /home/work/acme-api\n   3. No, and tell Claude what to do differently (esc)\n", false},
	// Older Claude Code: "> " in a box with sides, or alone.
	{"old box", "╭──────────────────────────────────╮\n│ >                                │\n╰──────────────────────────────────╯\n  ? for shortcuts\n", true},
	{"old box placeholder", "╭──────────────────────────────────╮\n│ > Try \"write a test for <filepath>\" │\n╰──────────────────────────────────╯\n  ? for shortcuts\n", true},
	{"old bare", "some output\n> \n", true},
	{"old box typed", "╭──────────────────────────────────╮\n│ > fix the tests                  │\n╰──────────────────────────────────╯\n  ? for shortcuts\n", false},
	{"old bare typed", "> fix the tests\n", false},
	// An empty prompt too far up isn't the input: the screen moved on.
	{"prompt scrolled up", "❯ \n1\n2\n3\n4\n5\n6\n7\n", false},
	{"empty screen", "\n\n", false},
}

func TestClaudesIdleInput(t *testing.T) {
	m, err := Compile(ForRunner([]string{"/usr/bin/claude", "--model", "opus"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claudeScreens {
		if got := m.Find(screenLines(c.screen)) >= 0; got != c.idle {
			t.Errorf("%s: idle %v, want %v\n%s", c.name, got, c.idle, c.screen)
		}
	}
}

// Find gives the prompt's line, so the helper reads a question only under
// it, and looks no further up than Lines.
func TestFindIsThePromptsLine(t *testing.T) {
	m, err := Compile(Claude)
	if err != nil {
		t.Fatal(err)
	}
	lines := screenLines(claude2_1_304(`❯ Try "refactor <filepath>"`))
	i := m.Find(lines)
	if i < 0 || !strings.HasPrefix(lines[i], "❯") {
		t.Fatalf("found %d in %q", i, lines)
	}
	last, err := Compile(Input{Pattern: `^❯`, Lines: 0})
	if err != nil {
		t.Fatal(err)
	}
	if last.Find(lines) != -1 {
		t.Error("Lines 0 looked above the last line")
	}
	if last.Find([]string{"x", "❯ "}) != 1 {
		t.Error("Lines 0 didn't read the last line")
	}
}

func TestRulesAreChecked(t *testing.T) {
	for _, in := range []Input{
		{},
		{Pattern: "(", Lines: 1},
		{Pattern: strings.Repeat("a", MaxPattern+1), Lines: 1},
		{Pattern: "x", Lines: -1},
		{Pattern: "x", Lines: MaxLines + 1},
	} {
		if _, err := Compile(in); err == nil {
			t.Errorf("%.20q, %d: compiled", in.Pattern, in.Lines)
		}
	}
	for name, in := range Runners {
		if _, err := Compile(in); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ForRunner([]string{"bash"}) != (Input{}) || ForRunner(nil) != (Input{}) {
		t.Error("an unknown runner has a rule")
	}
}
