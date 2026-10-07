// SPDX-License-Identifier: Apache-2.0

// Package sync saves and restores a box's home with restic, applies the
// resume rules, holds the tag-based lease and enforces the save and resume
// invariants in docs/PLAN.md. Design notes: docs/adr/0004-sync-engine.md.
//
// Importers usually alias it (boxsync) to avoid the standard library's sync.
package sync
