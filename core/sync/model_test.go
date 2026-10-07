// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
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
			// The other machine loses the lease if this was a take over.
			if res.Action == ActionNewBox {
				m.write("README", "new box")
			}
		default:
			switch rng.IntN(4) {
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
			}
		}
		t.Logf("step %2d %s %s", step, m.id, op)
		assertNothingLost(t, w, ms, written)
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
