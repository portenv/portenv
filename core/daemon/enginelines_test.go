// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"os"
	"strings"
	"testing"
)

// TestEngineLinesMatchTheApps: portenvd's engine lines are the ones in
// testdata/engine-lines.txt, which the app's test also reads and runs
// through DaemonErrorText unchanged (R-0214). Each ends with the window's
// own button: "click Try Again".
func TestEngineLinesMatchTheApps(t *testing.T) {
	b, err := os.ReadFile("testdata/engine-lines.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	got := []string{EngineNotRunning, EngineUnresponsive, PullStalled}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("engine lines\n got %q\nwant %q", got, want)
	}
	for _, l := range got {
		if !strings.HasSuffix(l, "then click Try Again.") || strings.Contains(l, "'") {
			t.Errorf("%q: should end \"then click Try Again.\" with typographic apostrophes", l)
		}
	}
}
