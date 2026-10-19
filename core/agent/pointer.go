// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/portenv/portenv/core/internal/teamkit"
)

// maxPointerRead is how much of an existing file init reads to tell
// Portenv's pointer from the agent's own file: the marker line is enough.
const maxPointerRead = 4 << 10

// pointerWriter writes an agent's start pointer (R-0327) as root, into a
// folder the agent owns, without following a link or writing through an
// existing file: every step is relative to an open descriptor of the
// agent's home, opened with O_NOFOLLOW, and a file is only ever created
// new (O_EXCL) or swapped in by rename, so a symlink, a hard link or a
// FIFO the agent left there is never written through.
type pointerWriter struct {
	// rootUID owns Portenv's pointer: 0, or the test's own uid.
	rootUID int
	// agentUID owns the agent's home and the pointer's folder.
	agentUID int
	// fchown sets an open file's or folder's owner (f.Chown; a recorder in
	// tests, which don't run as root).
	fchown func(f *os.File, uid, gid int) error
	// owner reads an open file's owner (its Stat_t; a fake in tests).
	owner func(f *os.File, fi os.FileInfo) int
}

func statOwner(_ *os.File, fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// write writes content at home/rel (rel one of teamkit.PointerFiles: a
// folder and a file in it). It returns "" when the pointer is in place,
// or a plain line saying why it left the path alone: a file the agent
// made there (a file is Portenv's only when root owns it and it starts
// with teamkit.PointerMarker), or a folder that isn't the agent's.
func (p pointerWriter) write(home, rel, content string) (kept string, err error) {
	if !teamkit.IsPointerFile(rel) {
		return "", fmt.Errorf("%q isn't a file Portenv writes for a runner", rel)
	}
	dirName, fileName := filepath.Split(rel)
	dirName = strings.TrimSuffix(dirName, "/")
	if dirName == "" || strings.Contains(dirName, "/") {
		return "", fmt.Errorf("%q isn't a folder and a file", rel)
	}
	owner := p.owner
	if owner == nil {
		owner = statOwner
	}
	h, err := openDirNoFollow(unix.AT_FDCWD, home)
	if err != nil {
		return "", fmt.Errorf("open the agent's home: %w", err)
	}
	defer func() { _ = h.Close() }()
	if fi, err := h.Stat(); err != nil || owner(h, fi) != p.agentUID {
		return "", fmt.Errorf("%s isn't the agent's home", home)
	}

	// The folder: the agent's, 0700, made now if it's missing. Runners
	// write their own settings there, so it stays the agent's.
	made := false
	if err := unix.Mkdirat(int(h.Fd()), dirName, 0o700); err == nil {
		made = true
	} else if !errors.Is(err, unix.EEXIST) {
		return "", fmt.Errorf("make ~/%s: %w", dirName, err)
	}
	d, err := openDirNoFollow(int(h.Fd()), dirName)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return "~/" + dirName + " isn't a folder, so Portenv left it alone.", nil
		}
		return "", fmt.Errorf("open ~/%s: %w", dirName, err)
	}
	defer func() { _ = d.Close() }()
	if made {
		if err := p.fchown(d, p.agentUID, p.agentUID); err != nil {
			return "", fmt.Errorf("give ~/%s to the agent: %w", dirName, err)
		}
		if err := d.Chmod(0o700); err != nil {
			return "", err
		}
	} else if fi, err := d.Stat(); err != nil || owner(d, fi) != p.agentUID {
		return "~/" + dirName + " isn't the agent's folder, so Portenv left it alone.", nil
	}

	// What's there now: nothing, Portenv's pointer, or the agent's file.
	switch ours, err := p.isOurs(int(d.Fd()), fileName, owner); {
	case errors.Is(err, unix.ENOENT):
		// Made new: O_EXCL fails if anything appeared meanwhile.
		f, err := p.create(int(d.Fd()), fileName, content)
		if err != nil {
			if errors.Is(err, unix.EEXIST) {
				return "~/" + rel + " appeared while Portenv wrote it, so Portenv left it alone.", nil
			}
			return "", err
		}
		return "", f.Close()
	case err != nil:
		return "", fmt.Errorf("read ~/%s: %w", rel, err)
	case !ours:
		return "~/" + rel + " is the agent's own file, so Portenv left it and didn't add its pointer.", nil
	}
	// Portenv's own pointer: a new one, swapped in by rename (never written
	// through, so a hard link to it can't carry the write elsewhere).
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	tmp := "." + fileName + ".portenv-" + hex.EncodeToString(rnd[:])
	f, err := p.create(int(d.Fd()), tmp, content)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(int(d.Fd()), tmp, 0)
		return "", err
	}
	if err := unix.Renameat(int(d.Fd()), tmp, int(d.Fd()), fileName); err != nil {
		_ = unix.Unlinkat(int(d.Fd()), tmp, 0)
		return "", fmt.Errorf("replace ~/%s: %w", rel, err)
	}
	return "", nil
}

// isOurs reports whether name in dir is Portenv's pointer: a regular file
// root owns that starts with the marker. A link, FIFO or folder is the
// agent's; ENOENT means nothing is there.
func (p pointerWriter) isOurs(dir int, name string, owner func(*os.File, os.FileInfo) int) (bool, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENXIO):
		return false, nil
	case err != nil:
		return false, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || owner(f, fi) != p.rootUID {
		return false, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPointerRead))
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(string(b), teamkit.PointerMarker+"\n"), nil
}

// create makes name in dir, new (O_EXCL, no link followed), root's and
// 0444, with content.
func (p pointerWriter) create(dir int, name, content string) (*os.File, error) {
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o444)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	fail := func(err error) (*os.File, error) {
		_ = f.Close()
		_ = unix.Unlinkat(dir, name, 0)
		return nil, err
	}
	if err := p.fchown(f, p.rootUID, p.rootUID); err != nil {
		return fail(fmt.Errorf("give %s to root: %w", name, err))
	}
	if err := f.Chmod(0o444); err != nil {
		return fail(err)
	}
	if _, err := f.WriteString(content); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	return f, nil
}

func openDirNoFollow(dir int, name string) (*os.File, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
