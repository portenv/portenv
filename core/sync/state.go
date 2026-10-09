// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const stateFile = "state.json"

// State is what this machine knows about its copy of a box's home. It is
// never uploaded.
type State struct {
	// Tree is the content hash of the snapshot the local home was last
	// synced to (saved from or restored to). Comparing trees, not snapshot
	// IDs, survives restic tag rewriting IDs.
	Tree string `json:"tree"`
	// Snapshot is the original ID of that snapshot (restic tag rewrites a
	// snapshot's ID but keeps the first one as "original"). A save records
	// it straight from restic's summary; Tree is filled in at the next
	// listing.
	Snapshot string `json:"snapshot,omitempty"`
	// KeyHint is the ID of this machine's own repository key, so restic
	// tries it first instead of deriving every key in turn.
	KeyHint string `json:"key_hint,omitempty"`
	// Dirty means the box was opened since that sync, so the local home may
	// hold unsaved changes.
	Dirty bool `json:"dirty"`
	// Lease records this machine's last lease action: LeaseHeld after a
	// resume or an autosave or save point, LeaseReleased after a clean
	// close. Empty when unknown (for example a resume cut short).
	Lease string `json:"lease,omitempty"`
	// SavedAt is when the snapshot the home was last synced to was made:
	// what "Saved at …" shows. Zero when nothing was ever saved.
	SavedAt time.Time `json:"saved_at,omitzero"`
}

// Recorded lease states.
const (
	LeaseHeld     = "held"
	LeaseReleased = "released"
)

// loadState reads the state in dir. ok is false when there is none.
func loadState(dir string) (st State, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile)) // #nosec G304 -- fixed name in the configured state dir
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("read sync state: %w", err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, false, fmt.Errorf("parse sync state: %w", err)
	}
	return st, true, nil
}

// saveState writes st to dir atomically: a crash leaves either the old or
// the new state, never a torn file.
func saveState(dir string, st State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create sync state dir: %w", err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, stateFile+".*")
	if err != nil {
		return fmt.Errorf("write sync state: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write sync state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync state to disk: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write sync state: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, stateFile)); err != nil {
		return fmt.Errorf("replace sync state: %w", err)
	}
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- the configured state dir
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
