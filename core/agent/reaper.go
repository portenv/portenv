// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"sync"
	"sync/atomic"
	"time"
)

// childReaper is init's response to SIGCHLD. Init's own commands hold mu
// while they run, so the reaper never takes an exit status a command is
// waiting for; a SIGCHLD that arrives then is skipped. It used to wait for
// the next SIGCHLD, which may never come, leaving a zombie. Now a skip
// arms one retry, which reaps once the command lets go.
type childReaper struct {
	mu    *sync.Mutex
	reap  func()
	delay time.Duration
	retry chan struct{} // the signal loop calls child() on each
	armed atomic.Bool
	arms  atomic.Int32 // retries armed, for tests
}

func newChildReaper(mu *sync.Mutex, reap func(), delay time.Duration) *childReaper {
	return &childReaper{mu: mu, reap: reap, delay: delay, retry: make(chan struct{}, 1)}
}

// child reaps now if no command holds the lock, and otherwise arms a
// single retry. It never blocks the signal loop.
func (r *childReaper) child() {
	if r.mu.TryLock() {
		r.reap()
		r.mu.Unlock()
		return
	}
	if r.armed.CompareAndSwap(false, true) {
		r.arms.Add(1)
		time.AfterFunc(r.delay, func() {
			r.armed.Store(false)
			select {
			case r.retry <- struct{}{}:
			default:
			}
		})
	}
}
