// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"strings"
	"time"
)

// Snapshot tags.
const (
	tagRelease     = "release"
	tagPoint       = "point"
	tagOrphaned    = "orphaned"
	tagMachinePfx  = "machine:"
	tagActivePfx   = "active:"
	staleAfter     = 10 * time.Minute
	snapshotIDSize = 8
)

// SnapshotKind says why a snapshot was made.
type SnapshotKind int

// Snapshot kinds. An untagged snapshot is an autosave.
const (
	KindAutosave SnapshotKind = iota
	KindPoint
	KindRelease
	KindOrphaned
)

func (k SnapshotKind) String() string {
	switch k {
	case KindPoint:
		return "point"
	case KindRelease:
		return "release"
	case KindOrphaned:
		return "orphaned"
	default:
		return "autosave"
	}
}

// Snapshot is one restic snapshot of a box's home.
type Snapshot struct {
	ID       string    `json:"id"`
	Original string    `json:"original,omitempty"` // set when tags were rewritten
	Tree     string    `json:"tree"`               // content hash: equal trees, equal homes
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`

	Kind    SnapshotKind `json:"-"`
	Machine string       `json:"-"` // from machine:<id>
	Active  string       `json:"-"` // from active:<id>; empty when no lease
}

func (s *Snapshot) parseTags() {
	s.Kind, s.Machine, s.Active = KindAutosave, "", ""
	for _, t := range s.Tags {
		switch {
		case t == tagRelease:
			s.Kind = KindRelease
		case t == tagPoint:
			s.Kind = KindPoint
		case t == tagOrphaned:
			s.Kind = KindOrphaned
		case strings.HasPrefix(t, tagMachinePfx):
			s.Machine = strings.TrimPrefix(t, tagMachinePfx)
		case strings.HasPrefix(t, tagActivePfx):
			s.Active = strings.TrimPrefix(t, tagActivePfx)
		}
	}
}

// origin is the snapshot's original ID, stable across tag rewrites.
func (s Snapshot) origin() string {
	if s.Original != "" {
		return s.Original
	}
	return s.ID
}

func (s Snapshot) short() string {
	if len(s.ID) > snapshotIDSize {
		return s.ID[:snapshotIDSize]
	}
	return s.ID
}

// LeaseSource says where a lease record comes from.
type LeaseSource int

// Lease sources. Before Phase 3 every lease is a snapshot tag.
const (
	LeaseFromSnapshotTag LeaseSource = iota
	LeaseFromControlPlane
)

// Lease is one machine's claim to hold a box open.
type Lease struct {
	Machine string
	// LastSeen is the time of the snapshot carrying the lease: the last time
	// the holder is known to have been working.
	LastSeen time.Time
	// Stale is true when LastSeen is more than 10 minutes ago. A stale lease
	// is only reported; the user decides whether to take over.
	Stale  bool
	Source LeaseSource
}
