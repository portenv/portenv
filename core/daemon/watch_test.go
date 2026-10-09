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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// serving runs a daemon on a short temporary directory and returns a
// client on its socket.
func serving(t *testing.T) (*Server, daemonv1.DaemonServiceClient) {
	t.Helper()
	return servingWith(t, nil)
}

// servingWith lets the test adjust the server before it serves.
func servingWith(t *testing.T, adjust func(*Server)) (*Server, daemonv1.DaemonServiceClient) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pw") // short: a Unix socket path has 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := New(&local.Env{Dir: dir, Machine: "mac-1"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if adjust != nil {
		adjust(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Serve(ctx) }()
	sock := filepath.Join(dir, SocketName)
	for range 100 {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return s, daemonv1.NewDaemonServiceClient(conn)
}

// TestWatchPushesChanges: the state comes at once, then whenever it
// changes, with no poll by the app; nothing is sent while nothing changes,
// apart from a heartbeat (shortened here) so a dead stream is noticed.
func TestWatchPushesChanges(t *testing.T) {
	t.Setenv("PORTENV_KEYS", "file")
	s, c := serving(t)
	watchHeartbeat = 600 * time.Millisecond
	t.Cleanup(func() { watchHeartbeat = 30 * time.Second })
	boxWithState(t, s, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := c.WatchBoxState(ctx, &daemonv1.WatchBoxStateRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	firstM, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	first := firstM.GetState()
	if first.GetState() != daemonv1.SaveState_SAVE_STATE_CLOSED {
		t.Fatalf("first message %v, want CLOSED", first.GetState())
	}
	// The box becomes interrupted (portenvd's previous run left it open).
	start := time.Now()
	if err := os.MkdirAll(filepath.Join(s.env.Dir, "state", "box-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.env.Dir, "state", "box-1", "state.json"), []byte(`{"snapshot":"abc","lease":"held","saved_at":"2026-10-09T05:56:40Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nextM, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	next := nextM.GetState()
	if !next.GetInterrupted() || next.GetState() != daemonv1.SaveState_SAVE_STATE_NOT_SAVED {
		t.Fatalf("after the change: %v", next)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the change took %v to arrive, want well under a second", d)
	}
	// Nothing changes: the next message is the heartbeat, not sooner.
	start = time.Now()
	beatM, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	beat := beatM.GetState()
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("a message %v after the last with no change", d)
	}
	if !beat.GetInterrupted() {
		t.Fatalf("the heartbeat repeats the state: %v", beat)
	}
}

// TestWatchEndsWithTheDaemon: when portenvd stops, the stream ends at once,
// so the app notices without polling (item 1g).
func TestWatchEndsWithTheDaemon(t *testing.T) {
	s, c := serving(t)
	boxWithState(t, s, "")
	stream, err := c.WatchBoxState(context.Background(), &daemonv1.WatchBoxStateRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Relaunch(context.Background(), &daemonv1.RelaunchRequest{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream outlived portenvd")
	}
}
