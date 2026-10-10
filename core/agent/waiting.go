// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"regexp"
	"strings"
	"time"
)

// The fallback waiting-for-input detector (ADR 0017; Agent readiness
// principle 5). It combines four signals per tab, because none is right on
// its own (the ADR has each one's error rates on the labelled cases):
//
//  1. OSC 133: the shell is running a command, not at its own prompt.
//  2. The foreground process is blocked waiting on the terminal.
//  3. The tab's output has been quiet for a while.
//  4. The screen ends with something that reads as a question.
//
// Full-screen programs (the alternate screen: less, vim) are being used,
// not asking, so they never count as waiting.
type waitSignals struct {
	Shell        shellState
	BlockedOnTTY bool
	// BlockedKnown is false when the foreground process can't be read: a
	// setuid program (sudo, su, passwd) runs as root, and the agent reads
	// /proc as the main user.
	BlockedKnown bool
	Quiet        time.Duration
	ScreenPrompt bool
	AltScreen    bool
	// The terminal's modes: a password prompt turns echo off but keeps
	// line (canonical) input; a raw-mode program turns both off.
	EchoOff, Canonical bool
	// LastLines are the last non-empty lines on screen, for the wording.
	LastLines []string
}

// quietAfter is how long a tab's output must be still before it can count
// as waiting.
const quietAfter = 1500 * time.Millisecond

// soloQuiet is the quiet signal's verdict (the others' are in
// waiting_linux.go, where the ADR's error table uses them).
func (s waitSignals) soloQuiet() bool { return s.Quiet >= quietAfter }

// Waiting is the combined verdict: a command (not the shell's own prompt)
// is blocked on the terminal, its output has stopped, it isn't a
// full-screen program, and the screen asks something. An unknown shell
// state (no integration) or an unreadable foreground process (setuid) leave
// the verdict to the other signals.
func (s waitSignals) Waiting() bool {
	if s.Shell == shellPrompt || s.Shell == shellDone {
		return false
	}
	blocked := s.BlockedOnTTY || !s.BlockedKnown
	return blocked && s.soloQuiet() && !s.AltScreen && s.ScreenPrompt
}

var (
	// promptLine: a line that asks something of the person. Checked on the
	// last few non-empty lines, since agent CLIs print their choices under
	// the question.
	promptLine = regexp.MustCompile(`(?i)(\?\s*$|\[y/n\]|\(y/n\)|\[yes/no\]|\(yes/no\)|:\s*$|^\s*>>>\s*$|^\s*\.\.\.\s*$)`)
	// secretWords: wording that asks for a secret.
	secretWords = regexp.MustCompile(`(?i)(pass(word|phrase)|\bpin\b|verification code|one-time code|\b2fa\b|otp\b)`)
)

// boxBorder are the characters TUIs draw boxes with; a question inside a
// box ends with a border, so borders are stripped before matching.
const boxBorder = "│┃║|╭╮╰╯┌┐└┘─━═ \t"

// screenAsks reports whether the last lines read as a question.
func screenAsks(lines []string) bool {
	for _, l := range lines {
		if promptLine.MatchString(strings.Trim(l, boxBorder)) {
			return true
		}
	}
	return false
}

// Secret is a prompt for a password or similar: the terminal has echo off
// with line input (what getpass, sudo and ssh set), or the last line's
// wording asks for one. Raw-mode programs (Claude Code, vim) turn echo off
// too, but without line input, so they are not secret prompts.
func (s waitSignals) Secret() bool {
	if s.EchoOff && s.Canonical {
		return true
	}
	if n := len(s.LastLines); n > 0 && secretWords.MatchString(s.LastLines[n-1]) {
		return true
	}
	return false
}

// SecretNotice is the only thing ever said about a secret prompt (ADR 0016
// point 8): never the prompt's text.
const SecretNotice = "A password is needed; answer it in the terminal."

// secretPromptError is the refusal of an answer to a secret prompt; its
// message is the notice, word for word.
type secretPromptError struct{}

func (secretPromptError) Error() string { return SecretNotice }

// RelayQuestion is what a relay (portenv ask, events, webhooks, MCP) may
// say about a waiting tab: the screen's question, or only SecretNotice for
// a secret prompt. Empty when the tab isn't waiting.
func (s waitSignals) RelayQuestion() string {
	if !s.Waiting() {
		return ""
	}
	if s.Secret() {
		return SecretNotice
	}
	return strings.Join(s.LastLines, "\n")
}

// CheckRelayAnswer refuses an answer sent through a relay to a secret
// prompt: a secret is only ever typed in the terminal.
func (s waitSignals) CheckRelayAnswer() error {
	if s.Secret() {
		return secretPromptError{}
	}
	return nil
}
