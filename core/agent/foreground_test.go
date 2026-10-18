// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProcRoot writes a /proc with a shell (pid 10) whose terminal's foreground
// process group is led by pid 20, started 500 s after boot.
func fakeProcRoot(t *testing.T, cmdline string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// stat: pid (comm) state ppid pgrp session tty_nr tpgid ... field 22 starttime.
	stat := func(pid, tpgid int, comm string, start int) string {
		fields := []string{fmt.Sprint(pid), "(" + comm + ")", "S", "1", fmt.Sprint(pid), "1", "34816", fmt.Sprint(tpgid)}
		for len(fields) < 21 {
			fields = append(fields, "0")
		}
		fields = append(fields, fmt.Sprint(start))
		return strings.Join(fields, " ") + " 0 0\n"
	}
	write("10/stat", stat(10, 20, "bash", 100))
	write("20/stat", stat(20, 20, "a (weird) name", 500*100))
	write("20/cmdline", cmdline)
	write("stat", "cpu 1 2 3\nbtime 1791900000\nprocesses 9\n")
	return root
}

// TestForegroundCommand: the command the person started in a tab, its whole
// command line on one line, and when it started.
func TestForegroundCommand(t *testing.T) {
	root := fakeProcRoot(t, "mysql\x00-u\x00root\x00-pSECRET\x00")
	cmd, since, ok := foregroundCommand(root, 10, 100)
	if !ok || cmd != "mysql -u root -pSECRET" {
		t.Fatalf("got %q, %v", cmd, ok)
	}
	if want := time.Unix(1791900000+500, 0); !since.Equal(want) {
		t.Fatalf("started %v, want %v", since, want)
	}
}

// TestForegroundCommandIsOneShortLine: control characters and newlines
// can't break the line, and a long one is cut with an ellipsis.
func TestForegroundCommandIsOneShortLine(t *testing.T) {
	root := fakeProcRoot(t, "printf\x00a\nb\x1b[31m\tc\x00"+strings.Repeat("x", 300)+"\x00")
	cmd, _, ok := foregroundCommand(root, 10, 100)
	if !ok || strings.ContainsAny(cmd, "\n\r\t\x1b") || !strings.HasPrefix(cmd, "printf a b [31m c x") {
		t.Fatalf("got %q", cmd)
	}
	if n := len([]rune(cmd)); n != maxCommandRunes || !strings.HasSuffix(cmd, "…") {
		t.Fatalf("%d runes, want %d ending in …: %q", n, maxCommandRunes, cmd)
	}
}

// TestNoForegroundCommand: the shell itself in front, a vanished process,
// or no /proc: nothing.
func TestNoForegroundCommand(t *testing.T) {
	root := fakeProcRoot(t, "x\x00")
	if _, _, ok := foregroundCommand(root, 20, 100); ok {
		t.Fatal("the leader itself in front is the shell's own group")
	}
	if _, _, ok := foregroundCommand(root, 99, 100); ok {
		t.Fatal("a process that's gone")
	}
	if _, _, ok := foregroundCommand(filepath.Join(root, "nope"), 10, 100); ok {
		t.Fatal("no /proc")
	}
}
