// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// Opening the project to the team (ADR 0022 §2) at team start: the owner's
// home becomes passable (0711) with its private folders checked, and the
// project becomes the team's through group team, setgid folders and
// default ACLs, except what would let an agent run code as the owner
// (.git/hooks, .git/config) or change agent files (.portenv/).
//
// init (root) only checks paths and adds the system git setting; the work
// on the tree runs as work ("portenv-agent team-io open-project"), through
// a descriptor of each file, never a path, so a link an agent swaps in
// mid-walk reaches nothing.

// teamIOBinary is the plain portenv-agent, which runs team-io.
const teamIOBinary = "/usr/local/bin/portenv-agent"

// protectedInProject are never the team's to write.
var protectedInProject = []string{".git/hooks", ".git/config", ".portenv"}

// PrepareProject opens the project folder (/home/<user>/<name>) to the team.
func (t *TeamUsers) PrepareProject(ctx context.Context, projectDir string) ([]string, error) {
	prefix := "/home/" + t.Cfg.User + "/"
	name, ok := strings.CutPrefix(projectDir, prefix)
	if !ok || name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, ".") || path.Clean(projectDir) != projectDir {
		return nil, fmt.Errorf("%q isn't a project folder in %s", projectDir, prefix)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	work := filepath.Join(t.Cfg.HomeRoot, t.Cfg.User)
	project := filepath.Join(work, name)
	for _, d := range []string{t.Cfg.HomeRoot, work, project} {
		if err := checkRealDir(d); err != nil {
			return nil, err
		}
	}
	// On a new workspace group team doesn't exist yet (agent users come
	// later), and setfacl can't name a group the box doesn't have.
	if err := t.ensureTeamGroup(ctx); err != nil {
		return nil, err
	}
	// Nothing directly in the owner's home is readable by others once they
	// can pass through it (R-0262).
	fixed, err := t.tightenHome(work, append(t.projects(), name))
	if err != nil {
		return nil, err
	}
	fixed = append(t.takeHomeFixesLocked(), fixed...)
	if err := t.recordProject(name); err != nil {
		return nil, err
	}
	if out, err := t.Run.Run(ctx, nil, "runuser", "-u", t.Cfg.User, "--", teamIOBinary, "team-io", "open-project", project); err != nil {
		return nil, cmdError("open the project to the team", out, err)
	}
	if fi, err := os.Lstat(filepath.Join(project, ".git")); err == nil && fi.IsDir() {
		// Agents run git in work's repository: allow it for this one, and
		// for work's own worktrees inside it (CC-0493), each named, each
		// once.
		if err := t.trustRepositories(ctx, append([]string{projectDir}, t.worktrees(project, projectDir)...)); err != nil {
			return nil, err
		}
	}
	return fixed, nil
}

// trustRepositories adds each folder to git's system safe.directory, if
// it isn't there.
func (t *TeamUsers) trustRepositories(ctx context.Context, dirs []string) error {
	// git config --get-all exits 1 when there's none yet.
	out, _ := t.Run.Run(ctx, nil, "git", "config", "--system", "--get-all", "safe.directory")
	have := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, d := range dirs {
		if slices.Contains(have, d) {
			continue
		}
		if out, err := t.Run.Run(ctx, nil, "git", "config", "--system", "--add", "safe.directory", d); err != nil {
			return cmdError("let agents use the project's git repository", out, err)
		}
		have = append(have, d)
	}
	return nil
}

// worktrees are work's git worktrees inside the project, as git names
// them (under projectDir), for safe.directory: agents get "dubious
// ownership" in a worktree work owns, as in the project. Only a worktree
// git registered in the project's .git/worktrees, that's a real folder
// inside the project (not in .git, no link on the way), and whose folder,
// .git file and registration are all work's, and agree with each other,
// is named. An agent can't make files that are work's, so it can't get
// a folder of its own trusted here; never "<project>/*", which would
// trust an agent's repository for work too. init reads these as root,
// so every file is opened without following a link, and small.
func (t *TeamUsers) worktrees(project, projectDir string) []string {
	ownerOf := t.ownerOf
	if ownerOf == nil {
		ownerOf = fileOwner
	}
	mine := func(p string, dir bool) bool {
		fi, err := os.Lstat(p)
		return err == nil && fi.IsDir() == dir && (dir || fi.Mode().IsRegular()) && ownerOf(p) == t.Cfg.UID
	}
	reg := filepath.Join(project, ".git", "worktrees")
	if !mine(filepath.Join(project, ".git"), true) || !mine(reg, true) {
		return nil
	}
	entries, err := os.ReadDir(reg)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !mine(filepath.Join(reg, name), true) || !mine(filepath.Join(reg, name, "gitdir"), false) {
			continue
		}
		b, err := readNoFollow(filepath.Join(reg, name), "gitdir", 4096)
		if err != nil {
			continue
		}
		gitFile := strings.TrimSuffix(string(b), "\n")
		rel, ok := strings.CutPrefix(gitFile, projectDir+"/")
		if !ok || path.Clean(gitFile) != gitFile || path.Base(rel) != ".git" || path.Dir(rel) == "." || strings.HasPrefix(rel, ".git/") {
			continue
		}
		wt := path.Dir(rel)
		real := true
		for p := wt; p != "."; p = path.Dir(p) {
			if !mine(filepath.Join(project, filepath.FromSlash(p)), true) {
				real = false
				break
			}
		}
		local := filepath.Join(project, filepath.FromSlash(wt))
		if !real || !mine(filepath.Join(local, ".git"), false) {
			continue
		}
		g, err := readNoFollow(local, ".git", 4096)
		if err != nil || strings.TrimSuffix(string(g), "\n") != "gitdir: "+projectDir+"/.git/worktrees/"+name {
			continue
		}
		out = append(out, projectDir+"/"+wt)
	}
	return out
}

// fileOwner is a path's owner, without following a link.
func fileOwner(p string) int {
	fi, err := os.Lstat(p)
	if err != nil {
		return -1
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// errNoACL is the plain line when the filesystem won't keep ACLs.
var errNoACL = errors.New("this workspace's files don't keep ACLs, so agents can't share the project. Portenv won't start the team")

// openProject is team-io open-project, run as work: group team, setgid
// and default ACLs on the project, git's shared repository, the protected
// paths closed again (readable by the team, never writable: agents read
// .git/config to use git, CC-0493), and /home/work made passable (0711).
//
// It walks the tree through descriptors: each entry is opened from its
// folder's descriptor without following a link, checked to be the file
// it looked at, and changed through that descriptor, so a link or a swap
// an agent makes mid-walk reaches nothing. It changes only what work owns
// (CC-0494). What an agent made was made in the team's setgid folders,
// under their default ACLs, so it's already the team's, and work couldn't
// change it anyway; an agent's folder is skipped with all it holds. The
// one exception is a protected path: if work doesn't own it, an agent
// could have changed it, and the team isn't started.
func openProject(ctx context.Context, project string, run Runner, ops projectOps) error {
	top, err := os.OpenFile(project, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- init checked it's /home/work/<name>
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fdOf(top), &st); err != nil {
		return err
	}
	if uid := ops.uidOf(".", &st); uid != ops.self {
		return fmt.Errorf("%s belongs to uid %d, not to you", project, uid)
	}
	w := &projectWalk{ops: ops, protected: map[fileID]bool{}}
	if err := w.collectProtected(top); err != nil {
		return err
	}
	// The project folder first: a filesystem without ACLs stops here.
	if err := w.change(top, ".", true, false); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			return errNoACL
		}
		return err
	}
	if err := w.walk(top, ".", false); err != nil {
		return err
	}
	if fi, err := os.Lstat(filepath.Join(project, ".git")); err == nil && fi.IsDir() {
		if out, err := run.Run(ctx, nil, "git", "-C", project, "config", "core.sharedRepository", "group"); err != nil {
			return cmdError("share the project's git repository", out, err)
		}
	}
	// git rewrote .git/config (a new file, with .git's default ACL and
	// the old mode, which can leave the team a --- mask): close the
	// protected paths again, after it.
	if err := w.closeProtected(top); err != nil {
		return err
	}
	return chmodNoFollow(filepath.Dir(project), 0o711)
}

// projectOps are open-project's changes to one open file, and whose it is
// (fakes in tests: a test can't chown to group team, or own another uid's
// file).
type projectOps struct {
	self     int // work's uid
	owner    func(rel string, st *unix.Stat_t) int
	chown    func(f *os.File, gid int) error
	addACL   func(f *os.File, dir bool) error                      // setfacl -m g:team:rwX (and d:g:team:rwX on a folder)
	readACL  func(f *os.File, dir bool) error                      // on a file without one: setfacl -m g:team:rX (and d:g:team:rX on a folder)
	clearACL func(f *os.File, dir bool) (fs.FileMode, bool, error) // setfacl -b: the owning group's bits, and whether it had an ACL
}

func (o projectOps) uidOf(rel string, st *unix.Stat_t) int {
	if o.owner != nil {
		return o.owner(rel, st)
	}
	return int(st.Uid)
}

type fileID struct{ dev, ino uint64 }

func idOf(st *unix.Stat_t) fileID {
	return fileID{statDev(st), st.Ino}
}

type projectWalk struct {
	ops       projectOps
	protected map[fileID]bool // everything at or under a protected path
}

// fdOf is f's descriptor, for the *at calls.
func fdOf(f *os.File) int {
	return int(f.Fd()) // #nosec G115 -- a descriptor
}

// openEntry opens name in dir without following a link, as the file
// fstatat saw (st); ok is false if it's gone or isn't that file any more.
func openEntry(dir *os.File, name, full string, st *unix.Stat_t) (f *os.File, ok bool, err error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if statType(st) == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(fdOf(dir), name, flags, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ENXIO) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	f = os.NewFile(uintptr(fd), full) // #nosec G115 -- a descriptor
	var now unix.Stat_t
	if err := unix.Fstat(fd, &now); err != nil {
		_ = f.Close()
		return nil, false, err
	}
	if idOf(&now) != idOf(st) || now.Uid != st.Uid || statType(&now) != statType(st) {
		_ = f.Close()
		return nil, false, nil
	}
	return f, true, nil
}

// collectProtected notes every file at or under the protected paths, so
// a hard link to one elsewhere in the project stays closed too.
func (w *projectWalk) collectProtected(top *os.File) error {
	for _, p := range protectedInProject {
		f, st, err := openProtected(top, p)
		if err != nil || f == nil {
			return err
		}
		w.protected[idOf(&st)] = true
		if statType(&st) == unix.S_IFDIR {
			err = w.collectUnder(f)
		}
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// closeProtected closes each protected path, and all under it, to the
// team's writes, leaving them readable.
func (w *projectWalk) closeProtected(top *os.File) error {
	for _, p := range protectedInProject {
		f, st, err := openProtected(top, p)
		if err != nil || f == nil {
			return err
		}
		dir := statType(&st) == unix.S_IFDIR
		if uid := w.ops.uidOf(p, &st); uid != w.ops.self {
			err = fmt.Errorf("%s in the project belongs to uid %d, not to you, so an agent could have changed it. Portenv won't open the project to the team until you check it and make it yours again", p, uid)
		} else {
			err = w.change(f, p, dir, true)
		}
		if err == nil && dir {
			err = w.walk(f, p, true)
		}
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// openProtected opens the protected path p in the project one name at a
// time from top's descriptor, never through a link; nil if it isn't
// there. A protected path, or a folder on the way to one, that's a link
// is refused: git would follow it.
func openProtected(top *os.File, p string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	dir, parts := top, strings.Split(p, "/")
	closeDir := func() {
		if dir != top {
			_ = dir.Close()
		}
	}
	for i, name := range parts {
		err := unix.Fstatat(fdOf(dir), name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			closeDir()
			return nil, st, nil
		}
		if err != nil {
			closeDir()
			return nil, st, fmt.Errorf("check %s: %w", p, err)
		}
		typ := statType(&st)
		if typ == unix.S_IFLNK {
			closeDir()
			return nil, st, fmt.Errorf("%s in the project is a link; Portenv won't open the project to the team until it's a real file or folder", path.Join(parts[:i+1]...))
		}
		last := i == len(parts)-1
		if (!last && typ != unix.S_IFDIR) || (last && typ != unix.S_IFDIR && typ != unix.S_IFREG) {
			closeDir()
			return nil, st, nil // .git as a file (a worktree's): nothing to protect under it
		}
		f, ok, err := openEntry(dir, name, filepath.Join(dir.Name(), name), &st)
		closeDir()
		if err != nil {
			return nil, st, fmt.Errorf("check %s: %w", p, err)
		}
		if !ok {
			return nil, st, fmt.Errorf("%s changed while Portenv opened the project to the team; try again", p)
		}
		dir = f
	}
	return dir, st, nil
}

func (w *projectWalk) collectUnder(dir *os.File) error {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(dir), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		w.protected[idOf(&st)] = true
		if statType(&st) != unix.S_IFDIR {
			continue
		}
		f, ok, err := openEntry(dir, name, filepath.Join(dir.Name(), name), &st)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		err = w.collectUnder(f)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// walk changes what's in dir (rel in the project), then what's under it.
func (w *projectWalk) walk(dir *os.File, rel string, protected bool) error {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	for _, name := range names {
		child := path.Join(rel, name)
		var st unix.Stat_t
		if err := unix.Fstatat(fdOf(dir), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue // gone
			}
			return fmt.Errorf("check %s: %w", child, err)
		}
		typ := statType(&st)
		if typ != unix.S_IFDIR && typ != unix.S_IFREG {
			continue // links and special files are left as they are
		}
		isDir := typ == unix.S_IFDIR
		p := protected || protectedPath(child) || w.protected[idOf(&st)]
		if uid := w.ops.uidOf(child, &st); uid != w.ops.self {
			if p {
				return fmt.Errorf("%s in the project belongs to uid %d, not to you, so an agent could have changed it. Portenv won't open the project to the team until you check it and make it yours again", child, uid)
			}
			continue // an agent's, made in the team's folders: already the team's
		}
		f, ok, err := openEntry(dir, name, filepath.Join(dir.Name(), name), &st)
		if err != nil {
			if errors.Is(err, unix.EACCES) && !p {
				continue // work's own file that work can't read: it stays work's
			}
			return fmt.Errorf("open %s: %w", child, err)
		}
		if !ok {
			if p {
				return fmt.Errorf("%s changed while Portenv opened the project to the team; try again", child)
			}
			continue // replaced mid-walk: the next team start sees what's there
		}
		err = w.change(f, child, isDir, p)
		if err == nil && isDir {
			err = w.walk(f, child, p)
		}
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// change makes one file the team's, or, if it's protected, closes it to
// the team's writes.
func (w *projectWalk) change(f *os.File, rel string, dir, protected bool) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	perm := fi.Mode().Perm()
	if protected {
		// Readable by the team, never writable: group team, the owner's
		// read and search bits for the group, and ACL entry g:team:rX with
		// a mask to match, so agents can read .git/config and run the
		// hooks, and can't change either (CC-0493).
		g, had, err := w.ops.clearACL(f, dir)
		if err != nil {
			return fmt.Errorf("close %s to the team: %w", rel, err)
		}
		if had {
			perm = perm&^0o070 | g
		}
		if err := w.ops.chown(f, TeamGID); err != nil {
			return fmt.Errorf("give %s to the team to read: %w", rel, err)
		}
		if err := f.Chmod(perm&0o700 | (perm&0o500)>>3 | perm&0o005); err != nil {
			return err
		}
		if err := w.ops.readACL(f, dir); err != nil {
			return fmt.Errorf("let the team read %s: %w", rel, err)
		}
		return nil
	}
	if err := w.ops.chown(f, TeamGID); err != nil {
		return fmt.Errorf("give %s to the team: %w", rel, err)
	}
	if dir {
		err = f.Chmod(perm | 0o070 | os.ModeSetgid)
	} else {
		err = f.Chmod(perm&^0o070 | (perm&0o700)>>3)
	}
	if err != nil {
		return err
	}
	if err := w.ops.addACL(f, dir); err != nil {
		return fmt.Errorf("give %s to group %s: %w", rel, TeamGroup, err)
	}
	return nil
}

func protectedPath(rel string) bool {
	for _, p := range protectedInProject {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// chmodNoFollow sets a folder's mode through a descriptor of the folder.
func chmodNoFollow(dir string, mode os.FileMode) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- a fixed folder
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Chmod(mode)
}

// PrepareTeamProject on the agent channel relays to init, which alone
// does it (ADR 0022 §2): the API process can't change work's files.
func (s *apiServer) PrepareTeamProject(ctx context.Context, req *agentv1.PrepareTeamProjectRequest) (*agentv1.PrepareTeamProjectResponse, error) {
	if s.prepareProject == nil {
		return nil, status.Error(codes.Unimplemented, "no team in this workspace")
	}
	fixed, err := s.prepareProject(ctx, req.GetProjectDir())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &agentv1.PrepareTeamProjectResponse{Fixed: fixed}, nil
}

// askInitPrepare asks init on its local socket.
func askInitPrepare(socket string) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, dir string) ([]string, error) {
		conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, fmt.Errorf("connect to init: %w", err)
		}
		defer func() { _ = conn.Close() }()
		r, err := agentv1.NewAgentServiceClient(conn).PrepareTeamProject(ctx, &agentv1.PrepareTeamProjectRequest{ProjectDir: dir})
		if err != nil {
			return nil, errors.New(status.Convert(err).Message())
		}
		return r.GetFixed(), nil
	}
}

// tightenHome takes the "other" bits, and group write, off every entry
// directly in the owner's home except the projects and .portenv, and gives
// any that's in group team back to work's group: folders end up 0700 or
// 0750, files 0600 or 0640 (R-0262). Each change goes through a descriptor
// of the entry itself, never through a link; links and special files are
// left as they are. It returns what it changed, for the log and the
// owner's notice.
func (t *TeamUsers) tightenHome(work string, projects []string) ([]string, error) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return nil, err
	}
	groupOf := t.groupOf
	if groupOf == nil {
		groupOf = fileGroup
	}
	var fixed []string
	for _, e := range entries {
		n := e.Name()
		if n == ".portenv" || slices.Contains(projects, n) {
			continue
		}
		p := filepath.Join(work, n)
		if e.Type()&os.ModeSymlink != 0 || (!e.IsDir() && !e.Type().IsRegular()) {
			continue
		}
		flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if e.IsDir() {
			flags |= syscall.O_DIRECTORY
		}
		f, err := os.OpenFile(p, flags, 0) // #nosec G304 -- an entry of work's home, never a link
		if err != nil {
			return fixed, fmt.Errorf("check ~/%s: %w", n, err)
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return fixed, err
		}
		perm := fi.Mode().Perm()
		var what []string
		if perm&0o027 != 0 {
			if err := f.Chmod(fi.Mode()&os.ModeSetgid | perm&^0o027); err != nil {
				_ = f.Close()
				return fixed, fmt.Errorf("make ~/%s private: %w", n, err)
			}
			what = append(what, fmt.Sprintf("%o → %o", perm, perm&^0o027))
		}
		if g := groupOf(p); g != t.Cfg.UID {
			if err := t.Chown(p, -1, t.Cfg.UID); err != nil {
				_ = f.Close()
				return fixed, fmt.Errorf("give ~/%s back to group %s: %w", n, t.Cfg.User, err)
			}
			what = append(what, fmt.Sprintf("group %d → %s", g, t.Cfg.User))
		}
		_ = f.Close()
		if len(what) > 0 {
			fixed = append(fixed, "~/"+n+": "+strings.Join(what, ", "))
		}
	}
	return fixed, nil
}

// fileGroup is a path's group, without following a link.
func fileGroup(p string) int {
	fi, err := os.Lstat(p)
	if err != nil {
		return -1
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Gid)
	}
	return -1
}

// projects are the project folders a team has opened, from users.json.
func (t *TeamUsers) projects() []string {
	f, err := t.load()
	if err != nil {
		return nil
	}
	return f.Projects
}

func (t *TeamUsers) recordProject(name string) error {
	f, err := t.load()
	if err != nil {
		return err
	}
	if slices.Contains(f.Projects, name) {
		return nil
	}
	f.Projects = append(f.Projects, name)
	return t.save(f)
}

// takeHomeFixes returns what box starts fixed since the last team start,
// once.
func (t *TeamUsers) takeHomeFixes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.takeHomeFixesLocked()
}

func (t *TeamUsers) takeHomeFixesLocked() []string {
	out := t.homeFixes
	t.homeFixes = nil
	return out
}
