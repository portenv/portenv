// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// defaultMigrateOps: the move runs only in the workspace.
func defaultMigrateOps(Config) *migrateOps {
	no := errors.New("the move to agents' own copies runs only in the workspace")
	return &migrateOps{
		othersWrite: func(*os.File, *unix.Stat_t, bool) (bool, error) { return false, no },
		reown:       func(*os.File, bool) error { return no },
		reownLink:   func(*os.File, string) error { return no },
		asAgent:     runTeamIOAsAgent,
		running:     func() ([]int, error) { return nil, no },
	}
}
