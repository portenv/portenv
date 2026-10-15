// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// engineAt returns a driver whose client talks to a Unix socket in a
// temporary directory: served by handler, or nothing at all when nil.
func engineAt(t *testing.T, handler http.Handler) *Driver {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pd") // short: a socket path is at most 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "docker.sock")
	if handler != nil {
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
		go func() { _ = srv.Serve(l) }()
		t.Cleanup(func() { _ = srv.Close() })
	}
	cli, err := client.New(client.WithHost("unix://" + sock))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{StateDir: t.TempDir(), Client: cli})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestPingSaysWhetherTheEngineAnswers (R-0098): Welcome's Docker card asks
// portenvd, which asks the engine; no engine, no answer.
func TestPingSaysWhetherTheEngineAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	running := engineAt(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.51")
		_, _ = w.Write([]byte("OK"))
	}))
	if err := running.Ping(ctx); err != nil {
		t.Fatalf("an engine that answers: %v", err)
	}

	if err := engineAt(t, nil).Ping(ctx); err == nil {
		t.Fatal("no engine on the socket, but Ping succeeded")
	}

	failing := engineAt(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "starting", http.StatusInternalServerError)
	}))
	if err := failing.Ping(ctx); err == nil {
		t.Fatal("an engine that answers with an error counted as running")
	}
}

// TestPingGivesUpAtTheDeadline: an engine that accepts but never answers
// (Docker Desktop starting) is not running, and the check doesn't hang.
func TestPingGivesUpAtTheDeadline(t *testing.T) {
	stuck := engineAt(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := stuck.Ping(ctx); err == nil {
		t.Fatal("an engine that never answered counted as running")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Ping took %v past a 300 ms deadline", d)
	}
}
