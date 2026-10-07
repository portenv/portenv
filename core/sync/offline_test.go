// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"testing"
)

// Offline resume (owner's conditions): start from the local home only if
// this machine's state says its home equals the last save it made or
// restored, and it holds the lease or released it cleanly. Anything else is
// an error.

func TestResumeOfflineAfterCleanClose(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "saved")
	mustSave(t, a, SaveRelease)

	res, err := a.ResumeOffline()
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionStartLocal || !res.Offline {
		t.Fatalf("got %+v, want an offline local start", res)
	}
	equalFiles(t, "home untouched", a.files(), map[string]string{"f": "saved"})
	st, _, _ := loadState(a.cfg.StateDir)
	if !st.Dirty {
		t.Fatal("an offline start must mark the box open (dirty)")
	}
}

func TestResumeOfflineRefusesUnsavedHome(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveAutosave) // still open: the home may have moved on
	if _, err := a.ResumeOffline(); !errors.Is(err, ErrOfflineUnsafe) {
		t.Fatalf("got %v, want ErrOfflineUnsafe", err)
	}
}

func TestResumeOfflineRefusesUnknownOrNeverSaved(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	if _, err := a.ResumeOffline(); !errors.Is(err, ErrOfflineUnsafe) {
		t.Fatalf("no state: got %v, want ErrOfflineUnsafe", err)
	}
	mustResume(t, a, ResumeOptions{}) // rule 1: nothing saved yet
	a.write("f", "1")
	if _, err := a.ResumeOffline(); !errors.Is(err, ErrOfflineUnsafe) {
		t.Fatalf("never saved: got %v, want ErrOfflineUnsafe", err)
	}
}

// reconnectScenario: a closes cleanly, works offline, and meanwhile b takes
// the box and saves. Returns the token written offline.
func reconnectScenario(t *testing.T, w *world) (a, b *machine, offlineTok string) {
	t.Helper()
	a, b = w.machine("a"), w.machine("b")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "v1")
	mustSave(t, a, SaveRelease)
	if _, err := a.ResumeOffline(); err != nil {
		t.Fatal(err)
	}
	offlineTok = "offline-" + token()
	a.write("offline.txt", offlineTok)

	mustResume(t, b, ResumeOptions{})
	b.write("f", "v2 from b")
	mustSave(t, b, SaveRelease)
	return a, b, offlineTok
}

func TestReconnectAfterTakeOverKeepsOfflineWork(t *testing.T) {
	w := newWorld(t)
	a, _, tok := reconnectScenario(t, w)
	res := mustResume(t, a, ResumeOptions{})
	if res.Rule != 4 || res.Orphaned == nil {
		t.Fatalf("reconnect: rule %d orphan %v, want rule 4 with an orphan", res.Rule, res.Orphaned)
	}
	if got := w.snapshotFiles(*res.Orphaned)["offline.txt"]; got != tok {
		t.Fatalf("offline work not in the orphaned save: %q", got)
	}
	equalFiles(t, "a now has b's version", a.files(), map[string]string{"f": "v2 from b"})
}

// failingExecutor fails the nth restic call, as if the process were killed
// there, and runs every other call normally.
type failingExecutor struct {
	Executor
	n, calls int
}

func (f *failingExecutor) Restic(ctx context.Context, args []string, cred Credentials) (ExecResult, error) {
	f.calls++
	if f.calls == f.n {
		return ExecResult{}, errors.New("killed")
	}
	return f.Executor.Restic(ctx, args, cred)
}

// TestReconnectKilledAtEveryStep kills the reconnect at each of its restic
// calls in turn, then reconnects again: the offline work is never lost, the
// repository stays consistent and the lease ends with a.
func TestReconnectKilledAtEveryStep(t *testing.T) {
	// Count the calls a full reconnect makes.
	w := newWorld(t)
	a, _, _ := reconnectScenario(t, w)
	counter := &failingExecutor{Executor: a.cfg.Executor}
	a.cfg.Executor, a.restic.exec = counter, counter
	mustResume(t, a, ResumeOptions{})
	total := counter.calls

	for n := 1; n <= total; n++ {
		t.Run("", func(t *testing.T) {
			w := newWorld(t)
			a, _, tok := reconnectScenario(t, w)
			orig := a.cfg.Executor
			f := &failingExecutor{Executor: orig, n: n}
			a.cfg.Executor, a.restic.exec = f, f
			if _, err := a.Resume(context.Background(), ResumeOptions{}); err == nil {
				t.Fatalf("killed at call %d of %d but Resume succeeded", n, total)
			}
			a.cfg.Executor, a.restic.exec = orig, orig
			mustResume(t, a, ResumeOptions{})

			found := a.files()["offline.txt"] == tok
			for _, s := range w.snapshots() {
				if w.snapshotFiles(s)["offline.txt"] == tok {
					found = true
				}
			}
			if !found {
				t.Fatalf("killed at call %d: the offline work was lost", n)
			}
			if lease, _ := a.Lease(context.Background()); lease == nil || lease.Machine != "a" {
				t.Fatalf("killed at call %d: lease %+v, want held by a", n, lease)
			}
			w.check()
		})
	}
}
