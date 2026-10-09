// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

func quietServer(t *testing.T) *Server {
	t.Helper()
	return New(&local.Env{Dir: t.TempDir(), Machine: "mac-1"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestTheQuitMarker: written when Portenv quits before a box is saved, found
// on the next open, removed after a successful save.
func TestTheQuitMarker(t *testing.T) {
	s := quietServer(t)
	if s.hasQuitMarker("box-1") {
		t.Fatal("a new box has a quit marker")
	}
	if err := s.writeQuitMarker("box-1"); err != nil {
		t.Fatal(err)
	}
	if !s.hasQuitMarker("box-1") || s.hasQuitMarker("box-2") {
		t.Fatal("the marker belongs to box-1 only")
	}
	s.clearQuitMarker("box-1")
	s.clearQuitMarker("box-1") // clearing twice is fine
	if s.hasQuitMarker("box-1") {
		t.Fatal("the marker is still there after a save")
	}
}

// TestShutdownWaitsForBoxesStillOpening: closeAll waits for an open in
// progress (so that box is closed too, not left half open), and once
// shutdown has begun no new open starts.
func TestShutdownWaitsForBoxesStillOpening(t *testing.T) {
	s := quietServer(t)
	done, err := s.startOpening()
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { s.closeAll(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("closeAll finished while a box was still opening")
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := s.startOpening(); status.Code(err) != codes.Unavailable {
		t.Fatalf("an open during shutdown: %v, want Unavailable", err)
	}
	done()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closeAll did not finish after the open ended")
	}
}

// TestOneDaemonAtATime: a second portenvd (or runner) on the same Portenv
// directory refuses to start while the first holds it, so it can never
// re-key a box the first one still serves (ADR 0014). Once the first has
// stopped, a new one starts.
func TestOneDaemonAtATime(t *testing.T) {
	dir := t.TempDir()
	first, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockDir(dir); err == nil || err.Error() != "another portenvd is already running for "+dir {
		t.Fatalf("a second daemon: %v", err)
	}
	first.release()
	again, err := lockDir(dir)
	if err != nil {
		t.Fatalf("after the first stopped: %v", err)
	}
	again.release()
}

// TestRelaunchLeavesBoxesRunning: when Portenv relaunches for an update,
// portenvd stops serving without closing its boxes (closeAll never runs),
// so the next portenvd takes them over (ADR 0014). No new open starts
// meanwhile.
func TestRelaunchLeavesBoxesRunning(t *testing.T) {
	dir, err := os.MkdirTemp("", "pd") // short: a Unix socket path has 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := New(&local.Env{Dir: dir, Machine: "mac-1"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background()) }()
	for range 50 {
		if _, err := os.Stat(filepath.Join(dir, SocketName)); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := s.Relaunch(context.Background(), &daemonv1.RelaunchRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.startOpening(); status.Code(err) != codes.Unavailable {
		t.Fatalf("an open while relaunching: %v, want Unavailable", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("portenvd kept serving after Relaunch")
	}
	s.mu.Lock()
	closed := s.stopping
	s.mu.Unlock()
	if closed {
		t.Fatal("relaunching closed the boxes (closeAll ran)")
	}
}

// TestLeaveUnsaved: Quit Anyway, with portenvd run by launchd (1.1). The
// box couldn't be saved; portenvd writes the quit marker and lets go of
// the box without saving it, as its own shutdown did. The next open takes
// the running box over and saves it first thing.
func TestLeaveUnsaved(t *testing.T) {
	s := quietServer(t)
	s.open["acme-api"] = &openBox{sess: &local.Session{Cfg: local.BoxConfig{ID: "box-1", Name: "acme-api"}}}
	if _, err := s.LeaveUnsaved(context.Background(), &daemonv1.LeaveUnsavedRequest{Name: "acme-api"}); err != nil {
		t.Fatal(err)
	}
	if !s.hasQuitMarker("box-1") {
		t.Fatal("no quit marker: the next open wouldn't save first thing")
	}
	if _, err := s.get("acme-api"); err == nil {
		t.Fatal("portenvd still holds the box open")
	}
	if _, err := s.LeaveUnsaved(context.Background(), &daemonv1.LeaveUnsavedRequest{Name: "acme-api"}); err != nil {
		t.Fatalf("a box that isn't open: %v, want nothing to do", err)
	}
}
