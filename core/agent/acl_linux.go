// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	aclXattrAccess  = "system.posix_acl_access"
	aclXattrDefault = "system.posix_acl_default"
)

// fileProjectOps looks at and changes each file through its own
// descriptor.
func fileProjectOps() projectOps {
	gid := uint32(os.Getgid()) // #nosec G115 -- work's gid
	return projectOps{
		self:        os.Getuid(),
		othersWrite: func(f *os.File, st *unix.Stat_t, dir bool) (bool, error) { return othersWrite(f, st, dir, gid) },
		readOnly:    func(f *os.File, dir bool) error { return teamReadOnly(f, dir, gid) },
	}
}

// withFD runs fn with f's descriptor.
func withFD(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := rc.Control(func(fd uintptr) { inner = fn(int(fd)) }); err != nil { // #nosec G115 -- a descriptor
		return err
	}
	return inner
}

// getACL is f's ACL in the attribute, or nil if it has none.
func getACL(f *os.File, attr string) ([]aclEntry, error) {
	var es []aclEntry
	err := withFD(f, func(fd int) error {
		for range 3 { // it can grow between the two calls
			n, err := unix.Fgetxattr(fd, attr, nil)
			if errors.Is(err, unix.ENODATA) {
				return nil
			}
			if err != nil {
				return err
			}
			buf := make([]byte, n)
			n, err = unix.Fgetxattr(fd, attr, buf)
			if errors.Is(err, unix.ERANGE) {
				continue
			}
			if err != nil {
				return err
			}
			es, err = decodeACL(buf[:n])
			return err
		}
		return unix.ERANGE
	})
	return es, err
}

func setACL(f *os.File, attr string, es []aclEntry) error {
	return withFD(f, func(fd int) error { return unix.Fsetxattr(fd, attr, encodeACL(es), 0) })
}

// othersWrite is whether a uid other than the file's owner could write
// it: others' write, the write of an owning group that isn't ownGID (work's
// own group, which only work is in), or a named entry with write that the
// mask lets through. On a folder, its default ACL counts the same way,
// since files made in it get that ACL.
func othersWrite(f *os.File, st *unix.Stat_t, dir bool, ownGID uint32) (bool, error) {
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		return false, err
	}
	if acc == nil {
		acc = minimalACL(fs.FileMode(st.Mode & 0o777))
	}
	if aclLetsOthersWrite(acc, st.Uid, st.Gid, ownGID) {
		return true, nil
	}
	if !dir {
		return false, nil
	}
	def, err := getACL(f, aclXattrDefault)
	if err != nil || def == nil {
		return false, err
	}
	return aclLetsOthersWrite(def, st.Uid, st.Gid, ownGID), nil
}

// teamReadOnly gives f group team's read and no one else's write, through
// f itself: its ACL becomes its owner, owning group and other entries,
// others without write and an owning group other than ownGID without
// write, then g:team:r-X with a mask to match (named entries go). A
// folder gets the same as its default ACL, so files made in it later are
// the same. Nothing is written when the ACLs are already so.
func teamReadOnly(f *os.File, dir bool, ownGID uint32) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.ErrUnsupported
	}
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		return err
	}
	cur := acc
	if cur == nil {
		cur = minimalACL(fi.Mode())
	}
	perm := uint16(4)
	if dir || fi.Mode()&0o111 != 0 {
		perm = 5
	}
	want := aclWithGroup(readOnlyBase(cur, st.Gid, ownGID), TeamGID, perm)
	if acc == nil || !bytes.Equal(encodeACL(acc), encodeACL(want)) {
		if err := setACL(f, aclXattrAccess, want); err != nil {
			return err
		}
	}
	if !dir {
		return nil
	}
	def, err := getACL(f, aclXattrDefault)
	if err != nil {
		return err
	}
	wantDef := aclWithGroup(aclBase(want), TeamGID, 5)
	if def == nil || !bytes.Equal(encodeACL(def), encodeACL(wantDef)) {
		return setACL(f, aclXattrDefault, wantDef)
	}
	return nil
}

// statType is the file type bits of a stat.
func statType(st *unix.Stat_t) uint32 { return st.Mode & unix.S_IFMT }

func statDev(st *unix.Stat_t) uint64 { return st.Dev }

// fileLensOps sets lens entries through each file's own descriptor.
func fileLensOps() lensOps { return lensOps{set: setLensACL} }

// setLensACL gives readers (agent uids) read on f (r-x on a folder or a
// program, r-- otherwise), or --x with traverseOnly, and takes every other
// agent uid's entry away; on a folder (not traverseOnly) its default ACL
// gets the same, or loses its agent entries (and goes, when nothing is
// left in it) when there are no readers.
func setLensACL(f *os.File, dir bool, readers []uint32, traverseOnly bool) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		return err
	}
	cur := acc
	if cur == nil {
		cur = minimalACL(fi.Mode())
	}
	perm := uint16(4)
	if dir || fi.Mode()&0o111 != 0 {
		perm = 5
	}
	if traverseOnly {
		perm = 1
	}
	want := map[uint32]uint16{}
	for _, r := range readers {
		want[r] = perm
	}
	next := aclWithUsers(cur, want, AgentUIDFirst, AgentUIDLast)
	if acc == nil && !aclHasNamed(next) {
		// Nothing to add or take away.
	} else if acc == nil || !bytes.Equal(encodeACL(acc), encodeACL(next)) {
		if err := setACL(f, aclXattrAccess, next); err != nil {
			return err
		}
	}
	if !dir || traverseOnly {
		return nil
	}
	def, err := getACL(f, aclXattrDefault)
	if err != nil {
		return err
	}
	if def == nil && len(readers) == 0 {
		return nil
	}
	base := def
	if base == nil {
		base = aclBase(next)
	}
	dwant := map[uint32]uint16{}
	for _, r := range readers {
		dwant[r] = 5
	}
	nextDef := aclWithUsers(base, dwant, AgentUIDFirst, AgentUIDLast)
	if !aclHasNamed(nextDef) {
		err := withFD(f, func(fd int) error { return unix.Fremovexattr(fd, aclXattrDefault) })
		if errors.Is(err, unix.ENODATA) {
			return nil
		}
		return err
	}
	if def == nil || !bytes.Equal(encodeACL(def), encodeACL(nextDef)) {
		return setACL(f, aclXattrDefault, nextDef)
	}
	return nil
}
