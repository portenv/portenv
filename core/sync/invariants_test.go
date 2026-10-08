// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"testing"
)

// The invariants from docs/PLAN.md (Save, resume and leases). Each test
// names the invariant it enforces.

func TestInvariantNeverSaveAnEmptyHome(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	for _, kind := range []SaveKind{SaveAutosave, SavePoint, SaveRelease} {
		if _, err := a.Save(context.Background(), SaveOptions{Kind: kind}); !errors.Is(err, ErrEmptyHome) {
			t.Fatalf("Save(%v) of an empty home: got %v, want ErrEmptyHome", kind, err)
		}
	}
	if n := len(w.snapshots()); n != 0 {
		t.Fatalf("%d snapshots of an empty home were made", n)
	}
}

func TestInvariantNeverRestoreOverUnsavedChanges(t *testing.T) {
	// Exercised end to end by rule 4; here the internal guard itself.
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "a")
	s := mustSave(t, a, SaveRelease)

	mustResume(t, b, ResumeOptions{})
	b.write("unsaved", "b's work") // b is open: dirty
	if err := b.restoreSnapshot(context.Background(), s); !errors.Is(err, errUnsavedChanges) {
		t.Fatalf("restore over a dirty home: got %v, want errUnsavedChanges", err)
	}
	if b.files()["unsaved"] != "b's work" {
		t.Fatal("unsaved work was overwritten")
	}
}

func TestInvariantNoTwoLeaseHoldersWithoutTakeOver(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave)

	if _, err := b.Resume(context.Background(), ResumeOptions{}); err == nil {
		t.Fatal("b resumed while a holds the lease")
	}
	lease, err := b.Lease(context.Background())
	if err != nil || lease == nil || lease.Machine != "a" {
		t.Fatalf("lease = %+v, %v; want held by a", lease, err)
	}

	// Explicit take over moves the lease to b.
	mustResume(t, b, ResumeOptions{TakeOver: true})
	lease, err = a.Lease(context.Background())
	if err != nil || lease == nil || lease.Machine != "b" {
		t.Fatalf("after take over lease = %+v, %v; want held by b", lease, err)
	}
	if got := tagsWithPrefix(w.snapshots(), "active:"); len(got) != 1 {
		t.Fatalf("active tags %v, want exactly one", got)
	}
}

func TestInvariantSaveCompleteOnlyWithSnapshotAndState(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	s := mustSave(t, a, SavePoint)
	if s.ID == "" || s.Tree == "" {
		t.Fatalf("save returned %+v without a snapshot ID and tree", s)
	}
	st, ok, err := loadState(a.cfg.StateDir)
	if err != nil || !ok {
		t.Fatalf("state after save: %+v %v %v", st, ok, err)
	}
	if st.Snapshot != s.origin() {
		t.Fatalf("state records snapshot %s, want the saved snapshot %s", st.Snapshot, s.origin())
	}
}

func TestInvariantSaveOverChangedLeaseNeedsConfirmation(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "a1")
	mustSave(t, a, SaveAutosave)
	a.write("g", "a's later work")

	mustResume(t, b, ResumeOptions{TakeOver: true})
	b.write("f", "b1")
	bSnap := mustSave(t, b, SaveAutosave)

	// a, unaware, autosaves: refused.
	_, err := a.Save(context.Background(), SaveOptions{Kind: SaveAutosave})
	changed, ok := isLeaseChanged(err)
	if !ok {
		t.Fatalf("got %v, want LeaseChangedError", err)
	}
	if changed.Newest.Tree != bSnap.Tree {
		t.Fatalf("error names %s, want b's snapshot", changed.Newest.ID)
	}

	// With confirmation a saves, and b's version stays in history.
	aSnap, err := a.Save(context.Background(), SaveOptions{Kind: SaveAutosave, ConfirmLeaseChange: true})
	if err != nil {
		t.Fatal(err)
	}
	aSnap = w.resolve(aSnap)
	var sawB bool
	for _, s := range w.snapshots() {
		if s.Tree == bSnap.Tree {
			sawB = true
		}
	}
	if !sawB {
		t.Fatal("b's version is gone from history")
	}
	equalFiles(t, "a's confirmed save", w.snapshotFiles(aSnap), map[string]string{"f": "a1", "g": "a's later work"})
}

func TestOwnUnrecordedSaveIsNotAConflict(t *testing.T) {
	// portenvd was killed after restic finished but before the state was
	// written. The finished snapshot is this machine's own: not a conflict.
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave)
	a.write("f", "2")
	if _, err := a.restic.backup(context.Background(), backupArgs{
		home: a.home,
		tags: []string{"machine:a", "active:a"},
	}); err != nil {
		t.Fatal(err)
	}
	a.write("f", "3")
	s, err := a.Save(context.Background(), SaveOptions{Kind: SaveAutosave})
	if err != nil {
		t.Fatalf("save after an unrecorded own save: %v", err)
	}
	s = w.resolve(s)
	equalFiles(t, "latest", w.snapshotFiles(s), map[string]string{"f": "3"})
}
