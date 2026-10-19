// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"slices"
	"testing"
)

func TestACLRoundTripsInKernelOrder(t *testing.T) {
	es := []aclEntry{{aclOther, 4, aclUndefinedID}, {aclGroup, 6, 2001}, {aclMask, 6, aclUndefinedID}, {aclGroup, 7, TeamGID}, {aclUserObj, 6, aclUndefinedID}, {aclGroupObj, 4, aclUndefinedID}}
	got, err := decodeACL(encodeACL(es))
	if err != nil {
		t.Fatal(err)
	}
	want := []aclEntry{{aclUserObj, 6, aclUndefinedID}, {aclGroupObj, 4, aclUndefinedID}, {aclGroup, 7, TeamGID}, {aclGroup, 6, 2001}, {aclMask, 6, aclUndefinedID}, {aclOther, 4, aclUndefinedID}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range [][]byte{nil, {2, 0, 0}, {1, 0, 0, 0}, {2, 0, 0, 0, 1}} {
		if _, err := decodeACL(bad); err == nil {
			t.Errorf("decoded %v", bad)
		}
	}
}

func TestACLWithGroupIsSetfaclM(t *testing.T) {
	// setfacl -m g:team:rw on a 0640 file: the entry, and a mask that
	// makes it effective.
	got := aclWithGroup(minimalACL(0o640), TeamGID, 6)
	want := []aclEntry{{aclUserObj, 6, aclUndefinedID}, {aclGroupObj, 4, aclUndefinedID}, {aclOther, 0, aclUndefinedID}, {aclGroup, 6, TeamGID}, {aclMask, 6, aclUndefinedID}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Again, with another named entry and an old mask: team's entry is
	// replaced, the other kept, the mask the union.
	got = aclWithGroup([]aclEntry{{aclUserObj, 7, aclUndefinedID}, {aclGroupObj, 0, aclUndefinedID}, {aclUser, 1, 1500}, {aclGroup, 4, TeamGID}, {aclMask, 4, aclUndefinedID}, {aclOther, 0, aclUndefinedID}}, TeamGID, 6)
	if p, _ := aclPerm(got, aclMask); p != 7 {
		t.Errorf("mask %o, want 7 (team rw, user 1500 x)", p)
	}
	n := 0
	for _, e := range got {
		if e.tag == aclGroup && e.id == TeamGID {
			n++
			if e.perm != 6 {
				t.Errorf("team's entry %o, want 6", e.perm)
			}
		}
	}
	if n != 1 || !slices.Contains(got, aclEntry{aclUser, 1, 1500}) {
		t.Errorf("got %v", got)
	}
	if b := aclBase(got); len(b) != 3 {
		t.Errorf("base %v", b)
	}
}

// R-0315: who could write a file of work's, from its ACL.
func TestACLLetsOthersWrite(t *testing.T) {
	const work, own = 1000, 1000
	cases := []struct {
		name string
		es   []aclEntry
		gid  uint32
		want bool
	}{
		{"0644", minimalACL(0o644), own, false},
		{"0664 in work's own group", minimalACL(0o664), own, false},
		{"0664 in group team", minimalACL(0o664), TeamGID, true},
		{"0646", minimalACL(0o646), own, true},
		{"team read", aclWithGroup(minimalACL(0o644), TeamGID, 5), own, false},
		{"team write", aclWithGroup(minimalACL(0o644), TeamGID, 7), own, true},
		{"team write under a read mask", append(aclBase(minimalACL(0o644)), aclEntry{aclGroup, 7, TeamGID}, aclEntry{aclMask, 5, aclUndefinedID}), own, false},
		{"a named user's write", append(aclBase(minimalACL(0o644)), aclEntry{aclUser, 7, 2001}, aclEntry{aclMask, 7, aclUndefinedID}), own, true},
		{"work's own named entry", append(aclBase(minimalACL(0o644)), aclEntry{aclUser, 7, work}, aclEntry{aclMask, 7, aclUndefinedID}), own, false},
	}
	for _, c := range cases {
		if got := aclLetsOthersWrite(c.es, work, c.gid, own); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// readOnlyBase takes others' write, and the group's when it isn't
	// work's own.
	if got := readOnlyBase(minimalACL(0o666), TeamGID, own); !slices.Equal(got, minimalACL(0o644)) {
		t.Errorf("readOnlyBase(0666, team) = %v, want 0644's", got)
	}
	if got := readOnlyBase(minimalACL(0o666), own, own); !slices.Equal(got, minimalACL(0o664)) {
		t.Errorf("readOnlyBase(0666, own) = %v, want 0664's", got)
	}
}

// Lenses (ADR 0022 §2a point 4): agent uids' named entries are exactly
// the readers'; others are kept; no named entry left means no mask.
func TestACLWithUsers(t *testing.T) {
	base := minimalACL(0o700)
	got := aclWithUsers(base, map[uint32]uint16{2002: 1}, AgentUIDFirst, AgentUIDLast)
	want := []aclEntry{{aclUserObj, 7, aclUndefinedID}, {aclGroupObj, 0, aclUndefinedID}, {aclOther, 0, aclUndefinedID}, {aclUser, 1, 2002}, {aclMask, 1, aclUndefinedID}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Another reader replaces it; a named entry outside the agents' range
	// stays.
	withOther := append(slices.Clone(got[:4]), aclEntry{aclUser, 4, 1500}, aclEntry{aclMask, 5, aclUndefinedID})
	got = aclWithUsers(withOther, map[uint32]uint16{2003: 5}, AgentUIDFirst, AgentUIDLast)
	if slices.ContainsFunc(got, func(e aclEntry) bool { return e.tag == aclUser && e.id == 2002 }) || !slices.Contains(got, aclEntry{aclUser, 4, 1500}) || !slices.Contains(got, aclEntry{aclUser, 5, 2003}) {
		t.Errorf("got %v", got)
	}
	// No readers: the agents' entries and the mask go.
	got = aclWithUsers(want, nil, AgentUIDFirst, AgentUIDLast)
	if !slices.Equal(got, base) || aclHasNamed(got) {
		t.Errorf("got %v, want the mode's own %v", got, base)
	}
}
