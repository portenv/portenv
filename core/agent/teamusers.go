// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Agent users (ADR 0022 §1): each team agent is a user of the workspace,
// named by its id, with a uid from a fixed range and a private home. Only
// init (root) makes or removes them, when the helper asks over init's own
// socket; users.json in /home brings them back at every box start.
const (
	// TeamGroup holds work and every agent: /home/team and the project's
	// shared files (§2).
	TeamGroup = "team"
	TeamGID   = 2000
	// AgentUIDFirst..AgentUIDLast are the agents' uids, inside ADR 0010's
	// lane range; eight is the team's ceiling (§7).
	AgentUIDFirst = 2001
	AgentUIDLast  = 2008
)

var (
	// ErrInvalidAgentID means an id isn't [a-z][a-z0-9-]{0,31}.
	ErrInvalidAgentID = errors.New("isn't a valid agent id")
	// ErrReservedAgentID means the name belongs to the box already.
	ErrReservedAgentID = errors.New("is a name this workspace already uses")
	// ErrTeamFull means every agent uid is taken.
	ErrTeamFull = errors.New("the team is full")
	// ErrNoAgent means no agent user has that id.
	ErrNoAgent = errors.New("no agent has that id")
)

var agentIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// CheckAgentID checks an id's form and the names no agent may take. The
// box's own users and groups are checked by Ensure, which can ask the box.
func CheckAgentID(id, mainUser string) error {
	if !agentIDRE.MatchString(id) {
		return fmt.Errorf("%q %w: use a lowercase letter, then up to 31 lowercase letters, digits or hyphens", id, ErrInvalidAgentID)
	}
	if id == TeamGroup || id == mainUser || strings.HasPrefix(id, "portenv") {
		return fmt.Errorf("%q %w", id, ErrReservedAgentID)
	}
	return nil
}

// AgentUser is an agent's user, as Ensure leaves it.
type AgentUser struct {
	UID  int
	Home string
	// Created is true when Ensure made the user now.
	Created bool
	// RunDir, TmuxDir and TmpDir are the agent's run folder and the two
	// folders in it that are its own (§1), made by Ensure as root. Empty
	// where TeamUsers has no RunDir.
	RunDir, TmuxDir, TmpDir string
}

// TeamUsers makes, removes and restores agent users. Its methods are safe
// for concurrent use; each holds one lock for its whole run.
type TeamUsers struct {
	Cfg Config
	Run Runner
	// Chown sets a path's owner without following a symlink (os.Lchown).
	Chown func(path string, uid, gid int) error
	// RunDir is /run/portenv-team: the agents' run folders (§1).
	RunDir string
	// fchown sets an open folder's owner (f.Chown; a recorder in tests,
	// which don't run as root).
	fchown func(f *os.File, uid, gid int) error

	mu         sync.Mutex
	restoreErr error
	// homeFixes are what box starts fixed in the owner's home, for the
	// next team start's notice (R-0262); groupOf reads a path's group (a
	// fake in tests).
	homeFixes []string
	groupOf   func(string) int
	// migrate is the move to agents' own clones' file work as root and
	// its runs as agents (nil: defaultMigrateOps; a fake in tests).
	migrate *migrateOps
	// onMove sees what started a move (tests).
	onMove func(ProjectFindings)
	// teamDirACL sets /home/team's default ACL (nil: setTeamDirACL; a
	// recorder in tests).
	teamDirACL func(dir string, workUID int) error
}

type agentUserRecord struct {
	ID  string `json:"id"`
	UID int    `json:"uid"`
	// Removed: no longer a user, but its home and uid are kept until the
	// owner deletes its files.
	Removed bool `json:"removed,omitempty"`
}

type teamUsersFile struct {
	Version int               `json:"version"`
	Users   []agentUserRecord `json:"users"`
	// Projects are the project folders a team has opened (R-0262: the
	// home is re-checked around them at every box start).
	Projects []string `json:"projects,omitempty"`
}

func (t *TeamUsers) storePath() string {
	return filepath.Join(t.Cfg.HomeRoot, ".portenv", "team", "users.json")
}

func (t *TeamUsers) home(id string) string { return filepath.Join(t.Cfg.HomeRoot, id) }

// RestoreErr is what the last Restore couldn't do, or nil.
func (t *TeamUsers) RestoreErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.restoreErr
}

// Ensure makes the agent's user, group and home if they're missing, and
// returns them. An id already recorded gets its own uid back, removed or
// not.
func (t *TeamUsers) Ensure(ctx context.Context, id string) (AgentUser, error) {
	if err := CheckAgentID(id, t.Cfg.User); err != nil {
		return AgentUser{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := checkRealDir(t.Cfg.HomeRoot); err != nil {
		return AgentUser{}, err
	}
	if t.restoreErr != nil {
		return AgentUser{}, fmt.Errorf("agent users weren't restored when the workspace started: %w", t.restoreErr)
	}
	store, err := t.load()
	if err != nil {
		return AgentUser{}, err
	}
	if err := t.ensureTeamGroup(ctx); err != nil {
		return AgentUser{}, err
	}
	if err := t.ensureTeamDir(); err != nil {
		return AgentUser{}, err
	}
	rec := store.find(id)
	created := false
	switch {
	case rec == nil:
		if t.exists(ctx, "passwd", id) || t.exists(ctx, "group", id) {
			return AgentUser{}, fmt.Errorf("%q %w", id, ErrReservedAgentID)
		}
		uid, err := t.freeUID(ctx, store)
		if err != nil {
			return AgentUser{}, err
		}
		store.Users = append(store.Users, agentUserRecord{ID: id, UID: uid})
		rec = &store.Users[len(store.Users)-1]
		created = true
		// The uid is recorded before anything uses it, so a crash can't
		// give it to two agents.
		if err := t.save(store); err != nil {
			return AgentUser{}, err
		}
	case rec.Removed:
		rec.Removed = false
		if err := t.save(store); err != nil {
			return AgentUser{}, err
		}
	}
	if err := t.ensureLines(ctx, *rec); err != nil {
		return AgentUser{}, err
	}
	if err := t.ensureHome(*rec); err != nil {
		return AgentUser{}, err
	}
	u := AgentUser{UID: rec.UID, Home: t.home(id), Created: created}
	if t.RunDir != "" {
		// Again at every start: /run is lost when the box stops.
		if u.RunDir, err = t.ensureRunDir(*rec); err != nil {
			return AgentUser{}, err
		}
		u.TmuxDir, u.TmpDir = filepath.Join(u.RunDir, "tmux"), filepath.Join(u.RunDir, "tmp")
	}
	return u, nil
}

// Remove takes the agent's user and group out. Its home and uid stay,
// recorded as removed, unless deleteFiles: only after the owner approved
// deleting them (ADR 0022 §1) are they deleted and the uid freed.
func (t *TeamUsers) Remove(ctx context.Context, id string, deleteFiles bool) error {
	if err := CheckAgentID(id, t.Cfg.User); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	store, err := t.load()
	if err != nil {
		return err
	}
	rec := store.find(id)
	if rec == nil {
		return fmt.Errorf("%q: %w", id, ErrNoAgent)
	}
	if t.exists(ctx, "passwd", id) {
		if out, err := t.Run.Run(ctx, nil, "userdel", "--", id); err != nil {
			return cmdError("remove user "+id+" (stop it first)", out, err)
		}
	}
	if t.exists(ctx, "group", id) {
		if out, err := t.Run.Run(ctx, nil, "groupdel", "--", id); err != nil {
			return cmdError("remove group "+id, out, err)
		}
	}
	if !rec.Removed {
		rec.Removed = true
		if err := t.save(store); err != nil {
			return err
		}
	}
	if !deleteFiles {
		return nil
	}
	if err := t.deleteHome(id); err != nil {
		return err
	}
	store.drop(id)
	return t.save(store)
}

// Restore runs at every box start, after work's user and home: the socket
// folder and, for a workspace that has a team, the team group, /home/team
// and every recorded agent's user lines and home. It never stops the box; what fails is kept for the next
// Ensure to report.
func (t *TeamUsers) Restore(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.restoreErr = t.restore(ctx)
}

func (t *TeamUsers) restore(ctx context.Context) error {
	if err := checkRealDir(t.Cfg.HomeRoot); err != nil {
		return err
	}
	store, err := t.load()
	if err != nil {
		return err
	}
	if t.RunDir != "" {
		if err := t.ensureRunRoot(); err != nil {
			return err
		}
		// work's own socket's folder, for pushes (R-0391), team or not.
		if _, err := t.ensureOwnerRunDir(t.Cfg.UID); err != nil {
			return err
		}
	}
	// A workspace without a team gets nothing in /home: no team folder
	// until its first agent.
	if len(store.Users) == 0 {
		return nil
	}
	if err := t.ensureTeamGroup(ctx); err != nil {
		return err
	}
	if err := t.ensureTeamDir(); err != nil {
		return err
	}
	// The owner's home, again, at every box start with a team (R-0262).
	work := filepath.Join(t.Cfg.HomeRoot, t.Cfg.User)
	if fixed, err := t.tightenHome(work, store.Projects); err != nil {
		return err
	} else if len(fixed) > 0 {
		t.homeFixes = append(t.homeFixes, fixed...)
	}
	var errs []error
	for _, rec := range store.Users {
		if rec.Removed {
			continue
		}
		if err := t.ensureLines(ctx, rec); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := t.ensureHome(rec); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// load reads users.json. A missing file is an empty team; an unreadable
// one is an error, and is never written over.
func (t *TeamUsers) load() (*teamUsersFile, error) {
	b, err := os.ReadFile(t.storePath())
	if errors.Is(err, fs.ErrNotExist) {
		return &teamUsersFile{Version: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent users: %w", err)
	}
	var f teamUsersFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("read agent users %s: %w", t.storePath(), err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("read agent users %s: version %d, want 1", t.storePath(), f.Version)
	}
	seen := map[int]bool{}
	for _, r := range f.Users {
		if CheckAgentID(r.ID, t.Cfg.User) != nil || r.UID < AgentUIDFirst || r.UID > AgentUIDLast || seen[r.UID] {
			return nil, fmt.Errorf("read agent users %s: bad entry %q (uid %d)", t.storePath(), r.ID, r.UID)
		}
		seen[r.UID] = true
	}
	return &f, nil
}

// save writes users.json atomically: a new file, synced, renamed over the
// old one, and the folder synced.
func (t *TeamUsers) save(f *teamUsersFile) error {
	path := t.storePath()
	dir := filepath.Dir(path)
	if err := t.realDirs(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "users.json.tmp*")
	if err != nil {
		return fmt.Errorf("save agent users: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("save agent users: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("save agent users: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("save agent users: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save agent users: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save agent users: %w", err)
	}
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- the store's own folder
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (f *teamUsersFile) find(id string) *agentUserRecord {
	for i := range f.Users {
		if f.Users[i].ID == id {
			return &f.Users[i]
		}
	}
	return nil
}

func (f *teamUsersFile) drop(id string) {
	out := f.Users[:0]
	for _, r := range f.Users {
		if r.ID != id {
			out = append(out, r)
		}
	}
	f.Users = out
}

// exists asks the box whether a user or group name (or number) exists.
func (t *TeamUsers) exists(ctx context.Context, db, key string) bool {
	_, err := t.Run.Run(ctx, nil, "getent", db, key)
	return err == nil
}

// freeUID is the lowest agent uid no record holds (removed ones included)
// and no user or group in the box uses.
func (t *TeamUsers) freeUID(ctx context.Context, f *teamUsersFile) (int, error) {
	taken := map[int]bool{}
	for _, r := range f.Users {
		taken[r.UID] = true
	}
	for uid := AgentUIDFirst; uid <= AgentUIDLast; uid++ {
		n := strconv.Itoa(uid)
		if !taken[uid] && !t.exists(ctx, "passwd", n) && !t.exists(ctx, "group", n) {
			return uid, nil
		}
	}
	return 0, fmt.Errorf("%w: %d agents. Remove one and delete its files first", ErrTeamFull, AgentUIDLast-AgentUIDFirst+1)
}

func (t *TeamUsers) ensureTeamGroup(ctx context.Context) error {
	// A team group with another gid is never used, and gid 2000 held by
	// another group is never taken over (R-0260 item 7).
	if out, err := t.Run.Run(ctx, nil, "getent", "group", TeamGroup); err == nil {
		if f := strings.Split(strings.TrimSpace(string(out)), ":"); len(f) < 3 || f[2] != strconv.Itoa(TeamGID) {
			gid := "?"
			if len(f) >= 3 {
				gid = f[2]
			}
			return fmt.Errorf("this workspace's group %s has gid %s, not %d, so Portenv won't use it for the team. Remove or renumber it, then start the team again", TeamGroup, gid, TeamGID)
		}
	} else {
		if out, err := t.Run.Run(ctx, nil, "getent", "group", strconv.Itoa(TeamGID)); err == nil {
			return fmt.Errorf("gid %d belongs to another group (%s), so Portenv can't make the team's group", TeamGID, strings.SplitN(strings.TrimSpace(string(out)), ":", 2)[0])
		}
		if out, err := t.Run.Run(ctx, nil, "groupadd", "--gid", strconv.Itoa(TeamGID), TeamGroup); err != nil {
			return cmdError("create group "+TeamGroup, out, err)
		}
	}
	out, err := t.Run.Run(ctx, nil, "id", "-nG", t.Cfg.User)
	if err != nil {
		return cmdError("read "+t.Cfg.User+"'s groups", out, err)
	}
	for _, g := range strings.Fields(string(out)) {
		if g == TeamGroup {
			return nil
		}
	}
	if out, err := t.Run.Run(ctx, nil, "usermod", "-a", "-G", TeamGroup, t.Cfg.User); err != nil {
		return cmdError("add "+t.Cfg.User+" to "+TeamGroup, out, err)
	}
	return nil
}

// ensureLines adds the agent's group and user if the box lacks them, and
// refuses one whose uid isn't the recorded one.
func (t *TeamUsers) ensureLines(ctx context.Context, rec agentUserRecord) error {
	uid := strconv.Itoa(rec.UID)
	if out, err := t.Run.Run(ctx, nil, "id", "-u", rec.ID); err == nil {
		if got := strings.TrimSpace(string(out)); got != uid {
			return fmt.Errorf("user %s has uid %s, but its files belong to %s; it was changed outside Portenv", rec.ID, got, uid)
		}
		// Its own group first, and the team group (R-0260 item 8).
		out, err := t.Run.Run(ctx, nil, "id", "-g", rec.ID)
		if err != nil {
			return cmdError("read "+rec.ID+"'s group", out, err)
		}
		if got := strings.TrimSpace(string(out)); got != uid {
			return fmt.Errorf("user %s's group is %s, not its own %s; it was changed outside Portenv", rec.ID, got, uid)
		}
		out, err = t.Run.Run(ctx, nil, "id", "-nG", rec.ID)
		if err != nil {
			return cmdError("read "+rec.ID+"'s groups", out, err)
		}
		if !slices.Contains(strings.Fields(string(out)), TeamGroup) {
			if out, err := t.Run.Run(ctx, nil, "usermod", "-a", "-G", TeamGroup, rec.ID); err != nil {
				return cmdError("add "+rec.ID+" to "+TeamGroup, out, err)
			}
		}
		return nil
	}
	if !t.exists(ctx, "group", rec.ID) {
		if out, err := t.Run.Run(ctx, nil, "groupadd", "--gid", uid, rec.ID); err != nil {
			return cmdError("create group "+rec.ID, out, err)
		}
	}
	if out, err := t.Run.Run(ctx, nil, "useradd", "--uid", uid, "--gid", uid, "--groups", TeamGroup,
		"--no-create-home", "--home-dir", t.home(rec.ID), "--shell", "/bin/bash", rec.ID); err != nil {
		return cmdError("create user "+rec.ID, out, err)
	}
	return nil
}

// ensureHome makes the agent's home (0700, its own) if it's missing. An
// existing one keeps its files; only the folder itself is checked, and a
// symlink or file in its place is refused.
func (t *TeamUsers) ensureHome(rec agentUserRecord) error {
	home := t.home(rec.ID)
	info, err := os.Lstat(home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(home, 0o700); err != nil {
			return fmt.Errorf("make %s's home: %w", rec.ID, err)
		}
	case err != nil:
		return fmt.Errorf("check %s's home: %w", rec.ID, err)
	case !info.IsDir():
		return fmt.Errorf("%s isn't a folder; %s's home must be one", home, rec.ID)
	}
	if err := t.Chown(home, rec.UID, rec.UID); err != nil {
		return fmt.Errorf("give %s its home: %w", rec.ID, err)
	}
	return os.Chmod(home, 0o700) // #nosec G302 -- a directory: owner-only
}

// ensureTeamDir makes /home/team: work's, group team, setgid, 0770, with
// a default ACL giving the team and work read and write.
func (t *TeamUsers) ensureTeamDir() error {
	dir := filepath.Join(t.Cfg.HomeRoot, TeamGroup)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o770); err != nil { // #nosec G301 -- the team's shared folder: group team writes it (ADR 0022 §2)
			return fmt.Errorf("make %s: %w", dir, err)
		}
	case err != nil:
		return fmt.Errorf("check %s: %w", dir, err)
	case !info.IsDir():
		return fmt.Errorf("%s isn't a folder", dir)
	}
	if err := t.Chown(dir, t.Cfg.UID, TeamGID); err != nil {
		return fmt.Errorf("set %s's owner: %w", dir, err)
	}
	if err := os.Chmod(dir, os.ModeSetgid|0o770); err != nil { // #nosec G302 -- the team's shared folder
		return err
	}
	// Its default ACL (ADR 0022 §2), which it never had (CC-0706 item 19):
	// what's made in it is the team's and work's, whatever the umask.
	// init's watcher shares what a default ACL can't reach (teamshare.go).
	set := t.teamDirACL
	if set == nil {
		set = setTeamDirACL
	}
	return set(dir, t.Cfg.UID)
}

// deleteHome deletes exactly HomeRoot/id, never through a symlink.
func (t *TeamUsers) deleteHome(id string) error {
	home := t.home(id)
	if filepath.Dir(home) != filepath.Clean(t.Cfg.HomeRoot) || filepath.Base(home) != id {
		return fmt.Errorf("refusing to delete %s", home)
	}
	info, err := os.Lstat(home)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check %s's home: %w", id, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s isn't a folder; not deleting it", home)
	}
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("delete %s's files: %w", id, err)
	}
	return nil
}

// TeamRunDir holds each agent's run folder (ADR 0022 §1).
const TeamRunDir = "/run/portenv-team"

// ensureRunRoot makes /run/portenv-team: root's, 0711. Everyone may pass
// through to their own folder; no one but root lists or writes it.
func (t *TeamUsers) ensureRunRoot() error {
	if err := os.MkdirAll(filepath.Dir(t.RunDir), 0o755); err != nil { // #nosec G301 -- /run
		return fmt.Errorf("make %s: %w", filepath.Dir(t.RunDir), err)
	}
	return secureDir(t.RunDir, 0o711)
}

// ensureRunDir makes the agent's run folder, as root, and returns it. The
// helper (uid 991, without cap_dac_override) can't make folders in
// /run/portenv-team, and never gives a file to another uid:
//
//	<RunDir>/<id>/       the helper's, the agent's group, setgid, 0710
//	<RunDir>/<id>/tmux/  the agent's, 0700: its tmux server's socket
//	<RunDir>/<id>/tmp/   the agent's, 0700: its TMPDIR
//
// The helper listens on <id>/portenv.sock; the setgid bit gives the socket
// the agent's group as it's made, so the helper needs no chown. Only the
// helper writes <id>, so the agent can't replace its socket or folders;
// another agent isn't in the group and can't pass through. Each folder is
// checked and set through a descriptor opened without following a link.
func (t *TeamUsers) ensureRunDir(rec agentUserRecord) (string, error) {
	if err := t.ensureRunRoot(); err != nil {
		return "", err
	}
	fchown := t.fchown
	if fchown == nil {
		fchown = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
	}
	base := filepath.Join(t.RunDir, rec.ID)
	if err := ownedDir(base, os.ModeSetgid|0o710, func(f *os.File) error { return fchown(f, ServeUID, rec.UID) }); err != nil {
		return "", err
	}
	for _, sub := range []string{"tmux", "tmp"} {
		if err := ownedDir(filepath.Join(base, sub), 0o700, func(f *os.File) error { return fchown(f, rec.UID, rec.UID) }); err != nil {
			return "", err
		}
	}
	return base, nil
}

// realDirs makes /home/.portenv and /home/.portenv/team, root's, 0755:
// work can read the map but never write next to it (R-0260 item 9).
func (t *TeamUsers) realDirs() error {
	if err := checkRealDir(t.Cfg.HomeRoot); err != nil {
		return err
	}
	for _, d := range []string{filepath.Join(t.Cfg.HomeRoot, ".portenv"), filepath.Dir(t.storePath())} {
		if err := secureDir(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// checkRealDir refuses a path that isn't a real folder (a symlink, say).
func checkRealDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder (a link isn't followed)", dir)
	}
	return nil
}

// secureDir makes dir if it's missing, refuses a symlink in its place, and
// sets its mode through a descriptor of the folder itself (and, as root,
// gives it to root), so a link swapped in between is never changed.
func secureDir(dir string, mode os.FileMode) error {
	var chown func(*os.File) error
	if os.Geteuid() == 0 {
		chown = func(f *os.File) error { return f.Chown(0, 0) }
	}
	return ownedDir(dir, mode, chown)
}

// ownedDir is secureDir with the owner set by chown (nil: left as it is)
// through the folder's own descriptor, before its mode.
func ownedDir(dir string, mode os.FileMode, chown func(*os.File) error) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("make %s: %w", dir, err)
		}
	}
	before, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check %s: %w", dir, err)
	}
	if !before.IsDir() {
		return fmt.Errorf("%s isn't a folder (a link isn't followed)", dir)
	}
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- a fixed folder
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return fmt.Errorf("%s changed while it was checked", dir)
	}
	if chown != nil {
		if err := chown(f); err != nil {
			return fmt.Errorf("set %s's owner: %w", dir, err)
		}
	}
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("set %s's mode: %w", dir, err)
	}
	return nil
}
