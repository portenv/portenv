// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// tabProbe gathers a tab's waiting signals (waiting.go). The /proc reads
// and stty run as the main user in the agent: the processes and the
// terminal are theirs, and a process may read its own user's
// /proc/<pid>/syscall.
type tabProbe struct {
	tmux     tmuxFunc
	read     func(path string) ([]byte, error)
	readlink func(path string) (string, error)
	// pids lists the process IDs in /proc.
	pids func() ([]string, error)
	stty func(ctx context.Context, tty string) (string, error)
}

// screenLines is how many of the last non-empty lines are read: agent CLIs
// print the question above a few choices, often inside a box.
const screenLines = 8

// Each signal's verdict on its own, for the ADR's error table.
func (s waitSignals) soloShell() bool   { return s.Shell == shellRunning }
func (s waitSignals) soloBlocked() bool { return s.BlockedOnTTY }
func (s waitSignals) soloScreen() bool  { return s.ScreenPrompt }

// waitSyscalls are the system calls a process sleeps in while it waits for
// input: read and readv on the terminal, and the poll family (poll, select,
// epoll), which Node TUIs such as Claude Code use. Numbers per architecture.
var waitSyscalls = map[string]map[int]string{
	"arm64": {63: "read", 65: "readv", 72: "pselect6", 73: "ppoll", 22: "epoll_pwait", 441: "epoll_pwait2"},
	"amd64": {0: "read", 19: "readv", 7: "poll", 23: "select", 270: "pselect6", 271: "ppoll", 232: "epoll_wait", 281: "epoll_pwait", 441: "epoll_pwait2"},
}

func (p tabProbe) signals(ctx context.Context, window string, shell shellState, quiet time.Duration) (waitSignals, error) {
	s := waitSignals{Shell: shell, Quiet: quiet}
	out, err := p.tmux(ctx, "display-message", "-p", "-t", window, "#{pane_pid}\t#{pane_tty}\t#{alternate_on}")
	if err != nil {
		return s, err
	}
	f := strings.Split(strings.TrimSpace(out), "\t")
	if len(f) != 3 {
		return s, fmt.Errorf("tab %s: unexpected pane info %q", window, out)
	}
	panePID, tty := f[0], f[1]
	s.AltScreen = f[2] == "1"
	if fg, err := p.foreground(panePID); err == nil {
		s.BlockedOnTTY, s.BlockedKnown = p.groupBlockedOn(fg, tty)
	}
	if modes, err := p.stty(ctx, tty); err == nil {
		s.EchoOff, s.Canonical = ttyModes(modes)
	}
	if screen, err := p.tmux(ctx, "capture-pane", "-p", "-t", window); err == nil {
		s.LastLines = lastLines(screen, screenLines)
		s.ScreenPrompt = screenAsks(s.LastLines)
	}
	return s, nil
}

// foreground is the terminal's foreground process group (tpgid in the
// shell's /proc/<pid>/stat), whose leader is the program in front.
func (p tabProbe) foreground(shellPID string) (string, error) {
	b, err := p.read("/proc/" + shellPID + "/stat")
	if err != nil {
		return "", err
	}
	// pid (comm) state ppid pgrp session tty_nr tpgid …; comm may hold
	// spaces and parentheses, so fields count from the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", fmt.Errorf("bad stat %q", s)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 6 {
		return "", fmt.Errorf("bad stat %q", s)
	}
	return fields[5], nil // tpgid
}

// groupBlockedOn checks the foreground process group: its leader may have
// exited (in "seq 500 | less" the leader is seq), so every live member is
// checked. Blocked if any member waits on the terminal; known if any member
// could be read.
func (p tabProbe) groupBlockedOn(pgrp, tty string) (blocked, known bool) {
	members := []string{pgrp}
	if p.pids != nil {
		if all, err := p.pids(); err == nil {
			for _, pid := range all {
				if pid == pgrp {
					continue
				}
				if b, err := p.read("/proc/" + pid + "/stat"); err == nil {
					s := string(b)
					if i := strings.LastIndexByte(s, ')'); i >= 0 {
						if f := strings.Fields(s[i+1:]); len(f) > 2 && f[2] == pgrp {
							members = append(members, pid)
						}
					}
				}
			}
		}
	}
	for _, pid := range members {
		b, k := p.blockedOn(pid, tty)
		known = known || k
		if b {
			return true, true
		}
	}
	return false, known
}

// blockedOn reports whether pid sleeps in a wait-for-input system call on
// the terminal; known is false when pid can't be read (a setuid program).
func (p tabProbe) blockedOn(pid, tty string) (blocked, known bool) {
	b, err := p.read("/proc/" + pid + "/syscall")
	if err != nil {
		return false, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return false, true // "running"
	}
	n, err := strconv.Atoi(f[0])
	if err != nil {
		return false, true
	}
	name, ok := waitSyscalls[runtime.GOARCH][n]
	if !ok {
		return false, true
	}
	isTTY := func(target string) bool { return target == tty || target == "/dev/tty" }
	if name == "read" || name == "readv" {
		// The fd read from must be the terminal: the tab's, or /dev/tty,
		// which sudo, ssh and less open to read from the person even when
		// their standard input is a pipe.
		fd, err := strconv.ParseInt(strings.TrimPrefix(f[1], "0x"), 16, 64)
		if err != nil {
			return false, true
		}
		target, err := p.readlink(fmt.Sprintf("/proc/%s/fd/%d", pid, fd))
		return err == nil && isTTY(target), true
	}
	// The poll family doesn't say which fds it waits on: require the
	// terminal as standard input.
	stdin, err := p.readlink("/proc/" + pid + "/fd/0")
	return err == nil && isTTY(stdin), true
}

// ttyModes reads echo and icanon from stty -a.
func ttyModes(stty string) (echoOff, canonical bool) {
	for _, w := range strings.Fields(stty) {
		switch w {
		case "-echo":
			echoOff = true
		case "icanon":
			canonical = true
		}
	}
	return echoOff, canonical
}

// lastLines are the last n non-empty lines of a screen capture.
func lastLines(screen string, n int) []string {
	var out []string
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{strings.TrimRight(lines[i], " ")}, out...)
		}
	}
	return out
}
