// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// reown on a real filesystem (as the file's own user, so the chown is to
// itself): ACLs gone, no group or other write, no setgid.
func TestReownMakesAFileWorksAlone(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o2775); err != nil {
		t.Fatal(err)
	}
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := setACL(d, aclXattrDefault, aclWithGroup(minimalACL(0o775), TeamGID, 7)); errors.Is(err, unix.EOPNOTSUPP) {
		t.Skip("this filesystem doesn't keep ACLs")
	} else if err != nil {
		t.Fatal(err)
	}
	ops := defaultMigrateOps(Config{UID: os.Getuid()})
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, x := range []struct {
		f    *os.File
		dir  bool
		want os.FileMode
	}{{f, false, 0o644}, {d, true, 0o755}} {
		if err := ops.reown(x.f, x.dir); err != nil {
			t.Fatal(err)
		}
		fi, err := x.f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&(os.ModePerm|os.ModeSetgid) != x.want {
			t.Errorf("%s: %v, want %o", x.f.Name(), fi.Mode(), x.want)
		}
		for _, a := range []string{aclXattrAccess, aclXattrDefault} {
			if es, _ := getACL(x.f, a); es != nil {
				t.Errorf("%s still has %s %v", x.f.Name(), a, es)
			}
		}
	}
	if _, err := agentUIDsRunning(); err != nil {
		t.Errorf("reading /proc: %v", err)
	}
}
