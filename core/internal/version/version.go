// SPDX-License-Identifier: Apache-2.0

// Package version holds build information, set at link time with -ldflags.
package version

// Set by the Makefile with -X; local builds without it report "dev".
var (
	Version = "dev"
	Commit  = "unknown"
)

// String returns "<version> (<commit>)".
func String() string {
	return Version + " (" + Commit + ")"
}
