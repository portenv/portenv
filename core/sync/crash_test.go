// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fill writes n files of size bytes of random (incompressible) data.
func fill(t *testing.T, dir string, n, size int) {
	t.Helper()
	buf := make([]byte, size)
	for i := range n {
		_, _ = rand.Read(buf)
		p := filepath.Join(dir, fmt.Sprintf("data/%03d/%05d.bin", i%50, i))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, buf, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestKilledSaveLeavesRepositoryConsistent kills restic part-way through
// saves, then checks the repository and that the next save succeeds and
// contains everything.
func TestKilledSaveLeavesRepositoryConsistent(t *testing.T) {
	if testing.Short() {
		t.Skip("writes ~40 MB")
	}
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("first", "1")
	before := mustSave(t, a, SaveAutosave)
	stateBefore, _, err := loadState(a.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}

	fill(t, a.home, 2000, 20<<10)
	for _, after := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, 700 * time.Millisecond} {
		ctx, cancel := context.WithTimeout(context.Background(), after)
		_, err := a.Save(ctx, SaveOptions{Kind: SaveAutosave})
		cancel()
		if err == nil {
			t.Logf("save finished within %v; kill came too late", after)
			continue
		}
		st, _, err := loadState(a.cfg.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Tree != stateBefore.Tree {
			t.Fatalf("a killed save updated the state (tree %s, want %s)", st.Tree, stateBefore.Tree)
		}
	}
	w.check()

	s := mustSave(t, a, SaveAutosave)
	if s.Tree == before.Tree {
		t.Fatal("the save after the crash did not record the new files")
	}
	got := w.snapshotFiles(s)
	if len(got) != 2001 {
		t.Fatalf("snapshot has %d files, want 2001", len(got))
	}
	w.check()
}
