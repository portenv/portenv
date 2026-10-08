// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestOnlyAFreshDownSkipsTheProbe: a "no network" report counts only from an
// app that still runs and for 30 s; anything else means probe the storage.
func TestOnlyAFreshDownSkipsTheProbe(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 17, 0, 0, 0, time.UTC)
	running := func(int) bool { return true }
	gone := func(int) bool { return false }
	down := networkReport{path: daemonv1.NetworkPath_NETWORK_PATH_UNSATISFIED, pid: 42, at: t0}
	up := networkReport{path: daemonv1.NetworkPath_NETWORK_PATH_SATISFIED, pid: 42, at: t0}
	for _, c := range []struct {
		name  string
		r     networkReport
		now   time.Time
		alive func(int) bool
		skip  bool
	}{
		{"no report", networkReport{}, t0, running, false},
		{"up", up, t0, running, false},
		{"fresh down", down, t0.Add(29 * time.Second), running, true},
		{"down at 30 s", down, t0.Add(30 * time.Second), running, true},
		{"stale down", down, t0.Add(31 * time.Second), running, false},
		{"down from an app that quit", down, t0.Add(time.Second), gone, false},
		{"down without a sender", networkReport{path: down.path, at: t0}, t0, running, false},
		{"down from the future (clock change)", down, t0.Add(-time.Second), running, false},
	} {
		if got := noNetwork(c.r, c.now, c.alive); got != c.skip {
			t.Errorf("%s: skip probe %v, want %v", c.name, got, c.skip)
		}
	}
}

// TestDownExpiresThroughTheServer: report down through the API, then let it
// go stale, or have its sender quit: the next open probes.
func TestDownExpiresThroughTheServer(t *testing.T) {
	now := time.Date(2026, 10, 8, 17, 0, 0, 0, time.UTC)
	s := New(nil, nil)
	s.now = func() time.Time { return now }
	report := func(pid int) {
		_, err := s.SetNetworkPath(context.Background(), &daemonv1.SetNetworkPathRequest{
			Path: daemonv1.NetworkPath_NETWORK_PATH_UNSATISFIED, ReporterPid: int32(pid), // #nosec G115 -- a process ID
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	report(os.Getpid())
	if !s.skipProbe() {
		t.Fatal("a fresh down from a running process should skip the probe")
	}
	now = now.Add(31 * time.Second)
	if s.skipProbe() {
		t.Fatal("a down older than 30 s must not skip the probe")
	}

	// A sender that has quit.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	report(cmd.Process.Pid)
	if s.skipProbe() {
		t.Fatal("a down from a process that has exited must not skip the probe")
	}

	report(os.Getpid())
	_, _ = s.SetNetworkPath(context.Background(), &daemonv1.SetNetworkPathRequest{Path: daemonv1.NetworkPath_NETWORK_PATH_SATISFIED, ReporterPid: int32(os.Getpid())}) // #nosec G115 -- a process ID
	if s.skipProbe() {
		t.Fatal("up must probe")
	}
}
