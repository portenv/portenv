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
