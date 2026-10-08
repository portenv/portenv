// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunRemoteNeverForwardsStdin reproduces the gate's hang: the remote
// command reads stdin, and the caller's stdin is an open pipe that never
// closes. Forwarding it would block until the timeout.
func TestRunRemoteNeverForwardsStdin(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "fake-ssh")
	// Stands in for ssh: drop "--" and the host, run the rest locally.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nshift 2\nexec \"$@\"\n"), 0o700); err != nil { // #nosec G306 -- test helper
		t.Fatal(err)
	}
	t.Setenv("PORTENV_SSH", fake)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	old := os.Stdin
	os.Stdin = r // open, never closed while the command runs
	defer func() { os.Stdin = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := RunRemote(ctx, "host", os.Stdout, "cat"); err != nil {
		t.Fatalf("runRemote: %v (after %v)", err, time.Since(start))
	}
}
