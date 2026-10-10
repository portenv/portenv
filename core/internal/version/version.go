// SPDX-License-Identifier: Apache-2.0

// Package version holds build information, set at link time with -ldflags.
package version

import "github.com/portenv/portenv/core/internal/instance"

// Set by the Makefile with -X; local builds without it report "dev".
var (
	Version = "dev"
	Commit  = "unknown"
)

// String returns "<version> (<commit>)", plus " [dev-X]" for a dev lane's
// build (gmake … LANE=X), so a binary's version says which instance it is.
func String() string {
	s := Version + " (" + Commit + ")"
	if st := instance.Stamp(); st != "" {
		s += " [" + st + "]"
	}
	return s
}
