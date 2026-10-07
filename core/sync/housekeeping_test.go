// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"slices"
	"testing"
)

// TestReadersOnlyLookAtTheCurrentSave: leftover active tags on older
// snapshots (housekeeping clears them later) never count as a lease.
func TestReadersOnlyLookAtTheCurrentSave(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave) // active:a
	mustSave(t, a, SaveRelease)  // current save: no active tag; the autosave keeps active:a
	if got := tagsWithPrefix(w.snapshots(), "active:"); len(got) == 0 {
		t.Fatal("expected a leftover active tag on the older autosave (cleared only by housekeeping)")
	}
	if lease, err := b.Lease(context.Background()); err != nil || lease != nil {
		t.Fatalf("lease %+v %v: a leftover tag on an older save must not count", lease, err)
	}
	hist, err := b.History(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hist {
		if h.Active != "" {
			t.Fatalf("history shows %s open on %s from a leftover tag", h.ID[:8], h.Active)
		}
	}
	mustResume(t, b, ResumeOptions{}) // not refused
}

// TestHousekeepingClearsLeftoverTags removes active tags from every snapshot
// but the current save.
func TestHousekeepingClearsLeftoverTags(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	for i := range 3 {
		a.write("f", string(rune('a'+i)))
		mustSave(t, a, SaveAutosave)
	}
	if err := a.Housekeep(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	snaps := w.snapshots()
	active := tagsWithPrefix(snaps, "active:")
	cur, _ := current(snaps)
	if len(active) != 1 || !hasTag(cur, "active:a") {
		t.Fatalf("after housekeeping active tags %v, want only the current save's", active)
	}
	w.check()
}

// TestRetentionNeverRemovesTheCurrentSave: with more newer orphaned saves
// than keep-last covers, restic's policy alone would remove the current
// save; housekeeping keeps it.
func TestRetentionNeverRemovesTheCurrentSave(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "older")
	mustSave(t, a, SaveAutosave) // restic keeps the oldest snapshot anyway
	a.write("f", "current")
	cur := mustSave(t, a, SaveRelease)
	for i := range 21 {
		a.write("f", string(rune('A'+i)))
		if _, err := a.restic.backup(context.Background(), backupArgs{home: a.home, tags: []string{"machine:a", tagOrphaned}}); err != nil {
			t.Fatal(err)
		}
	}
	err := a.Housekeep(context.Background(), true)
	snaps := w.snapshots()
	if !slices.ContainsFunc(snaps, func(s Snapshot) bool { return s.origin() == cur.origin() }) {
		t.Fatalf("housekeeping removed the current save (err %v)", err)
	}
	w.check()
}
