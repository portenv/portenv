// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckSwitchAllowsOnlyWorkLanesAndResticsUser(t *testing.T) {
	for _, c := range []struct {
		uid int
		p   Purpose
		ok  bool
	}{
		{WorkUID, ForUser, true},
		{LaneUIDMin, ForUser, true},
		{LaneUIDMax, ForUser, true},
		{0, ForUser, false},
		{0, ForRestic, false},
		{syncUID, ForUser, false},
		{ServeUID, ForUser, false},
		{LaneUIDMax + 1, ForUser, false},
		{999, ForUser, false},
		{syncUID, ForRestic, true},
		{WorkUID, ForRestic, false},
	} {
		if err := CheckSwitch(c.uid, c.p); (err == nil) != c.ok {
			t.Errorf("CheckSwitch(%d, %d) = %v, want allowed=%v", c.uid, c.p, err, c.ok)
		}
	}
}

// TestNoAmbientCapabilities: nothing in the core ever sets ambient
// capabilities, which would carry capabilities into processes started for
// users.
func TestNoAmbientCapabilities(t *testing.T) {
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "creds_test.go") {
			return err
		}
		src, err := os.ReadFile(path) // #nosec G304 -- the repository's own sources
		if err != nil {
			return err
		}
		for _, banned := range []string{"PR_CAP_AMBIENT", "AmbientCaps"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s uses %s", path, banned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
