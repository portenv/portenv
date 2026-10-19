// SPDX-License-Identifier: Apache-2.0

package local

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Two (or more) processes starting together on a fresh Mac must agree on one
// machine ID, and none may read it empty (R-0299).
func TestMachineIDConcurrentCallersAgree(t *testing.T) {
	for round := range 50 {
		dir := t.TempDir()
		const callers = 8
		ids := make([]string, callers)
		errs := make([]error, callers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ids[i], errs[i] = (&Env{Dir: dir}).machineID()
			}()
		}
		close(start)
		wg.Wait()
		for i := range callers {
			if errs[i] != nil {
				t.Fatalf("round %d caller %d: %v", round, i, errs[i])
			}
			if ids[i] == "" {
				t.Fatalf("round %d caller %d read an empty machine ID", round, i)
			}
			if ids[i] != ids[0] {
				t.Fatalf("round %d: callers disagree: %q and %q", round, ids[0], ids[i])
			}
		}
		b, err := os.ReadFile(filepath.Join(dir, "machine-id"))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != ids[0]+"\n" {
			t.Fatalf("round %d: file holds %q, callers got %q", round, got, ids[0])
		}
		left, _ := filepath.Glob(filepath.Join(dir, ".machine-id-*"))
		if len(left) != 0 {
			t.Fatalf("round %d: temporary files left: %v", round, left)
		}
		if fi, err := os.Stat(filepath.Join(dir, "machine-id")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("round %d: machine-id mode %v, %v; want 0600", round, fi.Mode().Perm(), err)
		}
	}
}

// An existing ID is kept as it is.
func TestMachineIDKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "machine-id"), []byte("studio-abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := (&Env{Dir: dir}).machineID()
	if err != nil || id != "studio-abc123" {
		t.Fatalf("machineID = %q, %v; want studio-abc123", id, err)
	}
}

// An empty file that an older writer is still filling is read again, and
// what it ends up holding wins.
func TestMachineIDWaitsForAnEmptyFileBeingWritten(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(3 * machineIDWait)
		_ = os.WriteFile(p, []byte("older-writer-1\n"), 0o600)
	}()
	id, err := (&Env{Dir: dir}).machineID()
	if err != nil || id != "older-writer-1" {
		t.Fatalf("machineID = %q, %v; want older-writer-1", id, err)
	}
}

// An empty file that stays empty (a writer killed mid-write) is replaced
// with a new ID rather than wedging every start.
func TestMachineIDReplacesAnEmptyFileThatStaysEmpty(t *testing.T) {
	old := machineIDRetries
	machineIDRetries = 2
	t.Cleanup(func() { machineIDRetries = old })
	dir := t.TempDir()
	p := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := (&Env{Dir: dir}).machineID()
	if err != nil || id == "" {
		t.Fatalf("machineID = %q, %v; want a new ID", id, err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != id+"\n" {
		t.Fatalf("file holds %q, want %q", b, id)
	}
}
