// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"testing"
	"time"
)

// TestPerformanceBudgets checks the Phase 0 budgets against a local
// repository: a 5 MB change saves in under 10 s, and resuming on the same
// machine takes under 5 s. Remote storage is measured in the e2e harness.
func TestPerformanceBudgets(t *testing.T) {
	if testing.Short() {
		t.Skip("performance budget")
	}
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	fill(t, a.home, 500, 8<<10) // an existing 4 MB home
	mustSave(t, a, SaveRelease)

	mustResume(t, a, ResumeOptions{})
	fill(t, a.home+"/change", 80, 64<<10) // a 5 MB change
	start := time.Now()
	mustSave(t, a, SaveRelease)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("5 MB change saved in %v, budget 10s", d)
	} else {
		t.Logf("5 MB change saved in %v", d)
	}

	start = time.Now()
	res := mustResume(t, a, ResumeOptions{})
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("same-machine resume took %v, budget 5s", d)
	} else {
		t.Logf("same-machine resume (rule %d) took %v", res.Rule, d)
	}
}
