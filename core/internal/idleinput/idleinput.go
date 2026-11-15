// SPDX-License-Identifier: Apache-2.0

// Package idleinput is the runner table's idle-input rule (ADR 0022 §4b,
// ADR 0025 §9): how the helper tells that an agent's runner waits at an
// empty input, so a routine or a message wake-up (CC-0498) may be typed
// into it. portenvd holds each runner's entry and sends it with the
// agent's start; the helper compiles it and finds the prompt on screen.
package idleinput

import (
	"fmt"
	"path"
	"regexp"
)

// MaxPattern is the longest pattern, in bytes.
const MaxPattern = 512

// MaxLines is the most screen lines a rule may look at: the helper reads
// the last 8 non-empty lines (the waiting detector's screenLines).
const MaxLines = 8

// Input is one runner's rule. Pattern (RE2) is matched against one whole
// screen line at a time: the line that shows the runner's input when it's
// empty. Lines is how many of the last non-empty lines the prompt may be
// on, counted from the bottom, since a runner may draw a rule and a
// status footer under its input (0 means 1: the last line only).
type Input struct {
	Pattern string
	Lines   int
}

// Runners are the known runners' rules, by program name (the runner
// argv's first word's basename). A runner not here is never typed into.
var Runners = map[string]Input{
	"claude": Claude,
	// Codex's empty prompt, anywhere in the last lines (a stand-in until
	// it's checked against the real CLI).
	"codex": {Pattern: `^\s*›\s*$`, Lines: MaxLines},
}

// Claude is Claude Code's rule. Claude Code 2.1 draws its input between
// two rules with the status footer under them, so the prompt is a few
// lines above the last:
//
//	────────────────────────────────────────
//	❯ Try "refactor <filepath>"
//	────────────────────────────────────────
//	  ▸▸ auto mode on (shift+tab to cycle)
//
// The input is empty when the prompt (❯, or > in older versions, maybe
// inside a box's │ sides) is followed by nothing or only by the dim
// placeholder, which always reads Try "…" (its closing quote cut off on a
// narrow pane). The screen capture has no attributes, so the placeholder
// is told from typed text by its form alone: anything else after the
// prompt, Try "x" and more included, is typed and never added to.
var Claude = Input{
	Pattern: `^[ \t\x{a0}]*[│|]?[ \t\x{a0}]*[❯>](?:[ \t\x{a0}]+Try "[^"]*"?)?[ \t\x{a0}]*[│|]?[ \t\x{a0}]*$`,
	Lines:   6,
}

// ForRunner is the rule for a runner's argv, or the zero Input for one
// Portenv doesn't know.
func ForRunner(argv []string) Input {
	if len(argv) == 0 {
		return Input{}
	}
	in := Runners[path.Base(argv[0])]
	if _, err := Compile(in); err != nil {
		return Input{}
	}
	return in
}

// Matcher is a compiled Input.
type Matcher struct {
	re    *regexp.Regexp
	lines int
}

// Compile checks and compiles a rule. An empty pattern is an error: a
// runner without one has no rule at all.
func Compile(in Input) (*Matcher, error) {
	switch {
	case in.Pattern == "":
		return nil, fmt.Errorf("an idle-input pattern is required")
	case len(in.Pattern) > MaxPattern:
		return nil, fmt.Errorf("an idle-input pattern is at most %d bytes", MaxPattern)
	case in.Lines < 0 || in.Lines > MaxLines:
		return nil, fmt.Errorf("an idle-input rule looks at 1 to %d lines", MaxLines)
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return nil, fmt.Errorf("the idle-input pattern: %w", err)
	}
	n := in.Lines
	if n == 0 {
		n = 1
	}
	return &Matcher{re: re, lines: n}, nil
}

// Find is the index in lines (the screen's last non-empty lines, top to
// bottom) of the runner's empty-input line, the lowest one among the last
// m's lines, or -1 when there's none: the runner isn't waiting at an
// empty input.
func (m *Matcher) Find(lines []string) int {
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-m.lines; i-- {
		if m.re.MatchString(lines[i]) {
			return i
		}
	}
	return -1
}
