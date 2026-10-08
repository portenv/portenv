// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"testing"
	"time"

	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestSaveStateComesOnlyFromRecordedState: each state the title can show,
// from the recorded sync state and what the daemon knows is happening.
func TestSaveStateComesOnlyFromRecordedState(t *testing.T) {
	saved := time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)
	never := boxsync.State{Dirty: true}
	done := boxsync.State{Snapshot: "abc", Dirty: true, SavedAt: saved}
	for _, c := range []struct {
		name                       string
		st                         boxsync.State
		saving, offline, agentDown bool
		want                       daemonv1.SaveState
		wantAt                     time.Time
	}{
		{"a new box, open", never, false, false, false, daemonv1.SaveState_SAVE_STATE_NOT_SAVED_YET, time.Time{}},
		{"saving", done, true, false, false, daemonv1.SaveState_SAVE_STATE_SAVING, saved},
		{"saved", done, false, false, false, daemonv1.SaveState_SAVE_STATE_SAVED, saved},
		{"offline", done, false, true, false, daemonv1.SaveState_SAVE_STATE_OFFLINE, saved},
		{"agent unavailable", done, false, false, true, daemonv1.SaveState_SAVE_STATE_AGENT_UNAVAILABLE, saved},
		{"agent unavailable wins over saving", done, true, false, true, daemonv1.SaveState_SAVE_STATE_AGENT_UNAVAILABLE, saved},
		{"a restored box counts as saved", boxsync.State{Tree: "t", SavedAt: saved}, false, false, false, daemonv1.SaveState_SAVE_STATE_SAVED, saved},
	} {
		got, at := saveState(c.st, c.saving, c.offline, c.agentDown)
		if got != c.want || !at.Equal(c.wantAt) {
			t.Errorf("%s: %v at %v, want %v at %v", c.name, got, at, c.want, c.wantAt)
		}
	}
}

func TestJoinStorageOnTheServerItself(t *testing.T) {
	for storage, want := range map[string]string{
		"sftp:portenv-storage@203.0.113.7:/storage":  "sftp:portenv-storage@host.portenv.internal:/storage",
		"sftp:portenv-storage@198.51.100.1:/storage": "sftp:portenv-storage@198.51.100.1:/storage",
	} {
		if got := joinStorage(localBox(storage), "portenv@203.0.113.7"); got != want {
			t.Errorf("joinStorage(%s) = %s, want %s", storage, got, want)
		}
	}
}

func localBox(storage string) local.BoxConfig { return local.BoxConfig{Storage: storage} }
