// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"syscall"
	"testing"
	"time"
)

// TestPerformanceBudgets checks the Phase 0 budgets against a local
// repository: a 5 MB change saves in under 10 s, and resuming on the same
// machine takes under 5 s. Remote storage is measured in the e2e harness.
//
// It doesn't run in parallel: when it did, the package's other tests (39
// worlds, each running restic) took the cores during the measured save,
// about 5.3 s of CPU time in a 1.5 s save that needs about 0.9 s itself, so
// on a machine with fewer cores or other load the budget measured them too.
// The log line gives the CPU time this process and restic used next to the
// wall-clock time, so a failure shows whether the save did more work or
// waited for a core.
func TestPerformanceBudgets(t *testing.T) {
	if testing.Short() {
		t.Skip("performance budget")
	}
	w := newSerialWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	fill(t, a.home, 500, 8<<10) // an existing 4 MB home
	mustSave(t, a, SaveRelease)

	mustResume(t, a, ResumeOptions{})
	fill(t, a.home+"/change", 80, 64<<10) // a 5 MB change
	start, cpu := time.Now(), cpuTime()
	mustSave(t, a, SaveRelease)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("5 MB change saved in %v (CPU time %v), budget 10s", d, cpuTime()-cpu)
	} else {
		t.Logf("5 MB change saved in %v (CPU time %v)", d, cpuTime()-cpu)
	}

	start = time.Now()
	res := mustResume(t, a, ResumeOptions{})
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("same-machine resume took %v, budget 5s", d)
	} else {
		t.Logf("same-machine resume (rule %d) took %v", res.Rule, d)
	}
}

// cpuTime is the CPU time this process and its finished children (restic)
// have used.
func cpuTime() time.Duration {
	var self, children syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children)
	return time.Duration(self.Utime.Nano() + self.Stime.Nano() + children.Utime.Nano() + children.Stime.Nano())
}
