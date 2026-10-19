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

// The real ACLs: the lens's entries on the home and the clone, then gone
// when the grant goes.
func TestLensGrantACLs(t *testing.T) {
	home := agentHomeDir(t)
	clone := filepath.Join(home, "shop")
	if err := os.MkdirAll(filepath.Join(clone, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "src", "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lensGrant(home, "shop", "2002", fileLensOps()); errors.Is(err, errNoACL) {
		t.Skip("this filesystem doesn't keep ACLs")
	} else if err != nil {
		t.Fatal(err)
	}
	acl := func(p, attr string) []aclEntry {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		es, err := getACL(f, attr)
		if err != nil {
			t.Fatal(err)
		}
		return es
	}
	if es := acl(home, aclXattrAccess); !slices.Contains(es, aclEntry{aclUser, 1, 2002}) || slices.ContainsFunc(es, func(e aclEntry) bool { return e.tag == aclGroup }) {
		t.Errorf("home ACL %v, want user 2002 --x and no group entry", es)
	}
	if es := acl(home, aclXattrDefault); es != nil {
		t.Errorf("the home got a default ACL %v", es)
	}
	if es := acl(filepath.Join(clone, "src", "a.go"), aclXattrAccess); !slices.Contains(es, aclEntry{aclUser, 4, 2002}) {
		t.Errorf("a.go ACL %v, want user 2002 r--", es)
	}
	if es := acl(filepath.Join(clone, "src"), aclXattrDefault); !slices.Contains(es, aclEntry{aclUser, 5, 2002}) {
		t.Errorf("src default ACL %v, want user 2002 r-x", es)
	}
	// No write anywhere for the lens.
	for _, p := range []string{home, clone, filepath.Join(clone, "src"), filepath.Join(clone, "src", "a.go")} {
		for _, e := range acl(p, aclXattrAccess) {
			if e.tag == aclUser && e.perm&2 != 0 {
				t.Errorf("%s: %v gives write", p, e)
			}
		}
	}
	// A new file in the clone stays readable to the lens.
	if err := os.WriteFile(filepath.Join(clone, "src", "b.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if es := acl(filepath.Join(clone, "src", "b.go"), aclXattrAccess); !slices.ContainsFunc(es, func(e aclEntry) bool { return e.tag == aclUser && e.id == 2002 }) {
		t.Errorf("a new file's ACL %v has no lens entry", es)
	}
	// The grant goes: every entry of the lens goes.
	if err := lensGrant(home, "shop", "-", fileLensOps()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{home, clone, filepath.Join(clone, "src"), filepath.Join(clone, "src", "a.go")} {
		if es := acl(p, aclXattrAccess); slices.ContainsFunc(es, func(e aclEntry) bool { return e.tag == aclUser }) {
			t.Errorf("%s still has %v", p, es)
		}
	}
	if es := acl(filepath.Join(clone, "src"), aclXattrDefault); es != nil {
		t.Errorf("src's default ACL stayed: %v", es)
	}
	var st unix.Stat_t
	if err := unix.Stat(home, &st); err != nil || st.Mode&0o077 != 0 {
		t.Errorf("home mode %o after the grant went, want 0700", st.Mode&0o777)
	}
}
