// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Seeding (ADR 0022 §2): on an agent's first start the helper copies its
// file's home_seed paths from the project into its home, at the same
// relative paths, as the agent. Only regular files and folders, never
// through a link; links, special files and hidden names are skipped; at
// most 100 MB and 10,000 files together, within the home's quota; only new
// files, so nothing the agent already has is replaced. A marker,
// ~/.portenv-seeded, records it, and later starts copy nothing again.

const (
	seedMarker   = ".portenv-seeded"
	maxSeedPaths = 64
)

// The caps on what one seeding copies (tests lower them).
var (
	maxSeedBytes int64 = 100 << 20
	maxSeedFiles       = 10000
)

// seedResult is what seed-home prints.
type seedResult struct {
	// Already: the marker was there, so nothing was copied.
	Already bool     `json:"already,omitempty"`
	Copied  []string `json:"copied,omitempty"`
	// Skipped are "<path>: <why>" lines (a link, hidden, missing, kept).
	Skipped []string `json:"skipped,omitempty"`
}

type seedFile struct {
	rel  []string
	data []byte
	exec bool
}

// checkSeedPath checks one home_seed path: relative, plain, no hidden part.
func checkSeedPath(p string) ([]string, error) {
	parts, err := homeRel(p, false)
	if err != nil {
		return nil, err
	}
	for _, s := range parts {
		if strings.HasPrefix(s, ".") {
			return nil, fmt.Errorf("%q has a hidden name; home_seed copies only plain files and folders", p)
		}
	}
	return parts, nil
}

// seedHome is "team-io seed-home PROJECT HOME QUOTA PATH…", run as the
// agent.
func seedHome(project, home, quota string, paths []string, w io.Writer) error {
	limit, err := strconv.ParseInt(quota, 10, 64)
	if err != nil || limit <= 0 {
		return errors.New("no quota given")
	}
	if len(paths) > maxSeedPaths {
		return fmt.Errorf("home_seed lists at most %d paths", maxSeedPaths)
	}
	hfd, err := unix.Open(home, dirFlags, 0)
	if err != nil {
		return fmt.Errorf("open the home: %w", err)
	}
	defer func() { _ = unix.Close(hfd) }()
	var st unix.Stat_t
	if err := unix.Fstatat(hfd, seedMarker, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return json.NewEncoder(w).Encode(seedResult{Already: true})
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}

	var res seedResult
	var files []seedFile
	var total int64
	skip := func(p, why string) { res.Skipped = append(res.Skipped, p+": "+why) }
	var walk func(dir int, name string, rel []string) error
	walk = func(dir int, name string, rel []string) error {
		shown := strings.Join(rel, "/")
		if strings.HasPrefix(name, ".") {
			skip(shown, "hidden")
			return nil
		}
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			total += st.Size
			if len(files) >= maxSeedFiles || total > maxSeedBytes {
				return fmt.Errorf("home_seed is at most %d MB and %d files together", maxSeedBytes>>20, maxSeedFiles)
			}
			fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			f := os.NewFile(uintptr(fd), shown) // #nosec G115 -- a descriptor
			defer func() { _ = f.Close() }()
			data := make([]byte, st.Size)
			if _, err := io.ReadFull(f, data); err != nil {
				return fmt.Errorf("%s changed while it was read", shown)
			}
			files = append(files, seedFile{rel: append([]string{}, rel...), data: data, exec: st.Mode&0o100 != 0})
			return nil
		case unix.S_IFDIR:
			sub, err := unix.Openat(dir, name, dirFlags, 0)
			if err != nil {
				return err
			}
			d := os.NewFile(uintptr(sub), shown) // #nosec G115 -- a descriptor
			defer func() { _ = d.Close() }()
			names, err := d.Readdirnames(-1)
			if err != nil {
				return err
			}
			for _, n := range names {
				if err := walk(sub, n, append(append([]string{}, rel...), n)); err != nil {
					return err
				}
			}
			return nil
		}
		skip(shown, "a link or a special file")
		return nil
	}
	for _, p := range paths {
		parts, err := checkSeedPath(p)
		if err != nil {
			skip(p, err.Error())
			continue
		}
		dfd, err := openDirs(project, parts[:len(parts)-1], false)
		if err != nil {
			// A folder on the way is missing or a link: nothing to copy.
			skip(p, "not a plain path in the project")
			continue
		}
		var top unix.Stat_t
		if err := unix.Fstatat(dfd, parts[len(parts)-1], &top, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
			_ = unix.Close(dfd)
			skip(p, "not in the project")
			continue
		}
		err = walk(dfd, parts[len(parts)-1], parts)
		_ = unix.Close(dfd)
		if err != nil {
			return err
		}
	}

	used, err := homeSize(home)
	if err != nil {
		return err
	}
	if used+total > limit {
		return errHomeFull
	}
	for _, f := range files {
		dfd, err := openDirs(home, f.rel[:len(f.rel)-1], true)
		if err != nil {
			skip(path.Join(f.rel...), "a folder on the way isn't a plain folder in the home")
			continue
		}
		mode := uint32(0o600)
		if f.exec {
			mode = 0o700
		}
		fd, err := unix.Openat(dfd, f.rel[len(f.rel)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		_ = unix.Close(dfd)
		if errors.Is(err, unix.EEXIST) {
			skip(path.Join(f.rel...), "already in the home; kept")
			continue
		}
		if err != nil {
			return err
		}
		fh := os.NewFile(uintptr(fd), f.rel[len(f.rel)-1]) // #nosec G115 -- a descriptor
		_, werr := fh.Write(f.data)
		serr := fh.Sync()
		cerr := fh.Close()
		if err := errors.Join(werr, serr, cerr); err != nil {
			return err
		}
		res.Copied = append(res.Copied, path.Join(f.rel...))
	}
	// The marker last: a start stopped before it seeds again, keeping what
	// it already copied.
	mfd, err := unix.Openat(hfd, seedMarker, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	if err == nil {
		mf := os.NewFile(uintptr(mfd), seedMarker) // #nosec G115 -- a descriptor
		_, werr := mf.WriteString(strings.Join(res.Copied, "\n") + "\n")
		cerr := mf.Close()
		if err := errors.Join(werr, cerr); err != nil {
			return err
		}
	}
	return json.NewEncoder(w).Encode(res)
}
