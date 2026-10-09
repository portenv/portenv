// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// boxWithState makes a box whose recorded sync state is state.json.
func boxWithState(t *testing.T, s *Server, stateJSON string) local.BoxConfig {
	t.Helper()
	c := local.BoxConfig{ID: "box-1", Name: "acme-api", Image: "img"}
	if err := os.MkdirAll(filepath.Join(s.env.Dir, "boxes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.env.SaveBox(c); err != nil {
		t.Fatal(err)
	}
	if stateJSON != "" {
		dir := filepath.Join(s.env.Dir, "state", c.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stateJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// TestABoxLeftOpenByAnEndedDaemon: portenvd restarted (crash, update,
// login item relaunch) while a box was open. The new portenvd hasn't opened
// it and has no keys, but the box isn't closed: it reports the real state,
// "Not saved since" the last save, and that the box needs opening again
// (the app then hands the keys over and reopens it). It needs no key to
// say so.
func TestABoxLeftOpenByAnEndedDaemon(t *testing.T) {
	t.Setenv("PORTENV_KEYS", "file")
	s := quietServer(t)
	boxWithState(t, s, `{"snapshot":"abc","lease":"held","saved_at":"2026-10-09T05:56:40Z"}`)
	r, err := s.GetBoxState(context.Background(), &daemonv1.GetBoxStateRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetState() != daemonv1.SaveState_SAVE_STATE_NOT_SAVED || !r.GetInterrupted() {
		t.Fatalf("state %v interrupted %v, want NOT_SAVED and interrupted", r.GetState(), r.GetInterrupted())
	}
	if want := time.Date(2026, 10, 9, 5, 56, 40, 0, time.UTC); !r.GetSavedAt().AsTime().Equal(want) {
		t.Fatalf("saved at %v, want %v", r.GetSavedAt().AsTime(), want)
	}
}

// TestAClosedBoxStaysClosed: released lease (closed and saved), or never
// opened: closed, nothing to reopen.
func TestAClosedBoxStaysClosed(t *testing.T) {
	for _, st := range []string{`{"snapshot":"abc","lease":"released","saved_at":"2026-10-09T05:56:40Z"}`, ""} {
		s := quietServer(t)
		boxWithState(t, s, st)
		r, err := s.GetBoxState(context.Background(), &daemonv1.GetBoxStateRequest{Name: "acme-api"})
		if err != nil {
			t.Fatal(err)
		}
		if r.GetState() != daemonv1.SaveState_SAVE_STATE_CLOSED || r.GetInterrupted() {
			t.Fatalf("%q: state %v interrupted %v, want CLOSED", st, r.GetState(), r.GetInterrupted())
		}
	}
}

// TestQuitBeforeSavingKeepsItsLine: a box Portenv quit before saving keeps
// "Portenv quit before saving" until it is opened and saved.
func TestQuitBeforeSavingKeepsItsLine(t *testing.T) {
	s := quietServer(t)
	c := boxWithState(t, s, `{"snapshot":"abc","lease":"held","saved_at":"2026-10-09T05:56:40Z"}`)
	if err := s.writeQuitMarker(c.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetBoxState(context.Background(), &daemonv1.GetBoxStateRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetState() != daemonv1.SaveState_SAVE_STATE_QUIT_UNSAVED || !r.GetInterrupted() {
		t.Fatalf("state %v interrupted %v, want QUIT_UNSAVED and interrupted", r.GetState(), r.GetInterrupted())
	}
}
