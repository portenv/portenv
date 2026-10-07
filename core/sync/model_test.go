// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
)

// TestRandomSequencesNeverLoseWork drives two machines through random
// sequences of edits, saves, closes and opens (with and without take over)
// and checks after every step that no edit was ever lost: each one is still
// in some machine's home or in some snapshot.
func TestRandomSequencesNeverLoseWork(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5}
	steps := 30
	if testing.Short() {
		seeds, steps = seeds[:1], 12
	}
	for _, seed := range seeds {
		t.Run("", func(t *testing.T) {
			runModel(t, seed, steps) // newWorld makes it parallel
		})
	}
}

func runModel(t *testing.T, seed uint64, steps int) {
	rng := rand.New(rand.NewPCG(seed, seed))
	w := newWorld(t)
	ms := []*machine{w.machine("a"), w.machine("b")}
	open := map[string]bool{}
	holder := ""         // the machine that should hold the lease, "" for none
	var written []string // every token ever written

	ctx := context.Background()
	for step := range steps {
		m := ms[rng.IntN(len(ms))]
		var op string
		switch {
		case !open[m.id]:
			op = "open"
			res, err := m.Resume(ctx, ResumeOptions{TakeOver: rng.IntN(3) == 0})
			if _, held := isLeaseHeld(err); held {
				op = "open (refused: lease held)"
				break
			}
			if err != nil {
				t.Fatalf("seed %d step %d %s open: %v", seed, step, m.id, err)
			}
			open[m.id] = true
			if res.Rule != 1 { // with nothing saved there is no lease to take yet
				holder = m.id
			}
			if res.Action == ActionNewBox {
				m.write("README", "new box")
			}
		default:
			switch rng.IntN(6) {
			case 0, 1:
				op = "edit"
				tok := token()
				m.write("t/"+tok, tok)
				written = append(written, tok)
			case 2:
				op = "autosave"
				_, err := m.Save(ctx, SaveOptions{Kind: SaveAutosave})
				if _, changed := isLeaseChanged(err); changed {
					op = "autosave (refused: lease changed)"
					break
				}
				if err != nil {
					t.Fatalf("seed %d step %d %s autosave: %v", seed, step, m.id, err)
				}
				holder = m.id
			case 3:
				op = "close"
				_, err := m.Save(ctx, SaveOptions{Kind: SaveRelease})
				if _, changed := isLeaseChanged(err); changed {
					// The user confirms: their work is saved, the other
					// version stays in history.
					op = "close (confirmed over changed lease)"
					_, err = m.Save(ctx, SaveOptions{Kind: SaveRelease, ConfirmLeaseChange: true})
				}
				if err != nil {
					t.Fatalf("seed %d step %d %s close: %v", seed, step, m.id, err)
				}
				open[m.id] = false
				holder = ""
			case 4:
				// The machine crashed or was offline and resumes again
				// without having closed: rules 3 and 4.
				res, err := m.Resume(ctx, ResumeOptions{})
				if _, held := isLeaseHeld(err); held {
					op = "reopen (refused: lease held)"
					break
				}
				if err != nil {
					t.Fatalf("seed %d step %d %s reopen: %v", seed, step, m.id, err)
				}
				op = fmt.Sprintf("reopen (rule %d)", res.Rule)
				if res.Rule != 1 {
					holder = m.id
				}
			case 5:
				// A save killed mid-way: either restic itself (no snapshot),
				// or the CLI after restic finished but before it recorded the
				// snapshot ID (a snapshot exists, the state does not know it).
				if rng.IntN(2) == 0 {
					op = "save killed inside restic"
					orig := m.restic.exec
					f := &failingExecutor{Executor: orig, n: 2} // listing, then backup
					m.restic.exec = f
					_, _ = m.Save(ctx, SaveOptions{Kind: SaveAutosave})
					m.restic.exec = orig
				} else {
					op = "save killed after restic, before recording it"
					snaps, err := m.restic.snapshots(ctx)
					if err != nil {
						t.Fatal(err)
					}
					cur, ok := current(snaps)
					if ok && ((cur.Active != "" && cur.Active != m.id) || cur.Machine != m.id) {
						op += " (skipped: the lease is elsewhere)"
						break
					}
					if empty, _ := m.homeEmpty(); empty {
						op += " (skipped: empty home)"
						break
					}
					if _, err := m.restic.backup(ctx, backupArgs{home: m.home, tags: []string{tagMachinePfx + m.id, tagActivePfx + m.id}}); err != nil {
						t.Fatal(err)
					}
					holder = m.id
				}
			}
		}
		t.Logf("step %2d %s %s", step, m.id, op)
		assertNothingLost(t, w, ms, written)
		lease, err := w.inspector().Lease(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if lease != nil {
			got = lease.Machine
		}
		if got != holder {
			t.Fatalf("seed %d step %d: lease held by %q, want %q", seed, step, got, holder)
		}
	}
}

func assertNothingLost(t *testing.T, w *world, ms []*machine, written []string) {
	t.Helper()
	have := map[string]bool{}
	for _, m := range ms {
		for _, content := range m.files() {
			have[content] = true
		}
	}
	for _, s := range w.snapshots() {
		for _, content := range w.snapshotFiles(s) {
			have[content] = true
		}
	}
	for _, tok := range written {
		if !have[tok] {
			t.Fatalf("edit %s was lost", tok)
		}
	}
}
