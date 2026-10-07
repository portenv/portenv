// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The five resume rules from docs/PLAN.md (Save, resume and leases), in order.

func TestResumeRule1NothingSaved(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	res := mustResume(t, a, ResumeOptions{})
	if res.Rule != 1 || res.Action != ActionNewBox {
		t.Fatalf("got rule %d action %v, want rule 1 ActionNewBox", res.Rule, res.Action)
	}
	if len(a.files()) != 0 {
		t.Fatal("rule 1 must not touch the home; the agent creates it from the skeleton")
	}
}

func TestResumeRule1KeepsExistingLocalHome(t *testing.T) {
	// The repository is empty but this machine already has a home (for
	// example the repository was recreated): keep it, never treat it as new.
	w := newWorld(t)
	a := w.machine("a")
	a.write("project/notes.txt", "local work")
	res := mustResume(t, a, ResumeOptions{})
	if res.Action == ActionNewBox {
		t.Fatal("an existing local home was treated as a new box")
	}
	equalFiles(t, "home", a.files(), map[string]string{"project/notes.txt": "local work"})
}

func TestResumeRule2LeaseHeldElsewhere(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave) // a holds the lease

	b.write("mine", "b's unsaved file")
	_, err := b.Resume(context.Background(), ResumeOptions{})
	held, ok := isLeaseHeld(err)
	if !ok {
		t.Fatalf("got %v, want LeaseHeldError", err)
	}
	if held.Lease.Machine != "a" {
		t.Fatalf("lease machine %q, want a", held.Lease.Machine)
	}
	equalFiles(t, "b's home untouched", b.files(), map[string]string{"mine": "b's unsaved file"})
}

func TestResumeRule3LocalEqualsNewest(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveRelease)

	res := mustResume(t, a, ResumeOptions{})
	if res.Rule != 3 || res.Action != ActionStartLocal {
		t.Fatalf("got rule %d action %v, want rule 3 ActionStartLocal", res.Rule, res.Action)
	}
	equalFiles(t, "home", a.files(), map[string]string{"f": "1"})
}

func TestResumeLocalChangesOnTopOfNewestStartLocally(t *testing.T) {
	// The box was open, an autosave ran, more edits followed, then the
	// machine crashed. Reopening on the same machine keeps those edits.
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "saved")
	mustSave(t, a, SaveAutosave)
	a.write("g", "unsaved")

	res := mustResume(t, a, ResumeOptions{})
	if res.Action != ActionStartLocal {
		t.Fatalf("got action %v, want ActionStartLocal", res.Action)
	}
	equalFiles(t, "home", a.files(), map[string]string{"f": "saved", "g": "unsaved"})
}

func TestResumeRule4UnsavedLocalAndNewerSnapshot(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "v1")
	mustSave(t, a, SaveAutosave) // a holds the lease
	// a keeps working offline; nothing reaches the repository.
	a.write("offline.txt", "a's offline work")

	// The user takes the box over on b and closes it there.
	mustResume(t, b, ResumeOptions{TakeOver: true}) // b restores v1
	b.write("f", "v2 from b")
	mustSave(t, b, SaveRelease)

	res := mustResume(t, a, ResumeOptions{})
	if res.Rule != 4 || res.Action != ActionRestoredKeptLocal {
		t.Fatalf("got rule %d action %v, want rule 4 ActionRestoredKeptLocal", res.Rule, res.Action)
	}
	equalFiles(t, "a's home is b's version", a.files(), map[string]string{"f": "v2 from b"})
	if res.Orphaned == nil || !hasTag(*res.Orphaned, "orphaned") {
		t.Fatalf("orphaned snapshot missing: %+v", res.Orphaned)
	}
	equalFiles(t, "orphaned snapshot keeps a's work", w.snapshotFiles(*res.Orphaned),
		map[string]string{"f": "v1", "offline.txt": "a's offline work"})
}

func TestResumeRule5IncrementalRestore(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("project/main.go", "package main")
	a.write("project/old.txt", "old")
	mustSave(t, a, SaveRelease)

	res := mustResume(t, b, ResumeOptions{})
	if res.Rule != 5 || res.Action != ActionRestored {
		t.Fatalf("got rule %d action %v, want rule 5 ActionRestored", res.Rule, res.Action)
	}
	equalFiles(t, "b's home", b.files(), a.files())
	mustSave(t, b, SaveRelease)

	// a changes the home again; b's next resume restores only the change and
	// deletes what a deleted.
	mustResume(t, a, ResumeOptions{})
	a.remove("project/old.txt")
	a.write("project/new.txt", "new")
	mustSave(t, a, SaveRelease)

	res = mustResume(t, b, ResumeOptions{})
	if res.Rule != 5 {
		t.Fatalf("got rule %d, want 5", res.Rule)
	}
	equalFiles(t, "b's home after second restore", b.files(),
		map[string]string{"project/main.go": "package main", "project/new.txt": "new"})
}

func TestResumeUnknownStateWithLocalContentKeepsIt(t *testing.T) {
	// The per-machine state was lost but a home is present: treat it as
	// unsaved, save it as orphaned before restoring.
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "a")
	mustSave(t, a, SaveRelease)

	b.write("stray.txt", "b had something")
	if err := os.RemoveAll(filepath.Join(w.root, "b", "state")); err != nil {
		t.Fatal(err)
	}
	res := mustResume(t, b, ResumeOptions{})
	if res.Action != ActionRestoredKeptLocal || res.Orphaned == nil {
		t.Fatalf("got action %v orphaned %v, want ActionRestoredKeptLocal with an orphan", res.Action, res.Orphaned)
	}
	equalFiles(t, "orphan", w.snapshotFiles(*res.Orphaned), map[string]string{"stray.txt": "b had something"})
	equalFiles(t, "b's home", b.files(), map[string]string{"f": "a"})
}

// TestOrphanIsNeverTheCurrentSave: rule 4 saves the local home as orphaned
// just before restoring, so the orphan is the newest snapshot by time. It
// must not count as the current save: the lease stays with the machine that
// resumed, and the next machine resumes the real latest work.
func TestOrphanIsNeverTheCurrentSave(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "v1")
	mustSave(t, a, SaveAutosave)
	a.write("offline.txt", "stale")
	mustResume(t, b, ResumeOptions{TakeOver: true})
	b.write("f", "v2 from b")
	mustSave(t, b, SaveRelease)

	res := mustResume(t, a, ResumeOptions{}) // rule 4
	if res.Rule != 4 {
		t.Fatalf("rule %d, want 4", res.Rule)
	}
	lease, err := b.Lease(context.Background())
	if err != nil || lease == nil || lease.Machine != "a" {
		t.Fatalf("after rule 4 the lease is %+v (%v); want held by a", lease, err)
	}
	if _, err := b.Resume(context.Background(), ResumeOptions{}); err == nil {
		t.Fatal("b resumed while a holds the box")
	}

	a.write("f", "v3 from a")
	mustSave(t, a, SaveRelease)
	mustResume(t, b, ResumeOptions{})
	equalFiles(t, "b gets a's latest work, not the orphan", b.files(), map[string]string{"f": "v3 from a"})
}
