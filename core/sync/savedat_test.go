// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"testing"
	"time"
)

// TestSavedAtComesOnlyFromRecordedSaves: a new box records no save time;
// a save records its snapshot's time; another machine restoring that save
// records the same time.
func TestSavedAtComesOnlyFromRecordedSaves(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	if st, _, _ := a.LocalState(); !st.SavedAt.IsZero() {
		t.Fatalf("a new box records a save time %v", st.SavedAt)
	}
	a.write("f", "1")
	s := w.resolve(mustSave(t, a, SaveRelease))
	st, _, _ := a.LocalState()
	if d := st.SavedAt.Sub(s.Time); d < -time.Second || d > time.Second {
		t.Fatalf("saved at %v, want the snapshot's time %v", st.SavedAt, s.Time)
	}
	mustResume(t, b, ResumeOptions{})
	if st, _, _ := b.LocalState(); !st.SavedAt.Equal(s.Time) { // restored: exactly the snapshot's
		t.Fatalf("after restoring, saved at %v, want %v", st.SavedAt, s.Time)
	}
	if _, err := b.Resume(context.Background(), ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
}
