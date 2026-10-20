// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The move to agents' own clones (ADR 0022 §2a point 6, R-0315): a
// workspace whose team worked in the owner's project (group team, setgid
// folders, g:team:rwX, core.sharedRepository, Portenv's system
// safe.directory entries) is moved at its next team start, by init (root),
// before any agent process runs. This is a data-loss path: nothing is
// deleted here, only recorded; the owner keeps or removes each recorded
// item in the team-start decision (ResolveMigration).
//
//  1. Every agent process is stopped: init refuses while any process has
//     an agent's uid.
//  2. It records what git reads for work that an agent could have changed
//     (config files, hooks, worktree registrations in .git that aren't
//     work's or that another uid could write), and every repository,
//     worktree or file an agent made in the project. A link in .git work
//     didn't make stops the move, named.
//  3. Each agent gets its clone (team-io clone-own, as the agent), and the
//     files it made in the project or its old worktree are copied, as the
//     agent, into the clone: never over a file already there.
//  4. .git becomes work's alone: owner work, no ACLs, no group or other
//     write. core.sharedRepository and Portenv's safe.directory entries go.
//  5. The rest of the project becomes work's the same way, except the
//     recorded repositories and worktrees, left as they are (work's git
//     refuses them by ownership) until the owner decides.
//
// Every walk goes through descriptors and never follows a link.

// migrationFile is the record while the owner hasn't answered: root's, in
// the team's state folder.
func (t *TeamUsers) migrationFile() string {
	return filepath.Join(t.Cfg.HomeRoot, ".portenv", "team", "migration.json")
}

// migrationRecord is what the move recorded, for the owner's answer.
type migrationRecord struct {
	Project string           `json:"project"`
	Items   []*migrationItem `json:"items"`
}

type migrationItem struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Agent  string `json:"agent,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Choice bool   `json:"choice,omitempty"`
	Note   string `json:"note,omitempty"`
	// Registration is a worktree's .git/worktrees/<name>, removed with it.
	Registration string `json:"registration,omitempty"`
	uid          int
}

func (i *migrationItem) proto() *typesv1.MigrationItem {
	return &typesv1.MigrationItem{Id: i.ID, Path: showPath(i.Path), Kind: i.Kind, Agent: i.Agent, Size: i.Size, Choice: i.Choice, Note: showPath(i.Note)}
}

// showPath is a path as the owner's decision shows it: an agent can name a
// file with any bytes, so one with a control or bidi character, or not
// UTF-8, is shown quoted.
func showPath(p string) string {
	if plainText(p, false) {
		return p
	}
	return strconv.QuoteToASCII(p)
}

// maxMigrationItems is the most the owner is asked to decide on at once.
const maxMigrationItems = 200

// PrepareOptions are what the move needs from portenvd: the agents that
// get their own clone, and their home quota.
type PrepareOptions struct {
	CloneAgents []string
	HomeQuota   int64
	// Names are the agents' display names, by id, for their clones' git
	// identity.
	Names map[string]string
}

// migrateOps are the move's looks and changes as root, and its runs as an
// agent (fakes in tests, which don't run as root).
type migrateOps struct {
	owner func(rel string, st *unix.Stat_t) int // nil: the file's uid
	group func(rel string, st *unix.Stat_t) int // nil: the file's gid
	// othersWrite is whether a uid other than work could write the file.
	othersWrite func(f *os.File, st *unix.Stat_t, dir bool) (bool, error)
	// reown makes an open file work's alone: owner and group work, no
	// ACLs, no group or other write.
	reown func(f *os.File, dir bool) error
	// reownLink makes a link in dir work's, without following it.
	reownLink func(dir *os.File, name string) error
	asAgent   func(ctx context.Context, uid int, home string, args []string, stdin io.Reader) ([]byte, error)
	// running are the agent uids with a process.
	running func() ([]int, error)
}

func (o *migrateOps) uidOf(rel string, st *unix.Stat_t) int {
	if o.owner != nil {
		return o.owner(rel, st)
	}
	return int(st.Uid)
}

func (o *migrateOps) gidOf(rel string, st *unix.Stat_t) int {
	if o.group != nil {
		return o.group(rel, st)
	}
	return int(st.Gid)
}

func (t *TeamUsers) mops() *migrateOps {
	if t.migrate != nil {
		return t.migrate
	}
	return defaultMigrateOps(t.Cfg)
}

// errAgentsRunning is the plain line while an agent process runs.
var errAgentsRunning = errors.New("this workspace's team moves to its own copies of the project before it starts again, which needs every agent stopped. Choose Stop Team, then Start")

// checkStopped refuses while any agent process runs.
func (o *migrateOps) checkStopped() error {
	uids, err := o.running()
	if err != nil {
		return fmt.Errorf("check that every agent is stopped: %w", err)
	}
	if len(uids) > 0 {
		return errAgentsRunning
	}
	return nil
}

// loadMigration is the record still waiting for the owner, for this
// project, or nil.
func (t *TeamUsers) loadMigration(project string) (*migrationRecord, error) {
	b, err := os.ReadFile(t.migrationFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r migrationRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("read the move's record: %w", err)
	}
	if r.Project != project {
		return nil, nil
	}
	return &r, nil
}

func (t *TeamUsers) saveMigration(r *migrationRecord) error {
	if err := t.realDirs(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(t.migrationFile(), append(b, '\n'), 0o644)
}

// writeAtomic writes a file by a new file, synced and renamed over it.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- the file's own folder
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func protoItems(items []*migrationItem) []*typesv1.MigrationItem {
	out := make([]*typesv1.MigrationItem, 0, len(items))
	for _, i := range items {
		out = append(out, i.proto())
	}
	return out
}

// migrateLocked moves the team to its own clones; t.mu is held.
func (t *TeamUsers) migrateLocked(ctx context.Context, project string, found ProjectFindings, opt PrepareOptions) ([]*typesv1.MigrationItem, error) {
	ops := t.mops()
	if t.onMove != nil {
		t.onMove(found)
	}
	if err := ops.checkStopped(); err != nil {
		return nil, err
	}
	users, err := t.load()
	if err != nil {
		return nil, err
	}
	agentOf := map[int]string{}
	for _, u := range users.Users {
		agentOf[u.UID] = u.ID
	}
	rec, err := recordMigration(project, t.Cfg.UID, agentOf, ops)
	if err != nil {
		return nil, err
	}
	for _, f := range found.Items {
		if f.What == "shared-repository" || f.What == "safe-directory" {
			rec.Items = append(rec.Items, &migrationItem{Path: f.String(), Kind: "setting", Note: "Portenv removed it."})
		}
	}
	if len(rec.Items) > maxMigrationItems {
		return nil, fmt.Errorf("agents made or changed %d files, folders and settings in your project; Portenv asks you about at most %d at once. Move some of the agents' files out of the project, then start the team again", len(rec.Items), maxMigrationItems)
	}
	for n, i := range rec.Items {
		i.ID = "m" + strconv.Itoa(n+1)
	}
	if err := t.cloneAndCopy(ctx, project, rec, users, opt, ops); err != nil {
		return nil, err
	}
	if err := reownTree(project, ".git", nil, true, t.Cfg.UID, ops); err != nil {
		return nil, fmt.Errorf("give .git back to you: %w", err)
	}
	cfg := filepath.Join(project, ".git", "config")
	if fi, err := os.Lstat(cfg); err == nil && fi.Mode().IsRegular() {
		// As work, on that one file: git config --file reads no other
		// config and runs nothing. Exit 5: it wasn't set.
		out, err := t.Run.Run(ctx, []string{"GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent"}, "runuser", "-u", t.Cfg.User, "--", agentGitPath, "config", "--file", cfg, "--unset-all", "core.sharedRepository")
		switch {
		case err == nil:
			if !slices.ContainsFunc(rec.Items, func(i *migrationItem) bool {
				return i.Kind == "setting" && strings.Contains(i.Path, "sharedRepository")
			}) {
				rec.Items = append(rec.Items, &migrationItem{ID: "m" + strconv.Itoa(len(rec.Items)+1), Path: ProjectFinding{What: "shared-repository"}.String(), Kind: "setting", Note: "Portenv removed it."})
			}
		case !strings.Contains(err.Error(), "exit status 5"):
			return nil, cmdError("take core.sharedRepository out of .git/config", out, err)
		}
	}
	for _, f := range found.Items {
		if f.What != "safe-directory" {
			continue
		}
		if out, err := t.Run.Run(ctx, nil, "git", "config", "--system", "--fixed-value", "--unset-all", "safe.directory", f.Path); err != nil && !strings.Contains(err.Error(), "exit status 5") {
			return nil, cmdError("take Portenv's safe.directory for "+f.Path+" out of the system git config", out, err)
		}
	}
	var keep []string
	for _, i := range rec.Items {
		if i.Kind == "repo" || i.Kind == "worktree" {
			keep = append(keep, i.Path)
		}
	}
	if err := reownTree(project, ".", append(keep, ".git"), false, t.Cfg.UID, ops); err != nil {
		return nil, fmt.Errorf("make the project yours again: %w", err)
	}
	if err := t.saveMigration(rec); err != nil {
		return nil, err
	}
	return protoItems(rec.Items), nil
}

// gitExecPath is whether a path in .git (relative to it) is one git reads
// to decide what to run: its config, its hooks (not the samples), a
// submodule's config and hooks.
func gitExecPath(rel string) bool {
	parts := strings.Split(rel, "/")
	for len(parts) >= 3 && parts[0] == "modules" {
		parts = parts[2:] // modules/<name>/… is a repository of its own
	}
	switch {
	case len(parts) == 1:
		return parts[0] == "config" || parts[0] == "config.worktree"
	case len(parts) == 2 && parts[0] == "hooks":
		return !strings.HasSuffix(parts[1], ".sample")
	}
	return false
}

// worktreeReg is one of .git/worktrees/<name>: where its worktree is, and
// its branch.
type worktreeReg struct {
	name, dir, branch string
	changed           bool // anything in it not work's, or writable by another uid
	uid               int  // not work's: whose
}

// recordMigration walks the project as root and records the items: .git
// first (its worktree registrations), then the rest.
func recordMigration(project string, work int, agentOf map[int]string, ops *migrateOps) (*migrationRecord, error) {
	rec := &migrationRecord{Project: project}
	top, err := os.OpenFile(project, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0) // #nosec G304 -- init checked it
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	var regs []*worktreeReg
	var st unix.Stat_t
	if err := unix.Fstatat(fdOf(top), ".git", &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && statType(&st) == unix.S_IFDIR {
		g, ok, err := openEntry(top, ".git", filepath.Join(project, ".git"), &st)
		if err != nil || !ok {
			return nil, fmt.Errorf("open .git: %w", changedErr(err))
		}
		regs, err = recordGit(g, ".git", "", project, work, agentOf, ops, rec)
		_ = g.Close()
		if err != nil {
			return nil, err
		}
	}
	byDir := map[string]*worktreeReg{}
	for _, r := range regs {
		if r.dir != "" {
			byDir[r.dir] = r
		}
	}
	if err := recordTree(top, ".", true, project, work, agentOf, byDir, ops, rec); err != nil {
		return nil, err
	}
	// Registrations whose worktree isn't in the project: the registration
	// itself, if anything in it changed.
	for _, r := range regs {
		if r.dir == "" && r.changed {
			rec.Items = append(rec.Items, &migrationItem{Path: path.Join(".git/worktrees", r.name), Kind: "git", Agent: agentOf[r.uid], Choice: true, uid: r.uid})
		}
	}
	for _, i := range rec.Items {
		if i.Kind != "git" {
			i.Size = treeSize(filepath.Join(project, i.Path))
		}
	}
	return rec, nil
}

// recordGit walks a .git folder (rel in the project; in is the path inside
// .git) and records what git reads to decide what to run, when it isn't
// work's or another uid could write it.
func recordGit(dir *os.File, rel, in, project string, work int, agentOf map[int]string, ops *migrateOps, rec *migrationRecord) ([]*worktreeReg, error) {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	var regs []*worktreeReg
	for _, n := range names {
		child, inChild := path.Join(rel, n), path.Join(in, n)
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(dir), n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return nil, err
		}
		uid, typ := ops.uidOf(child, &st), statType(&st)
		if typ == unix.S_IFLNK {
			if uid != work {
				return nil, fmt.Errorf("%s is a link that you didn't make (uid %d's). Look at it, remove it or make it yours, then start the team again", child, uid)
			}
			continue
		}
		if typ != unix.S_IFDIR && typ != unix.S_IFREG {
			continue
		}
		f, ok, err := openEntry(dir, n, filepath.Join(dir.Name(), n), &st)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s changed while Portenv recorded it; try again", child)
		}
		changed := uid != work
		if !changed {
			changed, err = ops.othersWrite(f, &st, typ == unix.S_IFDIR)
		}
		if err == nil && in == "worktrees" && typ == unix.S_IFDIR {
			var r *worktreeReg
			r, err = readWorktreeReg(f, n, child, project, work, uid, changed, ops)
			regs = append(regs, r)
		} else if err == nil {
			if changed && gitExecPath(inChild) {
				rec.Items = append(rec.Items, &migrationItem{Path: child, Kind: "git", Agent: agentOf[uid], Choice: true, uid: uid})
			}
			if typ == unix.S_IFDIR {
				var sub []*worktreeReg
				sub, err = recordGit(f, child, inChild, project, work, agentOf, ops, rec)
				regs = append(regs, sub...)
			}
		}
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	return regs, nil
}

// readWorktreeReg reads .git/worktrees/<name>: its gitdir (where the
// worktree is) and HEAD, as plain files, and whether anything in it isn't
// work's or another uid could write.
func readWorktreeReg(dir *os.File, name, rel, project string, work, uid int, changed bool, ops *migrateOps) (*worktreeReg, error) {
	r := &worktreeReg{name: name, changed: changed}
	if uid != work {
		r.uid = uid
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(dir), n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		u := ops.uidOf(path.Join(rel, n), &st)
		if statType(&st) == unix.S_IFLNK && u != work {
			return nil, fmt.Errorf("%s is a link that you didn't make (uid %d's). Look at it, remove it or make it yours, then start the team again", path.Join(rel, n), u)
		}
		if u != work {
			r.changed, r.uid = true, u
		} else if statType(&st) == unix.S_IFREG || statType(&st) == unix.S_IFDIR {
			if f, ok, err := openEntry(dir, n, filepath.Join(dir.Name(), n), &st); err == nil && ok {
				w, _ := ops.othersWrite(f, &st, statType(&st) == unix.S_IFDIR)
				r.changed = r.changed || w
				_ = f.Close()
			}
		}
	}
	if b, err := readNoFollow(dir.Name(), "gitdir", 4096); err == nil {
		gd := strings.TrimSpace(string(b))
		if wt, ok := strings.CutPrefix(filepath.Dir(gd), project+"/"); ok && filepath.Base(gd) == ".git" && !strings.HasPrefix(wt, ".git") && path.Clean(wt) == wt && !slices.Contains(strings.Split(wt, "/"), "..") {
			r.dir = wt
		}
	}
	if b, err := readNoFollow(dir.Name(), "HEAD", 4096); err == nil {
		r.branch = strings.TrimPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
	}
	return r, nil
}

// recordTree walks the project outside .git: a folder with a .git of its
// own is a repository or a worktree, recorded when an agent made it or
// worked in it; anything else not work's is recorded with all under it.
func recordTree(dir *os.File, rel string, skipGit bool, project string, work int, agentOf map[int]string, regs map[string]*worktreeReg, ops *migrateOps, rec *migrationRecord) error {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, n := range names {
		if skipGit && n == ".git" {
			continue // the folder's own .git, looked at already
		}
		child := path.Join(rel, n)
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(dir), n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		uid, typ := ops.uidOf(child, &st), statType(&st)
		if typ == unix.S_IFDIR {
			// A repository or worktree of its own?
			var gst unix.Stat_t
			hasGit := false
			f, ok, err := openEntry(dir, n, filepath.Join(dir.Name(), n), &st)
			if err != nil || !ok {
				if uid != work {
					rec.Items = append(rec.Items, &migrationItem{Path: child, Kind: "file", Agent: agentOf[uid], Choice: true, uid: uid})
				}
				continue
			}
			if unix.Fstatat(fdOf(f), ".git", &gst, unix.AT_SYMLINK_NOFOLLOW) == nil {
				hasGit = true
			}
			if reg, isWT := regs[child]; isWT {
				who := uid
				if who == work {
					who = ops.uidOf(path.Join(child, ".git"), &gst)
				}
				agent := agentOf[who]
				if b, ok := strings.CutPrefix(reg.branch, "agent/"); ok && agent == "" && slices.Contains(mapValues(agentOf), b) {
					agent = b
				}
				if who != work || agent != "" || reg.changed {
					rec.Items = append(rec.Items, &migrationItem{Path: child, Kind: "worktree", Agent: agent, Choice: true, Registration: path.Join(".git/worktrees", reg.name), uid: who})
					_ = f.Close()
					continue
				}
			} else if hasGit && (uid != work || ops.uidOf(path.Join(child, ".git"), &gst) != work) {
				who := uid
				if who == work {
					who = ops.uidOf(path.Join(child, ".git"), &gst)
				}
				rec.Items = append(rec.Items, &migrationItem{Path: child, Kind: "repo", Agent: agentOf[who], Choice: true, uid: who})
				_ = f.Close()
				continue
			}
			if uid != work {
				rec.Items = append(rec.Items, &migrationItem{Path: child, Kind: "file", Agent: agentOf[uid], Choice: true, uid: uid})
				_ = f.Close()
				continue
			}
			if hasGit && statType(&gst) == unix.S_IFDIR {
				// work's own nested repository: its .git counts like the
				// project's.
				if g, ok, err := openEntry(f, ".git", filepath.Join(f.Name(), ".git"), &gst); err == nil && ok {
					_, err = recordGit(g, path.Join(child, ".git"), "", project, work, agentOf, ops, rec)
					_ = g.Close()
					if err != nil {
						_ = f.Close()
						return err
					}
				}
			}
			err = recordTree(f, child, hasGit, project, work, agentOf, regs, ops, rec)
			_ = f.Close()
			if err != nil {
				return err
			}
			continue
		}
		if uid != work {
			rec.Items = append(rec.Items, &migrationItem{Path: child, Kind: "file", Agent: agentOf[uid], Choice: true, uid: uid})
		}
	}
	return nil
}

func mapValues(m map[int]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// cloneAndCopy gives each agent its clone and copies, as the agent, the
// files it made in the project and in its recorded repositories and
// worktrees into it.
func (t *TeamUsers) cloneAndCopy(ctx context.Context, project string, rec *migrationRecord, users *teamUsersFile, opt PrepareOptions, ops *migrateOps) error {
	if opt.HomeQuota <= 0 {
		return errors.New("no home quota given")
	}
	var nested []string
	for _, i := range rec.Items {
		if i.Kind == "repo" || i.Kind == "worktree" {
			nested = append(nested, i.Path)
		}
	}
	quota := strconv.FormatInt(opt.HomeQuota, 10)
	for _, id := range opt.CloneAgents {
		u := users.find(id)
		if u == nil || u.Removed {
			continue // never started: it gets its clone at its start
		}
		home := t.home(id)
		out, err := ops.asAgent(ctx, u.UID, home, []string{"clone-own", project, home, id, quota, opt.Names[id]}, nil)
		if err != nil {
			return fmt.Errorf("%s's copy of the project: %w", id, err)
		}
		var cr CloneResult
		if err := json.Unmarshal(bytes.TrimSpace(out), &cr); err != nil {
			return fmt.Errorf("%s's copy of the project: %w", id, err)
		}
		type source struct {
			src     string
			exclude []string
			item    *migrationItem
		}
		sources := []source{{src: project, exclude: append([]string{".git"}, nested...)}}
		for _, i := range rec.Items {
			if (i.Kind == "repo" || i.Kind == "worktree") && i.Agent == id {
				sources = append(sources, source{src: filepath.Join(project, i.Path), exclude: []string{".git"}, item: i})
			}
		}
		for _, s := range sources {
			ex, _ := json.Marshal(copyRequest{Exclude: s.exclude})
			out, err := ops.asAgent(ctx, u.UID, home, []string{"migrate-copy", home, s.src, cr.Dir, quota}, bytes.NewReader(ex))
			if err != nil {
				return fmt.Errorf("copy %s's files into its copy of the project: %w", id, err)
			}
			var res copyResult
			if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
				return fmt.Errorf("copy %s's files: %w", id, err)
			}
			short := "~/" + filepath.Base(cr.Dir)
			if s.item != nil {
				s.item.Note = copiedNote(id, short, len(res.Copied), res.Skipped)
				continue
			}
			for _, i := range rec.Items {
				if i.Kind != "file" || i.Agent != id {
					continue
				}
				n := 0
				for _, c := range res.Copied {
					if c == i.Path || strings.HasPrefix(c, i.Path+"/") {
						n++
					}
				}
				var sk []string
				for _, c := range res.Skipped {
					if c == i.Path || strings.HasPrefix(c, i.Path+"/") {
						sk = append(sk, c)
					}
				}
				i.Note = copiedNote(id, short, n, sk)
			}
		}
	}
	return nil
}

func copiedNote(id, where string, n int, skipped []string) string {
	s := fmt.Sprintf("%d files copied into %s's copy (%s), not committed.", n, id, where)
	if len(skipped) > 0 {
		s += fmt.Sprintf(" Not copied, as %s already has them: %s.", where, strings.Join(skipped[:min(len(skipped), 5)], ", "))
	}
	return s
}

// reownTree makes rel in the project (a folder) work's alone, with all
// under it, through descriptors: what isn't work's, is in group team, or
// another uid could write (all of it, with all, as for .git). Paths in
// skip (relative to the project) are left as they are.
func reownTree(project, rel string, skip []string, all bool, work int, ops *migrateOps) error {
	top, err := os.OpenFile(project, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0) // #nosec G304 -- init checked it
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	dir := top
	if rel != "." {
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(top), rel, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
			return nil
		} else if err != nil {
			return err
		}
		if statType(&st) != unix.S_IFDIR {
			return nil
		}
		f, ok, err := openEntry(top, rel, filepath.Join(project, rel), &st)
		if err != nil || !ok {
			return fmt.Errorf("open %s: %w", rel, changedErr(err))
		}
		defer func() { _ = f.Close() }()
		if err := ops.reown(f, true); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		dir = f
	}
	return reownWalk(dir, rel, skip, all, work, ops)
}

func reownWalk(dir *os.File, rel string, skip []string, all bool, work int, ops *migrateOps) error {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, n := range names {
		child := path.Join(rel, n)
		if slices.Contains(skip, child) {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(dir), n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		typ := statType(&st)
		mine := ops.uidOf(child, &st) == work && ops.gidOf(child, &st) != TeamGID
		if typ == unix.S_IFLNK {
			if !mine {
				if err := ops.reownLink(dir, n); err != nil {
					return fmt.Errorf("%s: %w", child, err)
				}
			}
			continue
		}
		if typ != unix.S_IFDIR && typ != unix.S_IFREG {
			continue
		}
		f, ok, err := openEntry(dir, n, filepath.Join(dir.Name(), n), &st)
		if err != nil {
			return fmt.Errorf("open %s: %w", child, err)
		}
		if !ok {
			return fmt.Errorf("%s changed while Portenv made it yours; try again", child)
		}
		change := all || !mine
		if !change {
			change, err = ops.othersWrite(f, &st, typ == unix.S_IFDIR)
		}
		if err == nil && change {
			err = ops.reown(f, typ == unix.S_IFDIR)
		}
		if err == nil && typ == unix.S_IFDIR {
			err = reownWalk(f, child, skip, all, work, ops)
		}
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", child, err)
		}
	}
	return nil
}

// ResolveMigration is the owner's answer to the move (ADR 0022 §2a point
// 6): each item named in remove is deleted (a worktree with its
// registration), every other becomes work's. Refused while any agent
// process runs; an id that isn't a choice in the record changes nothing.
func (t *TeamUsers) ResolveMigration(ctx context.Context, projectDir string, remove []string) error {
	prefix := "/home/" + t.Cfg.User + "/"
	name, ok := strings.CutPrefix(projectDir, prefix)
	if !ok || name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, ".") || path.Clean(projectDir) != projectDir {
		return fmt.Errorf("%q isn't a project folder in %s", projectDir, prefix)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	project := filepath.Join(t.Cfg.HomeRoot, t.Cfg.User, name)
	ops := t.mops()
	if err := ops.checkStopped(); err != nil {
		return err
	}
	rec, err := t.loadMigration(project)
	if err != nil {
		return err
	}
	if rec == nil {
		return errors.New("there's nothing of the move left to answer")
	}
	byID := map[string]*migrationItem{}
	for _, i := range rec.Items {
		byID[i.ID] = i
	}
	for _, id := range remove {
		if i := byID[id]; i == nil || !i.Choice {
			return fmt.Errorf("%q isn't an item you can remove", id)
		}
	}
	for _, i := range rec.Items {
		if !i.Choice {
			continue
		}
		if err := checkItemPath(project, i.Path); err != nil {
			return err
		}
		if slices.Contains(remove, i.ID) {
			if err := os.RemoveAll(filepath.Join(project, i.Path)); err != nil {
				return fmt.Errorf("remove %s: %w", i.Path, err)
			}
			if i.Registration != "" {
				if err := checkItemPath(project, i.Registration); err != nil {
					return err
				}
				if err := os.RemoveAll(filepath.Join(project, i.Registration)); err != nil {
					return fmt.Errorf("remove %s: %w", i.Registration, err)
				}
			}
			continue
		}
		if err := t.keepItem(project, i, ops); err != nil {
			return err
		}
	}
	if err := os.Remove(t.migrationFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// keepItem makes a kept item work's, with all under it.
func (t *TeamUsers) keepItem(project string, i *migrationItem, ops *migrateOps) error {
	dir, base := filepath.Split(filepath.Join(project, i.Path))
	d, err := os.OpenFile(filepath.Clean(dir), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0) // #nosec G304 -- checked by checkItemPath
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	var st unix.Stat_t
	if err := unix.Fstatat(fdOf(d), base, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil // gone already
	} else if err != nil {
		return err
	}
	switch statType(&st) {
	case unix.S_IFLNK:
		return ops.reownLink(d, base)
	case unix.S_IFREG:
		f, ok, err := openEntry(d, base, filepath.Join(dir, base), &st)
		if err != nil || !ok {
			return fmt.Errorf("open %s: %w", i.Path, changedErr(err))
		}
		defer func() { _ = f.Close() }()
		return ops.reown(f, false)
	case unix.S_IFDIR:
		return reownTree(project, i.Path, nil, true, t.Cfg.UID, ops)
	}
	return nil
}

// changedErr is err, or, when an entry was swapped while Portenv opened
// it, a line saying so.
func changedErr(err error) error {
	if err != nil {
		return err
	}
	return errors.New("it changed while Portenv opened it; try again")
}

// checkItemPath allows a plain path in the project whose folders on the
// way are real folders, not links.
func checkItemPath(project, rel string) error {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || slices.Contains(strings.Split(rel, "/"), "..") {
		return fmt.Errorf("%q isn't a plain path in the project", rel)
	}
	p := project
	parts := strings.Split(rel, "/")
	for _, s := range parts[:len(parts)-1] {
		p = filepath.Join(p, s)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s isn't a folder", p)
		}
	}
	return nil
}

// ownedByMe is migrate-copy's test of a file: the calling agent's own.
func ownedByMe(_ string, st *unix.Stat_t) bool { return int(st.Uid) == os.Getuid() }

// copyRequest and copyResult are migrate-copy's stdin and answer.
type copyRequest struct {
	Exclude []string `json:"exclude"`
}

type copyResult struct {
	Copied  []string `json:"copied"`
	Skipped []string `json:"skipped,omitempty"`
}

// maxCopyList caps the paths migrate-copy lists.
const maxCopyList = 1000

// migrateCopy is team-io migrate-copy HOME SRC DEST QUOTA, run as the
// agent, with {"exclude": [...]} on stdin: every regular file under SRC
// the agent owns (outside .git folders and the excluded paths, relative to
// SRC) is copied to the same path under DEST, its clone in HOME, never
// over a file already there and never through a link, within the quota.
func migrateCopy(home, src, dest, quota string, owns func(full string, st *unix.Stat_t) bool, r io.Reader, w io.Writer) error {
	limit, err := strconv.ParseInt(quota, 10, 64)
	if err != nil || limit <= 0 {
		return errors.New("no quota given")
	}
	if err := ownHome(home); err != nil {
		return err
	}
	if filepath.Dir(dest) != home || strings.HasPrefix(filepath.Base(dest), ".") || filepath.Clean(dest) != dest {
		return fmt.Errorf("%q isn't a clone in %s", dest, home)
	}
	if !filepath.IsAbs(src) || filepath.Clean(src) != src {
		return fmt.Errorf("%q isn't a full, plain path", src)
	}
	var req copyRequest
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&req); err != nil {
		return err
	}
	top, err := os.OpenFile(src, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0) // #nosec G304 -- a folder in the project, checked by init
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	type file struct {
		rel  string
		size int64
	}
	var files []file
	var total int64
	var walk func(dir *os.File, rel string) error
	walk = func(dir *os.File, rel string) error {
		names, err := dir.Readdirnames(-1)
		if err != nil {
			return nil // a folder it can't read holds nothing of its own to copy
		}
		slices.Sort(names)
		for _, n := range names {
			child := path.Join(rel, n)
			if n == ".git" || slices.Contains(req.Exclude, child) {
				continue
			}
			var st unix.Stat_t
			if unix.Fstatat(fdOf(dir), n, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue
			}
			switch statType(&st) {
			case unix.S_IFDIR:
				f, ok, err := openEntry(dir, n, filepath.Join(dir.Name(), n), &st)
				if err != nil || !ok {
					continue
				}
				err = walk(f, child)
				_ = f.Close()
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				if owns(filepath.Join(dir.Name(), n), &st) {
					files = append(files, file{child, st.Size})
					total += st.Size
				}
			}
		}
		return nil
	}
	if err := walk(top, "."); err != nil {
		return err
	}
	used, err := homeSize(home)
	if err != nil {
		return err
	}
	if used+total > limit {
		return fmt.Errorf("the files to copy (%s) don't fit in its files' limit (%s). Raise its limit in Settings", humanBytes(total), humanBytes(limit))
	}
	res := copyResult{Copied: []string{}}
	for _, f := range files {
		rel := strings.TrimPrefix(f.rel, "./")
		copied, err := copyOne(top, rel, home, filepath.Base(dest))
		if err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
		switch {
		case copied && len(res.Copied) < maxCopyList:
			res.Copied = append(res.Copied, rel)
		case !copied && len(res.Skipped) < maxCopyList:
			res.Skipped = append(res.Skipped, rel)
		}
	}
	return json.NewEncoder(w).Encode(res)
}

// copyOne copies rel from the source folder into home/clone/rel, as a new
// file; it returns false, copying nothing, when one is already there with
// other content (the same content counts as copied).
func copyOne(src *os.File, rel, home, clone string) (bool, error) {
	parts := strings.Split(rel, "/")
	sdir := src
	var opened []*os.File
	defer func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}()
	for _, s := range parts[:len(parts)-1] {
		fd, err := unix.Openat(fdOf(sdir), s, dirFlags, 0)
		if err != nil {
			return false, err
		}
		sdir = os.NewFile(uintptr(fd), s) // #nosec G115 -- a descriptor
		opened = append(opened, sdir)
	}
	in, err := unix.Openat(fdOf(sdir), parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	inF := os.NewFile(uintptr(in), rel) // #nosec G115 -- a descriptor
	defer func() { _ = inF.Close() }()
	fi, err := inF.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s isn't a file", rel)
	}
	data, err := io.ReadAll(io.LimitReader(inF, fi.Size()+1))
	if err != nil {
		return false, err
	}
	dfd, err := openDirs(home, append([]string{clone}, parts[:len(parts)-1]...), true)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(dfd) }()
	out, err := unix.Openat(dfd, parts[len(parts)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(fi.Mode().Perm()|0o600)) // #nosec G115 -- permission bits
	if errors.Is(err, unix.EEXIST) {
		old, rerr := unix.Openat(dfd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if rerr != nil {
			return false, nil
		}
		oldF := os.NewFile(uintptr(old), rel) // #nosec G115 -- a descriptor
		have, _ := io.ReadAll(io.LimitReader(oldF, int64(len(data))+1))
		_ = oldF.Close()
		return bytes.Equal(have, data), nil
	}
	if err != nil {
		return false, err
	}
	outF := os.NewFile(uintptr(out), rel) // #nosec G115 -- a descriptor
	_, werr := outF.Write(data)
	serr := outF.Sync()
	cerr := outF.Close()
	return true, errors.Join(werr, serr, cerr)
}
