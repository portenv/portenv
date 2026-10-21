// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// workEntry is the perm of the named entry for uid in path's access ACL,
// and whether the mask lets it through; -1 when there's none.
func workEntry(t *testing.T, path string, uid uint32) int {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- the test's file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	es, err := getACL(f, aclXattrAccess)
	if err != nil {
		t.Fatal(err)
	}
	mask, ok := aclPerm(es, aclMask)
	if !ok {
		return -1
	}
	for _, e := range es {
		if e.tag == aclUser && e.id == uid {
			return int(e.perm & mask)
		}
	}
	return -1
}

func testShare(t *testing.T) (string, teamShare) {
	t.Helper()
	dir := t.TempDir()
	probe, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if err := setACL(probe, aclXattrAccess, minimalACL(0o700)); errors.Is(err, unix.EOPNOTSUPP) {
		t.Skip("no ACLs on this file system")
	}
	me := uint32(os.Getuid())                                                                                          // #nosec G115 -- a uid
	return dir, teamShare{workUID: 65534, gid: uint32(os.Getgid()), owned: func(uid uint32) bool { return uid == me }} // #nosec G115 -- a gid
}

// CC-0706 item 19: a file an agent writes 0600, or renames in, and a
// folder it makes 0700, become readable by work (its named entry) and the
// team; a link and a file not an agent's are left as they are.
func TestTeamShareWalk(t *testing.T) {
	dir, ts := testShare(t)
	old := unix.Umask(0o077)
	defer unix.Umask(old)
	if err := os.WriteFile(filepath.Join(dir, "a.patch"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "b.tmp")
	if err := os.WriteFile(tmp, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "b.patch")); err != nil {
		t.Skipf("rename across folders: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("p"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := ts.walk(root, 0); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.patch", "b.patch", "sub/c.txt"} {
		if got := workEntry(t, filepath.Join(dir, p), ts.workUID); got != 6 {
			t.Errorf("%s: work's entry %d, want rw-", p, got)
		}
		if fi, _ := os.Stat(filepath.Join(dir, p)); fi.Mode().Perm()&0o060 != 0o060 {
			t.Errorf("%s: mode %v, want group rw", p, fi.Mode())
		}
	}
	if got := workEntry(t, filepath.Join(dir, "sub"), ts.workUID); got != 7 {
		t.Errorf("sub: work's entry %d, want rwx", got)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "sub")); fi.Mode()&os.ModeSetgid == 0 {
		t.Errorf("sub isn't setgid: %v", fi.Mode())
	}
	if got := workEntry(t, outside, ts.workUID); got != -1 {
		t.Errorf("the link's target was changed: %d", got)
	}
	if fi, _ := os.Stat(outside); fi.Mode().Perm() != 0o600 {
		t.Errorf("the link's target's mode changed: %v", fi.Mode())
	}
	// A hard-linked file stays as it is: it has another name elsewhere.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("e"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(elsewhere, filepath.Join(dir, "hard")); err == nil {
		if err := ts.walk(root, 0); err != nil {
			t.Fatal(err)
		}
		if got := workEntry(t, elsewhere, ts.workUID); got != -1 {
			t.Errorf("a hard-linked file was shared: %d", got)
		}
	}
	// A file that isn't an agent's stays as it is.
	if err := os.WriteFile(filepath.Join(dir, "mine"), []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	none := ts
	none.owned = func(uint32) bool { return false }
	if err := none.walk(root, 0); err != nil {
		t.Fatal(err)
	}
	if got := workEntry(t, filepath.Join(dir, "mine"), ts.workUID); got != -1 {
		t.Errorf("a file not an agent's was shared: %d", got)
	}
}

// The watcher shares what's written or renamed in after it starts.
func TestTeamShareWatches(t *testing.T) {
	dir, ts := testShare(t)
	old := unix.Umask(0o077)
	defer unix.Umask(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	go func() { _ = ts.watch(ctx, dir, ready, func(err error) { t.Log(err) }) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher didn't start")
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "new.patch"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "moved.tmp")
	if err := os.WriteFile(tmp, []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "moved.patch")); err != nil {
		t.Skipf("rename across folders: %v", err)
	}
	for _, p := range []string{"sub/new.patch", "moved.patch"} {
		deadline := time.Now().Add(5 * time.Second)
		for workEntry(t, filepath.Join(dir, p), ts.workUID) != 6 {
			if time.Now().After(deadline) {
				t.Fatalf("%s wasn't shared within 5 s", p)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
