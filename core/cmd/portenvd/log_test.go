// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheLogIsPrivate: run by launchd (PORTENVD_LOG=file), portenvd writes
// its own log under ~/Library/Logs/Portenv: the file 0600 in a 0700
// folder, appended to across restarts.
func TestTheLogIsPrivate(t *testing.T) {
	home := t.TempDir()
	for range 2 {
		f, err := openLog(home)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("line\n"); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	dir := filepath.Join(home, "Library", "Logs", "Portenv")
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("log folder: %v %v", fi.Mode().Perm(), err)
	}
	fi, err := os.Stat(filepath.Join(dir, "portenvd.log"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log file: %v %v", fi.Mode().Perm(), err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "portenvd.log")); string(b) != "line\nline\n" {
		t.Fatalf("not appended: %q", b)
	}
}
