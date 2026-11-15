// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"io/fs"
	"testing"
)

// CC-0706 item 19: a file in /home/team gets the owning group's read and
// write (and search or run, for a folder or a program), work's own named
// entry with the same, and a mask that lets both through. Its other
// entries are kept.
func TestTeamShareAccess(t *testing.T) {
	const work = 1000
	got := teamShareAccess(minimalACL(fs.FileMode(0o600)), teamSharePerm(false, false), work)
	want := []aclEntry{{aclUserObj, 6, aclUndefinedID}, {aclUser, 6, work}, {aclGroupObj, 6, aclUndefinedID}, {aclMask, 6, aclUndefinedID}, {aclOther, 0, aclUndefinedID}}
	if !bytes.Equal(encodeACL(got), encodeACL(want)) {
		t.Errorf("a 0600 file: %v, want %v", got, want)
	}
	// A program, with a lens's entry already on it and a mask that hid it.
	cur := []aclEntry{{aclUserObj, 7, aclUndefinedID}, {aclUser, 5, 2003}, {aclGroupObj, 0, aclUndefinedID}, {aclMask, 0, aclUndefinedID}, {aclOther, 4, aclUndefinedID}}
	got = teamShareAccess(cur, teamSharePerm(false, true), work)
	want = []aclEntry{{aclUserObj, 7, aclUndefinedID}, {aclUser, 5, 2003}, {aclUser, 7, work}, {aclGroupObj, 7, aclUndefinedID}, {aclMask, 7, aclUndefinedID}, {aclOther, 4, aclUndefinedID}}
	if !bytes.Equal(encodeACL(got), encodeACL(want)) {
		t.Errorf("a program: %v, want %v", got, want)
	}
	// Already shared: the same bytes, so nothing is written again.
	if again := teamShareAccess(got, teamSharePerm(false, true), work); !bytes.Equal(encodeACL(again), encodeACL(got)) {
		t.Errorf("not stable: %v", again)
	}
	if teamSharePerm(true, false) != 7 || teamSharePerm(false, false) != 6 {
		t.Error("a folder gets rwx, a file rw-")
	}
}

// A folder's default ACL: what's made in it is work's and the team's to
// read and write, whatever its creator's umask.
func TestTeamShareDefault(t *testing.T) {
	got := teamShareDefault(1000)
	want := []aclEntry{{aclUserObj, 7, aclUndefinedID}, {aclUser, 7, 1000}, {aclGroupObj, 7, aclUndefinedID}, {aclMask, 7, aclUndefinedID}, {aclOther, 0, aclUndefinedID}}
	if !bytes.Equal(encodeACL(got), encodeACL(want)) {
		t.Errorf("default %v, want %v", got, want)
	}
}

// ensureTeamDir gives /home/team its default ACL, with work's entry (the
// ADR's default ACL was never set: CC-0706 item 19).
func TestTeamDirGetsItsDefaultACL(t *testing.T) {
	team, _, _ := newTestTeam(t)
	var got []string
	team.teamDirACL = func(dir string, workUID int) error {
		got = append(got, dir)
		if workUID != 1000 {
			t.Errorf("work's uid %d", workUID)
		}
		return nil
	}
	if _, err := team.Ensure(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != team.Cfg.HomeRoot+"/team" {
		t.Errorf("team folder ACL set on %v", got)
	}
}
