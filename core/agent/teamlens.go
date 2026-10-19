// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// Read-only lenses (ADR 0022 §2a point 4): a lens whose approved file
// grants read_clones: [builder] gets a named-user ACL on builder's clone,
// u:<lens>:r-X (and the same default ACL on its folders, so new files stay
// readable), and u:<lens>:--x on builder's home, so it can pass through to
// the clone and list nothing else there. Never w, and never through group
// team, which every agent is in. The writer's own process sets them
// ("team-io lens-grant", through as-agent), at every start, to exactly the
// lenses granted now: a grant that went is taken away.

// lensOps sets one file's lens entries: readers' uids, with read (and
// search on folders and programs), or traverse only (the home).
type lensOps struct {
	set func(f *os.File, dir bool, readers []uint32, traverseOnly bool) error
}

// lensGrant is team-io lens-grant HOME NAME UIDS, run as the writer: UIDS
// (comma-separated agent uids, or "-" for none) get traverse on HOME and
// read on HOME/NAME, the writer's clone, and every other agent uid's entry
// goes. It walks the clone through descriptors, never following a link,
// and changes only what the writer owns.
func lensGrant(home, name, uids string, ops lensOps) error {
	switch {
	case !filepath.IsAbs(home) || filepath.Clean(home) != home:
		return fmt.Errorf("%q isn't a home folder", home)
	case name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, "."):
		return fmt.Errorf("%q isn't a project name", name)
	}
	readers, err := parseReaders(uids)
	if err != nil {
		return err
	}
	if err := ownHome(home); err != nil {
		return err
	}
	if slices.Contains(readers, uint32(os.Getuid())) { // #nosec G115 -- a uid
		return errors.New("an agent doesn't grant itself")
	}
	top, err := os.OpenFile(home, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- the caller's own home
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	if err := ops.set(top, true, readers, true); err != nil {
		if errors.Is(err, errors.ErrUnsupported) || errors.Is(err, unix.EOPNOTSUPP) {
			return errNoACL
		}
		return fmt.Errorf("let the lenses pass through the home: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(fdOf(top), name, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil // no clone yet
	} else if err != nil {
		return err
	}
	if statType(&st) != unix.S_IFDIR || int(st.Uid) != os.Getuid() {
		return nil // not a clone of its own: nothing to open
	}
	clone, ok, err := openEntry(top, name, filepath.Join(home, name), &st)
	if err != nil || !ok {
		return err
	}
	defer func() { _ = clone.Close() }()
	if err := ops.set(clone, true, readers, false); err != nil {
		return fmt.Errorf("let the lenses read ~/%s: %w", name, err)
	}
	return lensWalk(clone, name, readers, ops)
}

func lensWalk(dir *os.File, rel string, readers []uint32, ops lensOps) error {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	for _, n := range names {
		child := path.Join(rel, n)
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(dir), n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		typ := statType(&st)
		if (typ != unix.S_IFDIR && typ != unix.S_IFREG) || int(st.Uid) != os.Getuid() {
			continue // links, special files and what isn't its own stay as they are
		}
		f, ok, err := openEntry(dir, n, filepath.Join(dir.Name(), n), &st)
		if errors.Is(err, unix.EACCES) {
			continue // its own file it can't read (0000): the lens can't either
		}
		if err != nil {
			return fmt.Errorf("open %s: %w", child, err)
		}
		if !ok {
			continue
		}
		err = ops.set(f, typ == unix.S_IFDIR, readers, false)
		if err == nil && typ == unix.S_IFDIR {
			err = lensWalk(f, child, readers, ops)
		}
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", child, err)
		}
	}
	return nil
}

func parseReaders(s string) ([]uint32, error) {
	if s == "-" {
		return nil, nil
	}
	var out []uint32
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(p)
		if err != nil || n < AgentUIDFirst || n > AgentUIDLast {
			return nil, fmt.Errorf("%q isn't an agent's uid", p)
		}
		out = append(out, uint32(n)) // #nosec G115 -- an agent's uid, checked above
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// applyLensGrants makes each agent's clone readable by exactly the lenses
// granted on it now (ADR 0022 §2a point 4), as that agent. Every agent
// user of the workspace is visited, so a grant that went, or a writer
// missing from readers, ends with no lens. Readers not yet users are left
// out until their own start.
func (s *apiServer) applyLensGrants(ctx context.Context, projectDir string, readers []*agentv1.CloneReaders) error {
	name, err := s.projectName(projectDir)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	t := &TeamUsers{Cfg: s.cfg}
	f, err := t.load()
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	uidOf := map[string]int{}
	for _, u := range f.Users {
		if !u.Removed {
			uidOf[u.ID] = u.UID
		}
	}
	granted := map[string][]string{}
	for _, cr := range readers {
		if !agentIDRE.MatchString(cr.GetWriter()) {
			return status.Errorf(codes.InvalidArgument, "%q isn't an agent id", cr.GetWriter())
		}
		granted[cr.GetWriter()] = cr.GetReaders()
	}
	for _, u := range f.Users {
		if u.Removed {
			continue
		}
		var uids []string
		for _, r := range granted[u.ID] {
			if uid, ok := uidOf[r]; ok && r != u.ID {
				uids = append(uids, strconv.Itoa(uid))
			}
		}
		arg := "-"
		if len(uids) > 0 {
			arg = strings.Join(uids, ",")
		}
		if _, err := s.teamIOAsAgent(ctx, u.UID, t.home(u.ID), []string{"lens-grant", t.home(u.ID), name, arg}, nil); err != nil {
			return status.Errorf(codes.FailedPrecondition, "%s's copy of the project for its lenses: %v", u.ID, err)
		}
	}
	return nil
}
