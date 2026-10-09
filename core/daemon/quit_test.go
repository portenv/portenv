// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// fakeClock is the background save's clock: sleeping only records the
// wait and moves time on.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	waits  []time.Duration
	notify chan struct{}
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) bool {
	c.mu.Lock()
	c.now += d
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return ctx.Err() == nil
}

func (c *fakeClock) elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// leftBox is an open box as Quit Anyway finds it.
func leftBox(s *Server) {
	s.open["acme-api"] = &openBox{sess: &local.Session{Cfg: local.BoxConfig{ID: "box-1", Name: "acme-api"}}}
}

// TestQuitAnywayKeepsRetrying: after Quit Anyway, portenvd retries the
// save in the background (30 s, 1, 2 and 5 minutes, then every 5) and
// never stops while the box holds unsaved work: still retrying after 30
// minutes. Until a save succeeds the box stays open here, so the lease
// stays with this Mac (a failed close never releases it).
func TestQuitAnywayKeepsRetrying(t *testing.T) {
	s := quietServer(t)
	clock := &fakeClock{notify: make(chan struct{}, 1)}
	s.sleep = clock.sleep
	var attempts atomic.Int32
	s.closeForQuit = func(context.Context, string) error {
		attempts.Add(1)
		return errors.New("storage unreachable")
	}
	leftBox(s)
	if _, err := s.LeaveUnsaved(context.Background(), &daemonv1.LeaveUnsavedRequest{Name: "acme-api"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for clock.elapsed() < 35*time.Minute && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if clock.elapsed() < 35*time.Minute {
		t.Fatalf("stopped retrying after %v of fake time", clock.elapsed())
	}
	clock.mu.Lock()
	waits := append([]time.Duration(nil), clock.waits...)
	clock.mu.Unlock()
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if waits[i] != w {
			t.Fatalf("waits %v, want them to start %v", waits[:len(want)], want)
		}
	}
	if attempts.Load() < 8 {
		t.Fatalf("%d attempts in 35 minutes", attempts.Load())
	}
	if _, err := s.get("acme-api"); err != nil {
		t.Fatal("the box was let go of while unsaved: the lease would have no holder here")
	}
	if !s.hasQuitMarker("box-1") {
		t.Fatal("no quit marker while unsaved")
	}
	s.stopBackground()
}

// TestQuitAnywaySavedInTheBackground: once a background save succeeds the
// box is closed (saved, released, stopped), the marker is cleared, and a
// record is kept for the next launch's one notification.
func TestQuitAnywaySavedInTheBackground(t *testing.T) {
	s := quietServer(t)
	clock := &fakeClock{notify: make(chan struct{}, 1)}
	s.sleep = clock.sleep
	var attempts atomic.Int32
	s.closeForQuit = func(_ context.Context, name string) error {
		if attempts.Add(1) < 3 {
			return errors.New("storage unreachable")
		}
		s.mu.Lock()
		delete(s.open, name)
		s.mu.Unlock()
		return nil
	}
	leftBox(s)
	if _, err := s.LeaveUnsaved(context.Background(), &daemonv1.LeaveUnsavedRequest{Name: "acme-api"}); err != nil {
		t.Fatal(err)
	}
	var saved []*daemonv1.SavedAfterQuit
	for range 500 {
		r, err := s.TakeSavedAfterQuit(context.Background(), &daemonv1.TakeSavedAfterQuitRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if saved = r.GetSaved(); len(saved) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(saved) != 1 || saved[0].GetName() != "acme-api" || saved[0].GetSavedAt() == nil {
		t.Fatalf("saved after quit: %v", saved)
	}
	if s.hasQuitMarker("box-1") {
		t.Fatal("the marker is still there after the save")
	}
	if attempts.Load() != 3 {
		t.Fatalf("%d attempts, want 3 (it stops once saved)", attempts.Load())
	}
	// One notification: taken once, then gone.
	r, _ := s.TakeSavedAfterQuit(context.Background(), &daemonv1.TakeSavedAfterQuitRequest{})
	if len(r.GetSaved()) != 0 {
		t.Fatalf("the record was given out twice: %v", r.GetSaved())
	}
}

// TestOpeningStopsTheBackgroundSave: the app opens the box before a
// background save succeeded: it stays open as it is and saves first thing
// ("Portenv quit before saving"); the background loop stops.
func TestOpeningStopsTheBackgroundSave(t *testing.T) {
	s := quietServer(t)
	clock := &fakeClock{notify: make(chan struct{}, 1)}
	gate := make(chan struct{})
	s.sleep = func(ctx context.Context, d time.Duration) bool {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return clock.sleep(ctx, d)
	}
	var attempts atomic.Int32
	s.closeForQuit = func(context.Context, string) error { attempts.Add(1); return errors.New("unreachable") }
	leftBox(s)
	if _, err := s.LeaveUnsaved(context.Background(), &daemonv1.LeaveUnsavedRequest{Name: "acme-api"}); err != nil {
		t.Fatal(err)
	}
	ob, _ := s.get("acme-api")
	if !s.takeBackBox(ob) {
		t.Fatal("the app couldn't take the box back from the background save")
	}
	close(gate)
	time.Sleep(100 * time.Millisecond)
	if attempts.Load() != 0 {
		t.Fatalf("the background save ran %d times after the app took the box back", attempts.Load())
	}
	if !ob.quitUnsaved.Load() {
		t.Fatal("the line must still say Portenv quit before saving, until the save")
	}
}
