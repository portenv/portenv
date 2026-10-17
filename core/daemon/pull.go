// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"sync"

	"github.com/portenv/portenv/core/driver"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// pulls are the toolbox image pulls under way, by box name: the first
// open's progress, for the window (R-0211).
type pulls struct {
	mu sync.Mutex
	m  map[string]*daemonv1.ImagePull
}

// followPull makes ctx carry the open's pull progress into the box's
// state; done clears it once the open ends.
func (s *Server) followPull(ctx context.Context, name string) (context.Context, func()) {
	ctx = driver.WithPullProgress(ctx, func(done, total int64) {
		s.pulling.mu.Lock()
		defer s.pulling.mu.Unlock()
		if s.pulling.m == nil {
			s.pulling.m = map[string]*daemonv1.ImagePull{}
		}
		s.pulling.m[name] = &daemonv1.ImagePull{DoneBytes: done, TotalBytes: total}
	})
	return ctx, func() {
		s.pulling.mu.Lock()
		defer s.pulling.mu.Unlock()
		delete(s.pulling.m, name)
	}
}

// pullOf is the pull under way for name, if any.
func (s *Server) pullOf(name string) *daemonv1.ImagePull {
	s.pulling.mu.Lock()
	defer s.pulling.mu.Unlock()
	return s.pulling.m[name]
}
