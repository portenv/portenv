// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The Agent files view's file work in an agent's home (ADR 0022 §2,
// R-0254 rule 1), run as the agent: paths are relative, with no "..", and
// every step is opened with O_NOFOLLOW from the home down (an openat
// walk), so nothing leaves the home through a symlink the agent planted.

// errHomeFull means a write would take the home past its quota.
var errHomeFull = errors.New("the agent's files are full")

// homeRel checks a path relative to an agent's home and returns its parts.
func homeRel(p string, allowHome bool) ([]string, error) {
	if p == "" {
		if allowHome {
			return nil, nil
		}
		return nil, errors.New("no path given")
	}
	if strings.ContainsRune(p, 0) || path.IsAbs(p) || path.Clean(p) != p {
		return nil, fmt.Errorf("%q isn't a plain path in the agent's home", p)
	}
	parts := strings.Split(p, "/")
	for _, s := range parts {
		if s == "" || s == "." || s == ".." {
			return nil, fmt.Errorf("%q isn't a plain path in the agent's home", p)
		}
	}
	return parts, nil
}

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// openDirs opens home, then each of parts as a folder, never following a
// symlink; with create, missing folders are made (0700). The caller closes
// the returned descriptor.
func openDirs(home string, parts []string, create bool) (int, error) {
	fd, err := unix.Open(home, dirFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("open the home: %w", err)
	}
	for _, s := range parts {
		next, err := unix.Openat(fd, s, dirFlags, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err := unix.Mkdirat(fd, s, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				_ = unix.Close(fd)
				return -1, fmt.Errorf("make %s: %w", s, err)
			}
			next, err = unix.Openat(fd, s, dirFlags, 0)
		}
		_ = unix.Close(fd)
		if err != nil {
			return -1, fmt.Errorf("%s isn't a folder in the home (a link isn't followed): %w", s, err)
		}
		fd = next
	}
	return fd, nil
}

type homeEntry struct {
	Path     string `json:"path"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

func homeList(home, rel string, w io.Writer) error {
	parts, err := homeRel(rel, true)
	if err != nil {
		return err
	}
	fd, err := openDirs(home, parts, false)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), rel) // #nosec G115 -- a descriptor
	defer func() { _ = dir.Close() }()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	out := []homeEntry{}
	for _, n := range names {
		var st unix.Stat_t
		if unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
			continue
		}
		mode := st.Mode & unix.S_IFMT
		if mode != unix.S_IFDIR && mode != unix.S_IFREG {
			continue // links and devices aren't shown, or followed
		}
		out = append(out, homeEntry{Path: path.Join(rel, n), Dir: mode == unix.S_IFDIR, Size: st.Size, Modified: time.Unix(st.Mtim.Unix()).Unix()})
	}
	return json.NewEncoder(w).Encode(out)
}

func homeRead(home, rel string, w io.Writer) error {
	parts, err := homeRel(rel, false)
	if err != nil {
		return err
	}
	dfd, err := openDirs(home, parts[:len(parts)-1], false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	fd, err := unix.Openat(dfd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%s isn't a file in the home (a link isn't followed): %w", rel, err)
	}
	f := os.NewFile(uintptr(fd), rel) // #nosec G115 -- a descriptor
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a file", rel)
	}
	_, err = io.Copy(w, f)
	return err
}

// homeSize is the size of the regular files in the home, never following
// a link.
func homeSize(home string) (int64, error) {
	var n int64
	err := filepath.WalkDir(home, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable parts count as nothing
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n, err
}

func homeWrite(home, rel, quota string, r io.Reader, w io.Writer) error {
	parts, err := homeRel(rel, false)
	if err != nil {
		return err
	}
	limit, err := strconv.ParseInt(quota, 10, 64)
	if err != nil || limit <= 0 {
		return errors.New("no quota given")
	}
	used, err := homeSize(home)
	if err != nil {
		return err
	}
	room := limit - used
	if room < 0 {
		room = 0
	}
	data, err := io.ReadAll(io.LimitReader(r, room+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > room {
		return errHomeFull
	}
	dfd, err := openDirs(home, parts[:len(parts)-1], true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	base := parts[len(parts)-1]
	fd, err := unix.Openat(dfd, base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%s is already there", rel)
		}
		return err
	}
	f := os.NewFile(uintptr(fd), rel) // #nosec G115 -- a descriptor
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = unix.Unlinkat(dfd, base, 0)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = unix.Unlinkat(dfd, base, 0)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return json.NewEncoder(w).Encode([]homeEntry{{Path: rel, Size: int64(len(data)), Modified: time.Now().Unix()}})
}
