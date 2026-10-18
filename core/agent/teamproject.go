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
// on the tree runs as work ("portenv-agent team-io open-project"), inside
// an os.Root, so a link an agent swaps in mid-walk can reach nothing work
// couldn't change anyway.

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
		// Agents run git in work's repository: allow it for this one only.
		if out, err := t.Run.Run(ctx, nil, "git", "config", "--system", "--add", "safe.directory", projectDir); err != nil {
			return nil, cmdError("let agents use the project's git repository", out, err)
		}
	}
	return fixed, nil
}

// errNoACL is the plain line when the filesystem won't keep ACLs.
var errNoACL = errors.New("this workspace's files don't keep ACLs, so agents can't share the project. Portenv won't start the team")

// openProject is team-io open-project, run as work: default ACLs, group
// team and setgid on the project, the protected paths closed again, git's
// shared repository, and /home/work made passable (0711). lchown is
// root.Lchown (a fake in tests).
func openProject(ctx context.Context, project string, run Runner, lchown func(r *os.Root, rel string, uid, gid int) error) error {
	if out, err := run.Run(ctx, nil, "setfacl", "-R", "-P", "-m", "g:"+TeamGroup+":rwX,d:g:"+TeamGroup+":rwX", project); err != nil {
		// Only a filesystem that refuses ACLs gets the no-ACL line; an
		// unknown group (an invalid argument) or anything else is said as
		// setfacl said it.
		if strings.Contains(string(out), "Operation not supported") {
			return errNoACL
		}
		return cmdError("give the project to group "+TeamGroup, out, err)
	}
	out, err := run.Run(ctx, nil, "getfacl", "-c", "-p", project)
	if err != nil || !strings.Contains(string(out), "default:group:"+TeamGroup+":rwx") {
		return errNoACL
	}
	root, err := os.OpenRoot(project)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return nil // links and special files are left as they are
		}
		fi, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		perm := fi.Mode().Perm()
		if protectedPath(rel) {
			return root.Chmod(rel, perm&^0o022)
		}
		if err := lchown(root, rel, -1, TeamGID); err != nil {
			return fmt.Errorf("give %s to the team: %w", rel, err)
		}
		if d.IsDir() {
			return root.Chmod(rel, perm|0o070|os.ModeSetgid)
		}
		return root.Chmod(rel, perm&^0o070|(perm&0o700)>>3)
	})
	if err != nil {
		return err
	}
	for _, p := range protectedInProject {
		full := filepath.Join(project, filepath.FromSlash(p))
		fi, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		args := []string{"-b", full}
		if fi.IsDir() {
			args = []string{"-R", "-P", "-b", full}
		}
		if out, err := run.Run(ctx, nil, "setfacl", args...); err != nil {
			return cmdError("close "+p+" to the team", out, err)
		}
	}
	if fi, err := os.Lstat(filepath.Join(project, ".git")); err == nil && fi.IsDir() {
		if out, err := run.Run(ctx, nil, "git", "-C", project, "config", "core.sharedRepository", "group"); err != nil {
			return cmdError("share the project's git repository", out, err)
		}
	}
	return chmodNoFollow(filepath.Dir(project), 0o711)
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
