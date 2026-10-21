// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// maxTeamShareDepth bounds how deep the team folder is walked.
const maxTeamShareDepth = 64

// teamShare shares what agents put in the team folder (teamshare.go): as
// root, through each file's own descriptor, never following a link, and
// only an agent's own regular files (with no other name) and folders. A
// file of work's or root's, a link, a hard-linked file, or anything else
// is left as it is.
type teamShare struct {
	workUID uint32
	gid     uint32
	owned   func(uid uint32) bool
}

func newTeamShare(workUID int) teamShare {
	return teamShare{
		workUID: uint32(workUID), // #nosec G115 -- work's uid
		gid:     TeamGID,
		owned:   func(uid uint32) bool { return uid >= AgentUIDFirst && uid <= AgentUIDLast },
	}
}

// setTeamDirACL gives the team folder its default ACL (ADR 0022 §2): what's
// made in it is the team's and work's to read and write.
func setTeamDirACL(dir string, workUID int) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0) // #nosec G304 -- /home/team
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	want := teamShareDefault(uint32(workUID)) // #nosec G115 -- work's uid
	def, err := getACL(f, aclXattrDefault)
	if err != nil {
		return err
	}
	if def != nil && bytes.Equal(encodeACL(def), encodeACL(want)) {
		return nil
	}
	if err := setACL(f, aclXattrDefault, want); err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) {
			return errNoACL
		}
		return fmt.Errorf("the team folder's default ACL: %w", err)
	}
	return nil
}

// entry shares name in dir if it's an agent's file or folder. A folder is
// returned open, for the caller to walk and close.
func (ts teamShare) entry(dir *os.File, name string) (*os.File, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(fdOf(dir), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	typ := statType(&st)
	if (typ != unix.S_IFREG && typ != unix.S_IFDIR) || !ts.owned(st.Uid) {
		return nil, nil
	}
	// A file with another name elsewhere (a hard link) may be another
	// agent's that it can only read: sharing it here would let the whole
	// team write it.
	if typ == unix.S_IFREG && st.Nlink != 1 {
		return nil, nil
	}
	f, ok, err := openEntry(dir, name, filepath.Join(dir.Name(), name), &st)
	if err != nil || !ok {
		return nil, err
	}
	isDir := typ == unix.S_IFDIR
	if err := ts.fix(f, isDir); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("share %s: %w", filepath.Join(dir.Name(), name), err)
	}
	if isDir {
		return f, nil
	}
	_ = f.Close()
	return nil, nil
}

// fix makes f group team's, a folder setgid, and gives the group and work
// read and write (and search or run). It writes nothing that's already so.
func (ts teamShare) fix(f *os.File, dir bool) error {
	fd := fdOf(f)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Gid != ts.gid {
		if err := unix.Fchown(fd, -1, int(ts.gid)); err != nil {
			return err
		}
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
	}
	if dir && st.Mode&unix.S_ISGID == 0 {
		if err := unix.Fchmod(fd, st.Mode&0o7777|unix.S_ISGID); err != nil {
			return err
		}
	}
	acc, err := getACL(f, aclXattrAccess)
	if err != nil {
		return err
	}
	cur := acc
	if cur == nil {
		cur = minimalACL(fs.FileMode(st.Mode & 0o777))
	}
	want := teamShareAccess(cur, teamSharePerm(dir, st.Mode&0o100 != 0), ts.workUID)
	if acc == nil || !bytes.Equal(encodeACL(acc), encodeACL(want)) {
		if err := setACL(f, aclXattrAccess, want); err != nil {
			return err
		}
	}
	if !dir {
		return nil
	}
	wantDef := teamShareDefault(ts.workUID)
	def, err := getACL(f, aclXattrDefault)
	if err != nil {
		return err
	}
	if def == nil || !bytes.Equal(encodeACL(def), encodeACL(wantDef)) {
		return setACL(f, aclXattrDefault, wantDef)
	}
	return nil
}

// walk shares everything under dir.
func (ts teamShare) walk(dir *os.File, depth int) error {
	return ts.scan(dir, depth, nil)
}

// scan is walk, calling seen (if set) with each folder first, dir
// included, before what's in it.
func (ts teamShare) scan(dir *os.File, depth int, seen func(*os.File)) error {
	if depth > maxTeamShareDepth {
		return nil
	}
	if seen != nil {
		seen(dir)
	}
	fd, err := unix.Openat(fdOf(dir), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	d := os.NewFile(uintptr(fd), dir.Name()) // #nosec G115 -- a descriptor
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		sub, err := ts.entry(d, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if sub != nil {
			errs = append(errs, ts.scan(sub, depth+1, seen))
			_ = sub.Close()
		}
	}
	return errors.Join(errs...)
}

const teamShareEvents = unix.IN_CREATE | unix.IN_MOVED_TO | unix.IN_CLOSE_WRITE | unix.IN_ATTRIB

// shareWatch is one inotify instance over the team folder and every
// folder in it, each watch named by its path relative to the folder.
type shareWatch struct {
	ts   teamShare
	root *os.File
	ifd  int
	rel  map[int]string
}

// add watches dir (rel in the team folder) through its own descriptor.
func (w *shareWatch) add(dir *os.File, rel string) {
	wd, err := unix.InotifyAddWatch(w.ifd, "/proc/self/fd/"+strconv.Itoa(fdOf(dir)), teamShareEvents|unix.IN_ONLYDIR)
	if err == nil {
		w.rel[wd] = rel
	}
}

// scanFrom watches dir (rel) and every folder under it, and shares
// what's in them.
func (w *shareWatch) scanFrom(dir *os.File, rel string, depth int) error {
	base := strings.TrimSuffix(dir.Name(), "/")
	return w.ts.scan(dir, depth, func(d *os.File) {
		r := rel
		if n := strings.TrimSuffix(d.Name(), "/"); n != base {
			r = path.Join(rel, strings.TrimPrefix(n, base+"/"))
		}
		w.add(d, r)
	})
}

// openRel opens rel in the team folder one folder at a time, never
// following a link: a folder an agent swapped for a link isn't entered.
func (w *shareWatch) openRel(rel string) (*os.File, error) {
	fd, err := unix.Openat(fdOf(w.root), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	name := w.root.Name()
	if rel != "" {
		for _, part := range strings.Split(rel, "/") {
			next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			_ = unix.Close(fd)
			if err != nil {
				return nil, err
			}
			fd, name = next, filepath.Join(name, part)
		}
	}
	return os.NewFile(uintptr(fd), name), nil // #nosec G115 -- a descriptor
}

// event shares one entry that appeared or changed, and watches it if it's
// a folder that came in.
func (w *shareWatch) event(wd int, mask uint32, name string) error {
	rel, ok := w.rel[wd]
	if !ok || name == "" {
		return nil
	}
	dir, err := w.openRel(rel)
	if err != nil {
		return nil // gone or swapped since: nothing of the team folder's to share
	}
	defer func() { _ = dir.Close() }()
	sub, err := w.ts.entry(dir, name)
	if err != nil || sub == nil {
		return err
	}
	defer func() { _ = sub.Close() }()
	if mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
		return w.scanFrom(sub, path.Join(rel, name), strings.Count(rel, "/")+1)
	}
	return nil
}

// watch shares what's in dir now, then what appears in it, until ctx
// ends. ready (if set) is closed once the watches are in place; report
// (if set) hears what couldn't be shared.
func (ts teamShare) watch(ctx context.Context, dir string, ready chan<- struct{}, report func(error)) error {
	root, err := os.OpenFile(dir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0) // #nosec G304 -- /home/team
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	ifd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(ifd) }()
	w := &shareWatch{ts: ts, root: root, ifd: ifd, rel: map[int]string{}}
	var errs []error
	rescan := func() {
		if err := w.scanFrom(root, "", 0); err != nil {
			errs = append(errs, err)
		}
	}
	rescan()
	if ready != nil {
		close(ready)
	}
	buf := make([]byte, 64<<10)
	for ctx.Err() == nil {
		if len(errs) > 0 {
			err := errors.Join(errs...)
			errs = nil
			if report != nil {
				report(err)
			}
		}
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(ifd), Events: unix.POLLIN}}, 500) // #nosec G115 -- a descriptor
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		if n <= 0 {
			continue
		}
		m, err := unix.Read(ifd, buf)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		for off := 0; off+unix.SizeofInotifyEvent <= m; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off])) // #nosec G103 -- the kernel's event layout
			nameBytes := buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+int(ev.Len)]
			off += unix.SizeofInotifyEvent + int(ev.Len)
			name := string(bytes.TrimRight(nameBytes, "\x00"))
			switch {
			case ev.Mask&unix.IN_Q_OVERFLOW != 0:
				rescan()
			case ev.Mask&unix.IN_IGNORED != 0:
				delete(w.rel, int(ev.Wd))
			default:
				if err := w.event(int(ev.Wd), ev.Mask, name); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return nil
}

// watchTeamShare runs in init (root) for the box's life: once the team
// folder exists, it shares what agents put in it (CC-0706 item 19).
func watchTeamShare(ctx context.Context, dir string, workUID int, log *slog.Logger) {
	report := func(err error) { log.Error("team folder: a file couldn't be shared", "err", err) }
	ts := newTeamShare(workUID)
	for ctx.Err() == nil {
		if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
			if err := ts.watch(ctx, dir, nil, report); err != nil {
				log.Error("team folder: watching it failed; trying again", "err", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}
