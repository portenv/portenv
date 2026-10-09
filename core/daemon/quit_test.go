// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/local"
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
