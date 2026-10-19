// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// The project at team start (ADR 0022 §2 and §2a, the owner's decision
// R-0315): the owner's home becomes passable (0711) with its private
// folders checked, and the project becomes readable by the team and
// writable by no agent. Every file in it stays work's, with group team's
// read (g:team:r-X) in its ACL and, on a folder, its default ACL, and no
// write for group team, other users or any named entry. Portenv sets no
// core.sharedRepository and adds no system git safe.directory: agents
// work in their own clones (teamclone.go), and their work comes back as
// branches the owner fetches.
//
// init (root) checks paths and the system git config. The work on the
// tree runs as work ("portenv-agent team-io open-project"), through a
// descriptor of each file, never a path, so a link swapped in mid-walk
// reaches nothing. Before it changes anything it looks for what an agent
// could have changed in what work's git reads: a file in the project that
// isn't work's, anything in a .git another uid could write, and
// core.sharedRepository. If it finds any, it changes nothing and the team
// doesn't start: the workspace's team used the shared project.

// teamIOBinary is the plain portenv-agent, which runs team-io.
const teamIOBinary = "/usr/local/bin/portenv-agent"

// ProjectFinding is one thing an agent could have changed in what work's
// git reads, found at team start.
type ProjectFinding struct {
	// Path is relative to the project; for a safe.directory entry, the
	// entry itself.
	Path string `json:"path"`
	// What is "owner" (the file isn't work's; UID says whose),
	// "writable" (in a .git, another uid can write it),
	// "shared-repository" (core.sharedRepository in .git/config) or
	// "safe-directory" (a system safe.directory entry for the project or
	// a folder in it).
	What string `json:"what"`
	UID  int    `json:"uid,omitempty"`
	Dir  bool   `json:"dir,omitempty"`
}

// String is the finding as a plain line.
func (f ProjectFinding) String() string {
	switch f.What {
	case "owner":
		return fmt.Sprintf("%s (uid %d's)", f.Path, f.UID)
	case "writable":
		return f.Path + " (others can write it)"
	case "shared-repository":
		return ".git/config sets core.sharedRepository"
	case "safe-directory":
		return "the system git config trusts " + f.Path
	}
	return f.Path
}

// maxFindings caps what open-project lists; More counts the rest.
const maxFindings = 200

// ProjectFindings are open-project's findings.
type ProjectFindings struct {
	Items []ProjectFinding `json:"items"`
	More  int              `json:"more,omitempty"`
}

func (p *ProjectFindings) add(f ProjectFinding) {
	if len(p.Items) >= maxFindings {
		p.More++
		return
	}
	p.Items = append(p.Items, f)
}

// Empty is whether nothing was found.
func (p ProjectFindings) Empty() bool { return len(p.Items) == 0 && p.More == 0 }

// SharedProjectError is the plain line for a workspace whose team used the
// shared project: Portenv won't start the team on it.
type SharedProjectError struct{ Found ProjectFindings }

func (e *SharedProjectError) Error() string {
	var some []string
	for _, f := range e.Found.Items[:min(3, len(e.Found.Items))] {
		some = append(some, f.String())
	}
	if n := len(e.Found.Items) + e.Found.More - len(some); n > 0 {
		some = append(some, fmt.Sprintf("%d more", n))
	}
	return "this workspace's team worked in your project, so agents could have changed files your git reads (" + strings.Join(some, "; ") +
		"). Agents now work in their own copies, and Portenv won't start the team until the project is yours alone again"
}

// PrepareProject makes the project folder (/home/<user>/<name>) readable
// by the team and writable by no agent.
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
	// later), and an ACL can't name a group the box doesn't have.
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
	var found ProjectFindings
	for _, e := range t.portenvSafeDirs(ctx, projectDir) {
		found.add(ProjectFinding{Path: e, What: "safe-directory"})
	}
	args := []string{"-u", t.Cfg.User, "--", teamIOBinary, "team-io", "open-project", project}
	if !found.Empty() {
		args = append(args, "look") // it only looks: nothing changes until it's all fixed
	}
	out, err := t.Run.Run(ctx, nil, "runuser", args...)
	if err != nil {
		return nil, cmdError("open the project to the team", out, err)
	}
	opened, err := parseFindings(out)
	if err != nil {
		return nil, fmt.Errorf("open the project to the team: %w", err)
	}
	for _, f := range opened.Items {
		found.add(f)
	}
	found.More += opened.More
	if !found.Empty() {
		return nil, &SharedProjectError{Found: found}
	}
	return fixed, nil
}

// portenvSafeDirs are the system git config's safe.directory entries for
// the project or a folder in it: Portenv added them before R-0315, and
// they apply to work's git too.
func (t *TeamUsers) portenvSafeDirs(ctx context.Context, projectDir string) []string {
	out, err := t.Run.Run(ctx, nil, "git", "config", "--system", "--get-all", "safe.directory")
	if err != nil {
		return nil // none (git exits 1), or no system config
	}
	var dirs []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l == projectDir || strings.HasPrefix(l, projectDir+"/") {
			dirs = append(dirs, l)
		}
	}
	return dirs
}

// parseFindings reads open-project's output: one JSON line, its last.
func parseFindings(out []byte) (ProjectFindings, error) {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	var f ProjectFindings
	if len(lines) == 0 || len(lines[len(lines)-1]) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(lines[len(lines)-1], &f); err != nil {
		return f, fmt.Errorf("open-project said %q", bytes.TrimSpace(out))
	}
	return f, nil
}

// errNoACL is the plain line when the filesystem won't keep ACLs.
var errNoACL = errors.New("this workspace's files don't keep ACLs, so agents can't read the project. Portenv won't start the team")

// openProject is team-io open-project, run as work. It first looks for
// what an agent could have changed in what work's git reads; if it finds
// anything it changes nothing and returns it. Otherwise every file and
// folder of work's in the project gets the team's read and no one else's
// write, and /home/work becomes passable (0711).
//
// It walks the tree through descriptors: each entry is opened from its
// folder's descriptor without following a link, checked to be the file
// it looked at, and changed through that descriptor, so a link or a swap
// mid-walk reaches nothing. Links and special files of work's are left as
// they are.
func openProject(ctx context.Context, project string, run Runner, ops projectOps, lookOnly bool) (ProjectFindings, error) {
	var found ProjectFindings
	openTop := func() (*os.File, error) {
		top, err := os.OpenFile(project, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- init checked it's /home/work/<name>
		if err != nil {
			return nil, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(fdOf(top), &st); err != nil {
			_ = top.Close()
			return nil, err
		}
		if uid := ops.uidOf(".", &st); uid != ops.self {
			_ = top.Close()
			return nil, fmt.Errorf("%s belongs to uid %d, not to you", project, uid)
		}
		return top, nil
	}
	look, err := openTop()
	if err != nil {
		return found, err
	}
	w := &projectWalk{ops: ops, found: &found}
	err = w.walk(look, ".", false, false)
	_ = look.Close()
	if err != nil {
		return found, err
	}
	if sharedRepository(ctx, run, project) {
		found.add(ProjectFinding{Path: ".git/config", What: "shared-repository"})
	}
	if !found.Empty() || lookOnly {
		return found, nil
	}
	top, err := openTop()
	if err != nil {
		return found, err
	}
	defer func() { _ = top.Close() }()
	// The project folder first: a filesystem without ACLs stops here.
	if err := ops.readOnly(top, true); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			return found, errNoACL
		}
		return found, fmt.Errorf("give the team read of the project: %w", err)
	}
	if err := w.walk(top, ".", false, true); err != nil {
		return found, err
	}
	return found, chmodNoFollow(filepath.Dir(project), 0o711)
}

// sharedRepository is whether the project's .git/config sets
// core.sharedRepository (to anything but false or umask). It reads that
// one file (git config --file reads no other config and runs nothing).
func sharedRepository(ctx context.Context, run Runner, project string) bool {
	cfg := filepath.Join(project, ".git", "config")
	if fi, err := os.Lstat(cfg); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	out, err := run.Run(ctx, []string{"GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent"}, "git", "config", "--file", cfg, "--get", "core.sharedRepository")
	if err != nil {
		return false // unset (git exits 1)
	}
	switch v := strings.ToLower(strings.TrimSpace(string(out))); v {
	case "", "false", "umask", "0", "no", "off":
		return false
	}
	return true
}

// projectOps are open-project's looks at and changes to one open file,
// and whose it is (fakes in tests: a test can't own another uid's file, or
// count on ACLs).
type projectOps struct {
	self  int // work's uid
	owner func(rel string, st *unix.Stat_t) int
	// othersWrite is whether a uid other than work could write the file
	// (or, through a folder's default ACL, the files made in it): other's
	// write, the write of a group that isn't work's own, or a named ACL
	// entry with write.
	othersWrite func(f *os.File, st *unix.Stat_t, dir bool) (bool, error)
	// readOnly gives the file the team's read and no one else's write:
	// its ACL becomes the owner's, owning group's and others' entries
	// (others without write, a group that isn't work's own without write)
	// and g:team:r-X, and on a folder the same default ACL.
	readOnly func(f *os.File, dir bool) error
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
	ops   projectOps
	found *ProjectFindings
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

// walk looks at (apply false) or changes (apply true) what's in dir (rel
// in the project), then what's under it. inGit is whether dir is a .git
// folder or in one; any folder or file named .git counts, a nested
// repository's or a worktree's too.
func (w *projectWalk) walk(dir *os.File, rel string, inGit, apply bool) error {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	slices.Sort(names)
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
		isDir := typ == unix.S_IFDIR
		git := inGit || name == ".git"
		if uid := w.ops.uidOf(child, &st); uid != w.ops.self {
			// Not work's: an agent made it in the shared project, or
			// root put it there. Everything under it is listed with it.
			if !apply {
				w.found.add(ProjectFinding{Path: child, What: "owner", UID: uid, Dir: isDir})
			}
			continue
		}
		if !isDir && typ != unix.S_IFREG {
			continue // work's links and special files are left as they are
		}
		if !git && !isDir && !apply {
			continue // only .git's files are looked at
		}
		f, ok, err := openEntry(dir, name, filepath.Join(dir.Name(), name), &st)
		if err != nil {
			if errors.Is(err, unix.EACCES) && !git {
				continue // work's own file that work can't read: it stays as it is
			}
			return fmt.Errorf("open %s: %w", child, err)
		}
		if !ok {
			if git {
				return fmt.Errorf("%s changed while Portenv checked the project; try again", child)
			}
			continue // replaced mid-walk: the next team start sees what's there
		}
		err = w.visit(f, child, &st, isDir, git, apply)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *projectWalk) visit(f *os.File, rel string, st *unix.Stat_t, isDir, git, apply bool) error {
	if apply {
		if err := w.ops.readOnly(f, isDir); err != nil {
			return fmt.Errorf("give the team read of %s: %w", rel, err)
		}
	} else if git {
		writable, err := w.ops.othersWrite(f, st, isDir)
		if err != nil {
			return fmt.Errorf("check %s: %w", rel, err)
		}
		if writable {
			w.found.add(ProjectFinding{Path: rel, What: "writable", Dir: isDir})
		}
	}
	if isDir {
		return w.walk(f, rel, git, apply)
	}
	return nil
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
