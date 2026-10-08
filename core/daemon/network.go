// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"syscall"
	"time"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// downTrust is how long a "no network" report counts after it was sent.
// The app sends it again every 10 s while it lasts.
const downTrust = 30 * time.Second

// networkReport is the app's last report of the system's network status.
type networkReport struct {
	path daemonv1.NetworkPath
	pid  int // the app that sent it
	at   time.Time
}

// noNetwork reports whether a fresh "no network at all" report lets an open
// skip the storage probe: sent by an app that still runs, at most downTrust
// ago. A missing, stale or orphaned report, or "up", means probe.
func noNetwork(r networkReport, now time.Time, alive func(pid int) bool) bool {
	if r.path != daemonv1.NetworkPath_NETWORK_PATH_UNSATISFIED || r.pid <= 0 {
		return false
	}
	age := now.Sub(r.at)
	return age >= 0 && age <= downTrust && alive(r.pid)
}

// processAlive reports whether pid is a running process.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// SetNetworkPath records the app's report of the system's network status.
func (s *Server) SetNetworkPath(_ context.Context, req *daemonv1.SetNetworkPathRequest) (*daemonv1.SetNetworkPathResponse, error) {
	s.mu.Lock()
	s.network = networkReport{path: req.GetPath(), pid: int(req.GetReporterPid()), at: s.now()}
	s.mu.Unlock()
	return &daemonv1.SetNetworkPathResponse{}, nil
}

// skipProbe is noNetwork for the current report.
func (s *Server) skipProbe() bool {
	s.mu.Lock()
	r := s.network
	s.mu.Unlock()
	return noNetwork(r, s.now(), s.alive)
}
