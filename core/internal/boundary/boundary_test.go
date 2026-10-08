// SPDX-License-Identifier: Apache-2.0

package boundary

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCoreRespectsDriverBoundary is the real check: it runs over the whole
// core module.
func TestCoreRespectsDriverBoundary(t *testing.T) {
	root := filepath.Join("..", "..")
	violations, err := Check(root, "driver")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

func TestCheckFindsViolations(t *testing.T) {
	violations, err := Check(filepath.Join("testdata", "bad"))
	if err != nil {
		t.Fatal(err)
	}
	// One SDK import plus four engine CLI calls in testdata/bad/bad.go.
	if len(violations) != 5 {
		for _, v := range violations {
			t.Log(v)
		}
		t.Fatalf("got %d violations, want 5", len(violations))
	}
}

func TestCheckAllowsCleanCode(t *testing.T) {
	violations, err := Check(filepath.Join("testdata", "good"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

func TestCheckSkipsAllowedDirs(t *testing.T) {
	violations, err := Check("testdata", "bad", "good")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestDaemonStartsProcessesOnlyThroughTheBoundedRunner is the real check:
// on the daemon side (sync, local, daemon, the drivers and the commands),
// restic, ssh, docker or anything else is started only through
// core/internal/bounded, which gives every process a deadline (ADR 0012).
// The box agent is not covered: it starts the user's shells and runs restic
// under each call's deadline.
func TestDaemonStartsProcessesOnlyThroughTheBoundedRunner(t *testing.T) {
	root := filepath.Join("..", "..")
	violations, err := CheckProcesses(root, DaemonSide, "internal/bounded")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

func TestCheckProcessesFindsEveryWay(t *testing.T) {
	violations, err := CheckProcesses(filepath.Join("testdata", "procs"), []string{"bad", "good"})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 6 {
		for _, v := range violations {
			t.Log(v)
		}
		t.Fatalf("got %d violations, want 6 (all in bad, none in good)", len(violations))
	}
	for _, v := range violations {
		if !strings.Contains(v.Pos.Filename, "bad") {
			t.Errorf("violation outside bad: %v", v)
		}
	}
}
