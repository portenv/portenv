// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"time"

	"google.golang.org/protobuf/proto"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// How the watcher samples a box's state: everything this daemon records
// every 250 ms; the agent's package report, and boxes on a server (over
// SSH), every 5 s. The heartbeat repeats the state when nothing changed.
var (
	watchFast      = 250 * time.Millisecond
	watchSlow      = 5 * time.Second
	watchHeartbeat = 30 * time.Second
)

// WatchBoxState implements daemonv1.DaemonServiceServer: the box's state
// at once, then whenever it changes, and a heartbeat. It ends when the
// caller goes or portenvd stops.
func (s *Server) WatchBoxState(req *daemonv1.WatchBoxStateRequest, stream daemonv1.DaemonService_WatchBoxStateServer) error {
	ctx := stream.Context()
	name := req.GetName()
	var last *daemonv1.GetBoxStateResponse
	var lastSent, lastSlow time.Time
	var packages []string
	var packagesErr string
	tick := time.NewTicker(watchFast)
	defer tick.Stop()
	for {
		slow := last == nil || time.Since(lastSlow) >= watchSlow
		if slow || s.location(name) == "" {
			if st, err := s.boxState(ctx, name, slow); err == nil {
				if slow {
					lastSlow = time.Now()
					packages, packagesErr = st.GetFailedPackages(), st.GetPackagesError()
				} else {
					st.FailedPackages, st.PackagesError = packages, packagesErr
				}
				if last == nil || !proto.Equal(st, last) || time.Since(lastSent) >= watchHeartbeat {
					if err := stream.Send(&daemonv1.WatchBoxStateResponse{State: st}); err != nil {
						return err
					}
					last, lastSent = st, time.Now()
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
