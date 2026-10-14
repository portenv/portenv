// SPDX-License-Identifier: Apache-2.0

// Package version holds build information, set at link time with -ldflags.
package version

import "github.com/portenv/portenv/core/internal/instance"

// Set by the Makefile with -X; local builds without it report "dev".
var (
	Version = "dev"
	Commit  = "unknown"
	// BuildID is one build of the app and its helpers (gmake app's
	// BUILD_ID): the app restarts a portenvd of another build (R-0198).
	BuildID = ""
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
