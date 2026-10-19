// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

// The real calls on this machine's filesystem: an ACL entry can name any
// gid, so team's needs no group here.
func TestTeamReadOnlyThroughADescriptor(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	stat := func(f *os.File) *unix.Stat_t {
		var st unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &st); err != nil {
			t.Fatal(err)
		}
		return &st
	}
	st := stat(f)
	own, other := st.Gid, st.Gid+1 // the file's group as work's own, or as another
	if w, err := othersWrite(f, st, false, own); err != nil || !w {
		t.Errorf("a 0666 file: othersWrite = %v %v, want true (others' write)", w, err)
	}
	if err := teamReadOnly(f, false, other); errors.Is(err, unix.EOPNOTSUPP) {
		t.Skip("this filesystem doesn't keep ACLs")
	} else if err != nil {
		t.Fatal(err)
	}
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []aclEntry{{aclUserObj, 6, aclUndefinedID}, {aclGroupObj, 4, aclUndefinedID}, {aclGroup, 4, TeamGID}, {aclMask, 4, aclUndefinedID}, {aclOther, 4, aclUndefinedID}} {
		if !slices.Contains(acc, want) {
			t.Errorf("file ACL %v, want %v in it", acc, want)
		}
	}
	if w, err := othersWrite(f, stat(f), false, other); err != nil || w {
		t.Errorf("after teamReadOnly: othersWrite = %v %v, want false", w, err)
	}
	// The owning group keeps its write when it's work's own group.
	if err := teamReadOnly(f, false, own); err != nil {
		t.Fatal(err)
	}
	// A named entry with write lets another uid write.
	if err := setACL(f, aclXattrAccess, aclWithGroup(acc, 3000, 6)); err != nil {
		t.Fatal(err)
	}
	if w, err := othersWrite(f, stat(f), false, own); err != nil || !w {
		t.Errorf("a named group with write: othersWrite = %v %v, want true", w, err)
	}
	if err := teamReadOnly(f, false, own); err != nil {
		t.Fatal(err)
	}
	if acc, _ := getACL(f, aclXattrAccess); slices.ContainsFunc(acc, func(e aclEntry) bool { return e.tag == aclGroup && e.id == 3000 }) {
		t.Errorf("the named entry stayed: %v", acc)
	}

	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	// A default ACL that gives a named group write counts as others' write.
	if err := setACL(d, aclXattrDefault, aclWithGroup(minimalACL(0o755), 3000, 7)); err != nil {
		t.Fatal(err)
	}
	if w, err := othersWrite(d, stat(d), true, own); err != nil || !w {
		t.Errorf("a default ACL with a named group's write: othersWrite = %v %v, want true", w, err)
	}
	if err := teamReadOnly(d, true, own); err != nil {
		t.Fatal(err)
	}
	def, err := getACL(d, aclXattrDefault)
	if err != nil || !slices.Contains(def, aclEntry{aclGroup, 5, TeamGID}) || slices.ContainsFunc(def, func(e aclEntry) bool { return e.id == 3000 }) {
		t.Errorf("default ACL %v %v, want group:team:r-x and no other named entry", def, err)
	}
	// A file made in the folder now gets team's read, and no one else's
	// write.
	if err := os.WriteFile(filepath.Join(dir, "new"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := os.Open(filepath.Join(dir, "new"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Close() }()
	// (From the default r-x, under the mask the file's mode makes: r--.)
	if acc, _ := getACL(n, aclXattrAccess); !slices.ContainsFunc(acc, func(e aclEntry) bool { return e.tag == aclGroup && e.id == TeamGID && e.perm&4 != 0 }) {
		t.Errorf("a new file's ACL %v has no team read", acc)
	}
	if w, err := othersWrite(n, stat(n), false, own); err != nil || w {
		t.Errorf("a new file: othersWrite = %v %v, want false", w, err)
	}
}
