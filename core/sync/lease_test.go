// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestSnapshotConventions(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	auto := mustSave(t, a, SaveAutosave)
	point := mustSave(t, a, SavePoint)
	release := mustSave(t, a, SaveRelease)

	for _, s := range w.snapshots() {
		if s.Hostname != "portenv" {
			t.Errorf("snapshot %s host %q, want portenv", s.ID[:8], s.Hostname)
		}
		if !hasTag(s, "machine:a") {
			t.Errorf("snapshot %s tags %v, want machine:a", s.ID[:8], s.Tags)
		}
	}
	if auto.Kind != KindAutosave || point.Kind != KindPoint || release.Kind != KindRelease {
		t.Fatalf("kinds %v %v %v", auto.Kind, point.Kind, release.Kind)
	}
	if !hasTag(point, "point") || !hasTag(release, "release") {
		t.Fatalf("point tags %v, release tags %v", point.Tags, release.Tags)
	}
	// Release ends the lease: no snapshot carries an active tag.
	if got := tagsWithPrefix(w.snapshots(), "active:"); len(got) != 0 {
		t.Fatalf("active tags after release: %v", got)
	}
	if lease, err := a.Lease(context.Background()); err != nil || lease != nil {
		t.Fatalf("lease after release = %+v, %v; want none", lease, err)
	}
}

func TestOnlyNewestSnapshotCarriesTheLease(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	for i := range 3 {
		a.write("f", string(rune('a'+i)))
		mustSave(t, a, SaveAutosave)
	}
	snaps := w.snapshots()
	active := tagsWithPrefix(snaps, "active:")
	if len(active) != 1 {
		t.Fatalf("active tags %v, want one", active)
	}
	newest := snaps[len(snaps)-1]
	if !hasTag(newest, "active:a") {
		t.Fatalf("newest snapshot tags %v, want active:a", newest.Tags)
	}
}

func TestStaleLeaseIsReportedNotReleased(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave)

	later := func() time.Time { return time.Now().Add(11 * time.Minute) }
	b := w.machineAt("b", later)
	lease, err := b.Lease(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("lease = %+v, %v", lease, err)
	}
	if !lease.Stale {
		t.Fatal("a lease silent for 11 minutes is not marked stale")
	}
	if _, err := b.Resume(context.Background(), ResumeOptions{}); err == nil {
		t.Fatal("a stale lease was released automatically; the user must decide")
	}

	soon := w.machineAt("c", func() time.Time { return time.Now().Add(2 * time.Minute) })
	if lease, _ := soon.Lease(context.Background()); lease == nil || lease.Stale {
		t.Fatalf("a 2-minute-old lease: %+v, want held and fresh", lease)
	}
}

func TestHistoryIsOldestFirstWithKinds(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave)
	mustSave(t, a, SavePoint)
	mustSave(t, a, SaveRelease)
	hist, err := a.History(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []SnapshotKind
	for _, s := range hist {
		kinds = append(kinds, s.Kind)
	}
	if want := []SnapshotKind{KindAutosave, KindPoint, KindRelease}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
}

func TestRetentionKeepsOrphanedSnapshots(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave)
	a.write("offline", "x")
	mustResume(t, b, ResumeOptions{TakeOver: true})
	b.write("f", "2")
	mustSave(t, b, SaveRelease)
	res := mustResume(t, a, ResumeOptions{}) // rule 4: an orphan
	if res.Orphaned == nil {
		t.Fatal("no orphan made")
	}
	for i := range 22 {
		a.write("f", string(rune('A'+i)))
		if _, err := a.restic.backup(context.Background(), backupArgs{home: a.home, tags: []string{"machine:a"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Forget(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	snaps := w.snapshots()
	if !slices.ContainsFunc(snaps, func(s Snapshot) bool { return s.Tree == res.Orphaned.Tree && hasTag(s, "orphaned") }) {
		t.Fatal("retention removed an orphaned snapshot")
	}
	w.check()
}

func TestParseSnapshotTags(t *testing.T) {
	cases := []struct {
		tags    []string
		kind    SnapshotKind
		machine string
		active  string
	}{
		{[]string{"machine:m1", "active:m1"}, KindAutosave, "m1", "m1"},
		{[]string{"machine:m2", "point", "active:m2"}, KindPoint, "m2", "m2"},
		{[]string{"release", "machine:m3"}, KindRelease, "m3", ""},
		{[]string{"machine:m4", "orphaned"}, KindOrphaned, "m4", ""},
		{nil, KindAutosave, "", ""},
	}
	for _, c := range cases {
		s := Snapshot{Tags: c.tags}
		s.parseTags()
		if s.Kind != c.kind || s.Machine != c.machine || s.Active != c.active {
			t.Errorf("%v: got %v %q %q, want %v %q %q", c.tags, s.Kind, s.Machine, s.Active, c.kind, c.machine, c.active)
		}
	}
}
