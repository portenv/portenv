// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"testing"
)

// TestRevertToLastSavePointKeepsTheWorkItReplaces: the home goes back to
// the save point, and what it had just before is a save in history.
func TestRevertToLastSavePointKeepsTheWorkItReplaces(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "at the point")
	point := mustSave(t, a, SavePoint)
	a.write("f", "after the point")
	a.write("g", "new file")

	res, err := a.RevertToLastSavePoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	equalFiles(t, "home after revert", a.files(), map[string]string{"f": "at the point"})
	if res.Restored.origin() != point.origin() {
		t.Fatalf("restored %s, want the save point %s", res.Restored.short(), point.short())
	}
	before := w.resolve(res.SavedBefore)
	equalFiles(t, "the save made before reverting", w.snapshotFiles(before), map[string]string{"f": "after the point", "g": "new file"})
	w.check()
}

// TestRevertKeepsTheLeaseAndLaterSavesBuildOnIt: after a revert this
// machine still holds the lease, and closing hands the reverted home to the
// next machine.
func TestRevertKeepsTheLeaseAndLaterSavesBuildOnIt(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SavePoint)
	a.write("f", "2")
	if _, err := a.RevertToLastSavePoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lease, err := b.Lease(context.Background()); err != nil || lease == nil || lease.Machine != "a" {
		t.Fatalf("lease after revert %+v %v, want a", lease, err)
	}
	if _, err := b.Resume(context.Background(), ResumeOptions{}); err == nil {
		t.Fatal("b resumed while a holds the lease after reverting")
	}
	a.write("h", "after revert")
	mustSave(t, a, SaveRelease)
	mustResume(t, b, ResumeOptions{})
	equalFiles(t, "b's home", b.files(), map[string]string{"f": "1", "h": "after revert"})
	w.check()
}

// TestRevertNeedsASavePointAndTheLease refuses without changing anything.
func TestRevertNeedsASavePointAndTheLease(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave)
	before := len(w.snapshots())
	if _, err := a.RevertToLastSavePoint(context.Background()); !errors.Is(err, ErrNoSavePoint) {
		t.Fatalf("revert with no save point: %v, want ErrNoSavePoint", err)
	}
	if got := len(w.snapshots()); got != before {
		t.Fatalf("a refused revert made %d saves", got-before)
	}

	mustSave(t, a, SavePoint)
	mustResume(t, b, ResumeOptions{TakeOver: true})
	a.write("f", "a's unsaved edit")
	if _, err := a.RevertToLastSavePoint(context.Background()); err == nil {
		t.Fatal("a reverted after b took the box over")
	}
	equalFiles(t, "a's home is untouched", a.files(), map[string]string{"f": "a's unsaved edit"})
	w.check()
}

// TestRevertKilledDuringRestoreLosesNothing: the work from before the revert
// is already saved; retrying the revert completes it.
func TestRevertKilledDuringRestoreLosesNothing(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "point")
	mustSave(t, a, SavePoint)
	a.write("f", "precious")

	// Kill the restore, after the save of the current home.
	orig := a.restic.exec
	f := &failingExecutor{Executor: orig, cmd: "restore"}
	a.restic.exec = f
	if _, err := a.RevertToLastSavePoint(context.Background()); err == nil {
		t.Fatal("revert succeeded with restore killed")
	}
	a.restic.exec = orig

	found := false
	for _, s := range w.snapshots() {
		if w.snapshotFiles(s)["f"] == "precious" {
			found = true
		}
	}
	if !found {
		t.Fatal("the work from before the revert is in no save")
	}
	if _, err := a.RevertToLastSavePoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	equalFiles(t, "home after retry", a.files(), map[string]string{"f": "point"})
	w.check()
}
