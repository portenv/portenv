// SPDX-License-Identifier: Apache-2.0

package boundary

import (
	"path/filepath"
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
