// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

const (
	aclXattrAccess  = "system.posix_acl_access"
	aclXattrDefault = "system.posix_acl_default"
)

// fileProjectOps changes each file through its own descriptor.
func fileProjectOps() projectOps {
	return projectOps{
		self:     os.Getuid(),
		chown:    func(f *os.File, gid int) error { return f.Chown(-1, gid) },
		addACL:   addTeamACL,
		readACL:  addTeamReadACL,
		clearACL: clearACL,
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

// addTeamACL is setfacl -m g:team:rwX (and d:g:team:rwX on a folder) on
// f itself.
func addTeamACL(f *os.File, dir bool) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		return err
	}
	if acc == nil {
		acc = minimalACL(fi.Mode())
	}
	perm := uint16(6)
	if dir || fi.Mode()&0o111 != 0 {
		perm = 7
	}
	if err := setACL(f, aclXattrAccess, aclWithGroup(acc, TeamGID, perm)); err != nil {
		return err
	}
	if !dir {
		return nil
	}
	def, err := getACL(f, aclXattrDefault)
	if err != nil {
		return err
	}
	if def == nil {
		def = aclBase(acc)
	}
	return setACL(f, aclXattrDefault, aclWithGroup(def, TeamGID, 7))
}

// addTeamReadACL is setfacl -m g:team:rX (and d:g:team:rX on a folder)
// on f, which clearACL just left without an ACL: the mask becomes the
// owning group's bits and team's r or r-x, never write.
func addTeamReadACL(f *os.File, dir bool) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	acc := minimalACL(fi.Mode())
	perm := uint16(4)
	if dir || fi.Mode()&0o111 != 0 {
		perm = 5
	}
	if err := setACL(f, aclXattrAccess, aclWithGroup(acc, TeamGID, perm)); err != nil {
		return err
	}
	if !dir {
		return nil
	}
	return setACL(f, aclXattrDefault, aclWithGroup(acc, TeamGID, 5))
}

// clearACL is setfacl -b on f itself: its ACLs removed. It returns the
// owning group's permission bits the ACL held (the mode's group bits are
// the mask until the caller sets them), and whether it had one.
func clearACL(f *os.File, dir bool) (fs.FileMode, bool, error) {
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		return 0, false, err
	}
	attrs := []string{aclXattrAccess}
	if dir {
		attrs = append(attrs, aclXattrDefault)
	}
	for _, a := range attrs {
		err := withFD(f, func(fd int) error { return unix.Fremovexattr(fd, a) })
		if err != nil && !errors.Is(err, unix.ENODATA) {
			return 0, false, err
		}
	}
	if acc == nil {
		return 0, false, nil
	}
	g, _ := aclPerm(acc, aclGroupObj)
	return fs.FileMode(g) << 3, true, nil
}

// statType is the file type bits of a stat.
func statType(st *unix.Stat_t) uint32 { return st.Mode & unix.S_IFMT }

func statDev(st *unix.Stat_t) uint64 { return st.Dev }
