// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The command running in a tab, for the Move confirmation (2.3): the
// command line of the process group in front of the pane's terminal, and
// when it started. It can hold secrets (mysql -p…, TOKEN=… ./deploy): it
// goes to the app only, as one short line, and is never logged.

// maxCommandRunes bounds a command line to one line the sheet can show.
const maxCommandRunes = 160

// userHZ is the kernel's clock tick for /proc times (USER_HZ, 100 on
// Linux everywhere the agent runs).
const userHZ = 100

// foregroundCommand reads, under procRoot, the process group in front of
// the terminal of the pane's shell (panePID): its leader's command line
// and start time. Nothing when the shell itself is in front, or anything
// can't be read.
func foregroundCommand(procRoot string, panePID, hz int) (string, time.Time, bool) {
	fields, ok := statFields(procRoot, panePID)
	if !ok || len(fields) < 6 {
		return "", time.Time{}, false
	}
	// After the closing parenthesis: state ppid pgrp session tty_nr tpgid.
	tpgid, err := strconv.Atoi(fields[5])
	if err != nil || tpgid <= 0 || tpgid == panePID {
		return "", time.Time{}, false
	}
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(tpgid), "cmdline")) // #nosec G304 -- /proc of the workspace's own processes
	if err != nil || len(raw) == 0 {
		return "", time.Time{}, false
	}
	cmd := oneLine(raw)
	if cmd == "" {
		return "", time.Time{}, false
	}
	var since time.Time
	if lf, ok := statFields(procRoot, tpgid); ok && len(lf) >= 20 {
		// Field 22 overall, the 20th after the parenthesis: start time in
		// clock ticks after boot.
		if ticks, err := strconv.ParseInt(lf[19], 10, 64); err == nil {
			if boot, ok := bootTime(procRoot); ok {
				since = boot.Add(time.Duration(ticks) * time.Second / time.Duration(hz))
			}
		}
	}
	return cmd, since, true
}

// statFields is /proc/PID/stat after the command name, which may hold
// spaces and parentheses: split after the last ')'.
func statFields(procRoot string, pid int) ([]string, bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat")) // #nosec G304 -- /proc of the workspace's own processes
	if err != nil {
		return nil, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil, false
	}
	return strings.Fields(s[i+1:]), true
}

func bootTime(procRoot string) (time.Time, bool) {
	f, err := os.Open(filepath.Join(procRoot, "stat")) // #nosec G304 -- /proc
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(n, 0), true
			}
		}
	}
	return time.Time{}, false
}

// oneLine joins a NUL-separated command line with spaces, turns every
// control character into a space, collapses runs of spaces, and cuts it
// to maxCommandRunes with an ellipsis.
func oneLine(raw []byte) string {
	var b strings.Builder
	space := false
	for _, r := range string(raw) {
		if r == 0 || unicode.IsControl(r) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	s := []rune(b.String())
	if len(s) > maxCommandRunes {
		s = append(s[:maxCommandRunes-1], '…')
	}
	return string(s)
}
