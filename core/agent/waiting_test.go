// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strings"
	"testing"
	"time"
)

func asking(lines ...string) waitSignals {
	return waitSignals{Shell: shellRunning, BlockedOnTTY: true, BlockedKnown: true, Quiet: 3 * time.Second, ScreenPrompt: screenAsks(lines), LastLines: lines}
}

func TestWaitingNeedsEverySignal(t *testing.T) {
	base := asking("Continue? [y/N]")
	if !base.Waiting() {
		t.Fatal("a quiet, blocked command asking a question isn't waiting")
	}
	for name, s := range map[string]waitSignals{
		"at the shell's prompt": func() waitSignals { s := base; s.Shell = shellPrompt; return s }(),
		"command finished":      func() waitSignals { s := base; s.Shell = shellDone; return s }(),
		"not blocked":           func() waitSignals { s := base; s.BlockedOnTTY = false; return s }(),
		"still printing":        func() waitSignals { s := base; s.Quiet = 200 * time.Millisecond; return s }(),
		"full-screen program":   func() waitSignals { s := base; s.AltScreen = true; return s }(),
		"no question on screen": func() waitSignals { s := base; s.ScreenPrompt = false; return s }(),
	} {
		if s.Waiting() {
			t.Errorf("%s: counted as waiting", name)
		}
	}
	// Without shell integration (unknown state) the other signals decide.
	s := base
	s.Shell = shellUnknown
	if !s.Waiting() {
		t.Error("an unknown shell state blocked the verdict")
	}
	// A setuid program (sudo) can't be read: the other signals decide, so
	// it waits when it asks and doesn't when it doesn't.
	setuid := base
	setuid.BlockedOnTTY, setuid.BlockedKnown = false, false
	if !setuid.Waiting() {
		t.Error("an unreadable program asking a question isn't waiting")
	}
	setuid.ScreenPrompt = false
	if setuid.Waiting() {
		t.Error("an unreadable program with no question counted as waiting")
	}
}

func TestScreenAsks(t *testing.T) {
	for _, l := range []string{
		"Delete 3 files? [y/N] ", "Proceed (y/n)", "Do you want to proceed?", "[sudo] password for work: ",
		"Enter passphrase for \"/tmp/k\": ", ">>> ", "Overwrite? [yes/no]",
	} {
		if !screenAsks([]string{l}) {
			t.Errorf("%q: not read as a question", l)
		}
	}
	for _, l := range []string{"Compiling 42 files…", "work@box:~$ ", "PASS api/retries.spec.ts", "~"} {
		if screenAsks([]string{l}) {
			t.Errorf("%q: read as a question", l)
		}
	}
	// The question may sit above the choices, as in agent CLIs.
	if !screenAsks([]string{"Do you want to proceed?", "❯ 1. Yes", "  2. No"}) {
		t.Error("a question above its choices was missed")
	}
}

// ADR 0016 point 8: a password prompt is never relayed; only the notice is,
// and an answer through a relay is refused.
func TestSecretPromptsAreNeverRelayed(t *testing.T) {
	sudo := asking("[sudo] password for work: ")
	sudo.EchoOff, sudo.Canonical = true, true
	if !sudo.Secret() {
		t.Fatal("sudo's prompt isn't secret")
	}
	if got := sudo.RelayQuestion(); got != SecretNotice || strings.Contains(got, "sudo") {
		t.Fatalf("relayed %q", got)
	}
	if err := sudo.CheckRelayAnswer(); err == nil || err.Error() != SecretNotice {
		t.Fatalf("a relayed answer to a password prompt: %v", err)
	}
	// Echo off with line input is secret even with neutral wording.
	quiet := asking("Code: ")
	quiet.EchoOff, quiet.Canonical = true, true
	if !quiet.Secret() {
		t.Error("echo off with line input isn't secret")
	}
	// Wording alone (echo on) is secret too.
	if !asking("Enter passphrase for key: ").Secret() {
		t.Error("passphrase wording isn't secret")
	}
	// Raw mode (echo and line input off) is a TUI, not a secret prompt.
	tui := asking("Do you want to proceed?", "❯ 1. Yes", "  2. No")
	tui.EchoOff, tui.Canonical = true, false
	if tui.Secret() {
		t.Error("a raw-mode program counted as a secret prompt")
	}
	if got := tui.RelayQuestion(); !strings.Contains(got, "Do you want to proceed?") {
		t.Errorf("a normal question wasn't relayed: %q", got)
	}
	if err := tui.CheckRelayAnswer(); err != nil {
		t.Errorf("an answer to a normal question was refused: %v", err)
	}
	// Nothing is relayed for a tab that isn't waiting.
	idle := asking("Continue? [y/N]")
	idle.Shell = shellPrompt
	if idle.RelayQuestion() != "" {
		t.Error("relayed a question for a tab that isn't waiting")
	}
}
