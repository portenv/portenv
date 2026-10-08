// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestVersionIsCheckedOncePerBinary: restic is pinned and hash-checked when
// installed, so opening a box runs `restic version` only when the binary
// changes, never on every open.
func TestVersionIsCheckedOncePerBinary(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	bin := filepath.Join(dir, "restic")
	script := "#!/bin/sh\necho run >> " + runs + "\necho 'restic 0.18.0 compiled with go1.26 on darwin/arm64'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}
	count := func() int {
		b, _ := os.ReadFile(runs) // #nosec G304 -- the test's own file
		return strings.Count(string(b), "run")
	}
	cfg := Config{
		Executor:   LocalExecutor{Bin: bin},
		Repository: filepath.Join(dir, "repo"),
		Password:   []byte("pw"),
		BoxID:      "box-1",
		MachineID:  "mac-1",
		HomeDir:    filepath.Join(dir, "home"),
		StateDir:   filepath.Join(dir, "state"),
	}
	open := func() {
		t.Helper()
		if _, err := Open(cfg); err != nil {
			t.Fatal(err)
		}
	}
	open()
	open()
	open()
	if n := count(); n != 1 {
		t.Fatalf("three opens ran restic version %d times, want 1", n)
	}
	// A new binary (an update): checked again, once.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	open()
	open()
	if n := count(); n != 2 {
		t.Fatalf("after the binary changed: %d runs, want 2", n)
	}
	// A binary that fails the check is never recorded as checked.
	bad := "#!/bin/sh\necho run >> " + runs + "\necho 'restic 0.9.0'\n"
	if err := os.WriteFile(bin, []byte(bad), 0o700); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}
	for range 2 {
		if _, err := Open(cfg); err == nil || !strings.Contains(err.Error(), "too old") {
			t.Fatalf("an old restic: %v, want too old", err)
		}
	}
	if n := count(); n != 4 {
		t.Fatalf("a failing binary is checked every time: %d runs, want 4", n)
	}
}
