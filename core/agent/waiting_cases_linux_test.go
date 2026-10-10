// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The spike's labelled cases (ADR 0017): each runs in its own tab on a real
// tmux server, and the detector's four signals are read the way the agent
// reads them. Each case is "real" (the actual program) or "stand-in" (a
// script reproducing an agent CLI's terminal behaviour, because running the
// real one needs an account). Expected: waiting = a program needs an answer
// from a person; secret = a password-style prompt.
type waitCase struct {
	name, kind   string
	cmd          string
	waiting      bool
	secret       bool
	needs        string // a program the case needs
	standinWhen  func() bool
	standinCmd   string
	standinLabel string
}

// claudeStandIn reproduces Claude Code's permission prompt: raw mode (echo
// and line input off), no alternate screen, the question above numbered
// choices, and Node's event loop (epoll) waiting on the terminal.
const claudeStandIn = `import os, select, sys, termios, tty
fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
tty.setraw(fd)
sys.stdout.write("\x1b[?25l╭──────────────────────────────╮\r\n│ Bash command                 │\r\n│   npm test                   │\r\n│ Do you want to proceed?      │\r\n│ ❯ 1. Yes                     │\r\n│   2. No                      │\r\n╰──────────────────────────────╯\r\n")
sys.stdout.flush()
ep = select.epoll()
ep.register(fd, select.EPOLLIN)
ep.poll()
termios.tcsetattr(fd, termios.TCSADRAIN, old)
`

// secondCLIStandIn reproduces an Ink-based agent CLI (Gemini CLI style):
// raw mode, inline, the question with choices under it, waiting in select.
const secondCLIStandIn = `import select, sys, termios, tty
fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
tty.setraw(fd)
sys.stdout.write("✦ Apply this change to api/retries.ts?\r\n  ● 1. Yes, allow once\r\n    2. Yes, allow always\r\n    3. No (esc)\r\n")
sys.stdout.flush()
select.select([fd], [], [])
termios.tcsetattr(fd, termios.TCSADRAIN, old)
`

// getpassStandIn reproduces sudo's prompt where sudo can't prompt (CI's
// runner has passwordless sudo): getpass sets the same terminal modes.
const getpassStandIn = `import getpass; getpass.getpass("[sudo] password for work: ")`

func TestWaitingCases(t *testing.T) {
	h, tb := newCtlHarness(t, "bash")
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	claude := write("claude-standin.py", claudeStandIn)
	second := write("second-cli-standin.py", secondCLIStandIn)
	getpass := write("getpass-standin.py", getpassStandIn)
	// A throwaway key with a throwaway passphrase, for the real ssh prompt.
	key := filepath.Join(dir, "spike-key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "throwaway-spike-passphrase", "-f", key).CombinedOutput(); err != nil { // #nosec G204 -- test
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	// sudo prompts only where the user has no passwordless sudo.
	sudoPrompts := exec.Command("sudo", "-n", "true").Run() != nil

	cases := []waitCase{
		{name: "Claude Code asks (permission prompt)", kind: "stand-in", cmd: "python3 " + claude, waiting: true, needs: "python3"},
		{name: "a second agent CLI asks (Gemini CLI style)", kind: "stand-in", cmd: "python3 " + second, waiting: true, needs: "python3"},
		{name: "a script's y/n prompt", kind: "real", cmd: `read -r -p "Delete 3 files? [y/N] " answer`, waiting: true},
		{name: "a long build that goes quiet", kind: "real", cmd: `echo "Compiling 42 files…"; sleep 60`, waiting: false},
		{name: "a build that keeps printing", kind: "real", cmd: `while :; do echo "building $RANDOM"; sleep 0.2; done`, waiting: false},
		{name: "less (a pager)", kind: "real", cmd: "seq 1 500 | less", waiting: false, needs: "less"},
		{name: "vim (full-screen editor)", kind: "real", cmd: "vi " + filepath.Join(dir, "notes.txt"), waiting: false, needs: "vi"},
		{name: "a REPL (python3)", kind: "real", cmd: "python3 -q", waiting: true, needs: "python3"},
		{name: "a command blocked on the network", kind: "real", cmd: "nc -l 127.0.0.1 39123", waiting: false, needs: "nc"},
		{name: "sudo asks for a password", kind: "real", cmd: "sudo -k true", waiting: true, secret: true, needs: "sudo",
			standinWhen: func() bool { return !sudoPrompts }, standinCmd: "python3 " + getpass, standinLabel: "stand-in (passwordless sudo here)"},
		{name: "ssh key passphrase", kind: "real", cmd: "ssh-keygen -y -f " + key, waiting: true, secret: true, needs: "ssh-keygen"},
		{name: "an idle shell prompt", kind: "real", cmd: "", waiting: false},
	}

	probe := tabProbe{
		tmux:     tb.tmux,
		read:     os.ReadFile,
		readlink: os.Readlink,
		pids: func() ([]string, error) {
			es, err := os.ReadDir("/proc")
			var ids []string
			for _, e := range es {
				if e.Name()[0] >= '0' && e.Name()[0] <= '9' {
					ids = append(ids, e.Name())
				}
			}
			return ids, err
		},
		stty: func(ctx context.Context, tty string) (string, error) {
			out, err := exec.CommandContext(ctx, "stty", "-a", "-F", tty).Output() // #nosec G204 -- test
			return string(out), err
		},
	}
	ctx := context.Background()
	type row struct {
		c                           waitCase
		s                           waitSignals
		osc, blocked, quiet, screen bool
		combined, secret            bool
		relay                       string
	}
	var rows []row
	for i, c := range cases {
		if c.needs != "" {
			if _, err := exec.LookPath(c.needs); err != nil {
				if os.Getenv("CI") != "" {
					t.Fatalf("%s: %s is required in CI", c.name, c.needs)
				}
				t.Logf("%s: skipped, %s not installed", c.name, c.needs)
				continue
			}
		}
		if c.standinWhen != nil && c.standinWhen() {
			c.cmd, c.kind = c.standinCmd, c.standinLabel
		}
		tab, err := tb.open(ctx, fmt.Sprintf("case%d", i+1))
		if err != nil {
			t.Fatal(err)
		}
		w := tab.GetId()
		h.waitFor("prompt in "+w, func() bool { return h.shells.shell(w).State() == shellPrompt })
		if c.cmd != "" {
			h.raw("send-keys", "-t", w, "-l", c.cmd)
			h.raw("send-keys", "-t", w, "Enter")
		}
		// Past the quiet threshold, with room for a slow machine.
		time.Sleep(quietAfter + 2500*time.Millisecond)
		pane := strings.TrimSpace(h.raw("display-message", "-p", "-t", w, "#{pane_id}"))
		h.mu.Lock()
		shell := h.shells.shell(w).State()
		quiet := time.Since(h.lastOut[pane])
		h.mu.Unlock()
		s, err := probe.signals(ctx, w, shell, quiet)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		rows = append(rows, row{c: c, s: s, osc: s.soloShell(), blocked: s.soloBlocked(), quiet: s.soloQuiet(), screen: s.soloScreen(),
			combined: s.Waiting(), secret: s.Secret(), relay: s.RelayQuestion()})
		if err := tb.close(ctx, w); err != nil {
			t.Fatal(err)
		}
	}

	// The table, and each signal's errors on its own.
	yn := func(b bool) string {
		if b {
			return "waiting"
		}
		return "—"
	}
	var b strings.Builder
	b.WriteString("| # | Case | Kind | Expected | OSC 133 | Blocked on tty | Quiet | Screen asks | Combined | Secret |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	type tally struct{ falseWaiting, missed int }
	solo := map[string]*tally{"OSC 133": {}, "Blocked on tty": {}, "Quiet": {}, "Screen asks": {}, "Combined": {}}
	count := func(name string, got, want bool) {
		switch {
		case got && !want:
			solo[name].falseWaiting++
		case !got && want:
			solo[name].missed++
		}
	}
	for i, r := range rows {
		exp := yn(r.c.waiting)
		if r.c.secret {
			exp += " (secret)"
		}
		sec := "—"
		if r.secret {
			sec = "secret"
		}
		blocked := yn(r.blocked)
		if !r.s.BlockedKnown {
			blocked = "? (unreadable)"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", i+1, r.c.name, r.c.kind, exp,
			yn(r.osc), blocked, yn(r.quiet), yn(r.screen), yn(r.combined), sec)
		count("OSC 133", r.osc, r.c.waiting)
		count("Blocked on tty", r.blocked, r.c.waiting)
		count("Quiet", r.quiet, r.c.waiting)
		count("Screen asks", r.screen, r.c.waiting)
		count("Combined", r.combined, r.c.waiting)
	}
	b.WriteString("\n| Signal alone | False \"waiting\" | Missed \"waiting\" |\n| --- | --- | --- |\n")
	for _, name := range []string{"OSC 133", "Blocked on tty", "Quiet", "Screen asks", "Combined"} {
		fmt.Fprintf(&b, "| %s | %d of %d | %d of %d |\n", name, solo[name].falseWaiting, len(rows), solo[name].missed, len(rows))
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("PORTENV_SPIKE_TABLE"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The detector's own verdicts must all be right, and secret prompts
	// must never reach a relay.
	for _, r := range rows {
		if r.combined != r.c.waiting {
			t.Errorf("%s: combined says waiting=%v, want %v (signals %+v)", r.c.name, r.combined, r.c.waiting, r.s)
		}
		if r.secret != r.c.secret {
			t.Errorf("%s: secret=%v, want %v (signals %+v)", r.c.name, r.secret, r.c.secret, r.s)
		}
		if r.c.secret {
			if r.relay != SecretNotice {
				t.Errorf("%s: relayed %q, want only the notice", r.c.name, r.relay)
			}
			if r.s.CheckRelayAnswer() == nil {
				t.Errorf("%s: an answer through a relay was accepted", r.c.name)
			}
		} else if r.c.waiting && r.s.CheckRelayAnswer() != nil {
			t.Errorf("%s: a normal answer was refused", r.c.name)
		}
	}
	if len(rows) != len(cases) && os.Getenv("CI") != "" {
		t.Fatalf("ran %d of %d cases", len(rows), len(cases))
	}
}
