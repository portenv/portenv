// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// An orphan that exits while init runs one of its own commands (the lock
// held) must still be reaped once the command ends, without waiting for
// another SIGCHLD: CI's "orphans are reaped" failure on #162.
func TestAnOrphanExitingDuringACommandIsReapedLater(t *testing.T) {
	var mu sync.Mutex
	var reaped atomic.Int32
	r := newChildReaper(&mu, func() { reaped.Add(1) }, 20*time.Millisecond)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-r.retry:
				r.child()
			case <-stop:
				return
			}
		}
	}()

	mu.Lock() // init is running a command
	r.child() // the one SIGCHLD arrives now, and can't reap
	if reaped.Load() != 0 {
		t.Fatal("reaped while the lock was held")
	}
	time.Sleep(100 * time.Millisecond)
	mu.Unlock() // the command ends; no other SIGCHLD comes

	deadline := time.Now().Add(2 * time.Second)
	for reaped.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if reaped.Load() == 0 {
		t.Fatal("the orphan was never reaped")
	}
}

func TestAFreeLockReapsAtOnce(t *testing.T) {
	var mu sync.Mutex
	var reaped atomic.Int32
	r := newChildReaper(&mu, func() { reaped.Add(1) }, time.Hour)
	r.child()
	if reaped.Load() != 1 {
		t.Errorf("reaped %d times, want 1", reaped.Load())
	}
	select {
	case <-r.retry:
		t.Error("a retry was armed though the reap happened")
	default:
	}
}

// Many skipped signals arm one retry, not one each.
func TestSkippedSignalsArmOneRetry(t *testing.T) {
	var mu sync.Mutex
	r := newChildReaper(&mu, func() {}, 20*time.Millisecond)
	mu.Lock()
	for range 50 {
		r.child()
	}
	if n := r.arms.Load(); n != 1 {
		t.Errorf("%d retries armed for 50 skipped signals, want 1", n)
	}
	mu.Unlock()
}
