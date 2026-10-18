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
func TestTeamACLThroughADescriptor(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o640); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := addTeamACL(f, false); errors.Is(err, unix.EOPNOTSUPP) {
		t.Skip("this filesystem doesn't keep ACLs")
	} else if err != nil {
		t.Fatal(err)
	}
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(acc, aclEntry{aclGroup, 6, TeamGID}) || !slices.Contains(acc, aclEntry{aclMask, 6, aclUndefinedID}) {
		t.Errorf("file ACL %v, want group:team:rw- and mask rw-", acc)
	}
	g, had, err := clearACL(f, false)
	if err != nil || !had || g != 0o040 {
		t.Errorf("clear: %o %v %v, want the owning group's r", g, had, err)
	}
	if acc, _ := getACL(f, aclXattrAccess); acc != nil {
		t.Errorf("still has %v", acc)
	}

	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := addTeamACL(d, true); err != nil {
		t.Fatal(err)
	}
	def, err := getACL(d, aclXattrDefault)
	if err != nil || !slices.Contains(def, aclEntry{aclGroup, 7, TeamGID}) {
		t.Errorf("default ACL %v %v, want group:team:rwx", def, err)
	}
	// A file made in the folder now gets team's entry.
	if err := os.WriteFile(filepath.Join(dir, "new"), []byte("x"), 0o664); err != nil {
		t.Fatal(err)
	}
	n, err := os.Open(filepath.Join(dir, "new"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Close() }()
	if acc, _ := getACL(n, aclXattrAccess); !slices.ContainsFunc(acc, func(e aclEntry) bool { return e.tag == aclGroup && e.id == TeamGID }) {
		t.Errorf("a new file's ACL %v has no team entry", acc)
	}
}
