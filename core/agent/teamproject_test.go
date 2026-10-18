// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// projectRunner answers runuser and git like a box, and the user and
// group commands from sys. Its ops are open-project's file changes, faked:
// a test can't chown to group team, own another uid's file, or count on
// ACLs. foreign marks paths (relative to the project) another uid owns.
type projectRunner struct {
	calls    []string
	noACL    bool
	aclErr   error
	open     func(project string) error
	sys      *fakeSystem
	acls     []string // paths given g:team:rwX
	readable []string // paths given g:team:rX
	cleared  []string // paths whose ACLs were removed
	foreign  map[string]int
	git      func(args []string) // what a git call does, if anything
	safeDirs string              // git config --system --get-all safe.directory
}

func (r *projectRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	switch name {
	case "runuser":
		if i := slices.Index(args, "open-project"); i >= 0 && r.open != nil {
			return nil, r.open(args[i+1])
		}
		return nil, nil
	case "git":
		if r.git != nil {
			r.git(args)
		}
		if slices.Contains(args, "--get-all") {
			return []byte(r.safeDirs), nil
		}
		return nil, nil
	}
	return r.sys.Run(context.Background(), nil, name, args...)
}

// ops are fake projectOps recording into r and chowns.
func (r *projectRunner) ops(chowns *[]chownCall) projectOps {
	return projectOps{
		self: 1000,
		owner: func(rel string, _ *unix.Stat_t) int {
			if uid, ok := r.foreign[rel]; ok {
				return uid
			}
			return 1000
		},
		chown: func(f *os.File, gid int) error {
			*chowns = append(*chowns, chownCall{f.Name(), -1, gid})
			return nil
		},
		addACL: func(f *os.File, _ bool) error {
			if r.noACL {
				return syscall.EOPNOTSUPP
			}
			if r.aclErr != nil {
				return r.aclErr
			}
			r.acls = append(r.acls, f.Name())
			return nil
		},
		readACL: func(f *os.File, _ bool) error {
			r.readable = append(r.readable, f.Name())
			return nil
		},
		clearACL: func(f *os.File, _ bool) (fs.FileMode, bool, error) {
			r.cleared = append(r.cleared, f.Name())
			return 0, false, nil
		},
	}
}

func (r *projectRunner) ran(prefix string) bool {
	return slices.ContainsFunc(r.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func newProjectTeam(t *testing.T) (*TeamUsers, *projectRunner, *[]chownCall, string) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	project := filepath.Join(work, "shop")
	for _, d := range []string{".ssh", ".config", "shop/src", "shop/.git/hooks", "shop/.portenv/agents"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{".ssh", ".config"} {
		if err := os.Chmod(filepath.Join(work, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	for f, mode := range map[string]os.FileMode{"shop/src/app.ts": 0o644, "shop/run.sh": 0o755, "shop/.git/config": 0o644, "shop/.git/hooks/pre-commit": 0o755, "shop/.portenv/agents/lead.md": 0o644} {
		if err := os.WriteFile(filepath.Join(work, f), []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}
	run := &projectRunner{sys: newFakeSystem()}
	run.sys.groups[TeamGroup] = TeamGID
	run.sys.members[TeamGroup] = map[string]bool{"work": true}
	var chowns []chownCall
	tu := &TeamUsers{
		Cfg:   Config{User: "work", UID: 1000, HomeRoot: root},
		Run:   run,
		Chown: func(p string, uid, gid int) error { chowns = append(chowns, chownCall{p, uid, gid}); return nil },
		// The test's files are the test user's: call them work's.
		ownerOf: func(string) int { return 1000 },
	}
	// team-io open-project, as work, in this process.
	run.open = func(project string) error {
		return openProject(context.Background(), project, run, run.ops(&chowns))
	}
	return tu, run, &chowns, project
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

func TestPrepareTeamProjectOpensTheProjectToTheTeam(t *testing.T) {
	tu, run, chowns, project := newProjectTeam(t)
	if _, err := tu.PrepareProject(context.Background(), "/home/work/shop"); err != nil {
		t.Fatal(err)
	}
	work := filepath.Dir(project)
	if m := mode(t, work).Perm(); m != 0o711 {
		t.Errorf("/home/work is %o, want 711", m)
	}
	if m := mode(t, filepath.Join(project, "src")); m&os.ModeSetgid == 0 || m.Perm()&0o070 != 0o070 {
		t.Errorf("src is %v, want setgid and group rwx", m)
	}
	if m := mode(t, filepath.Join(project, "src", "app.ts")).Perm(); m&0o060 != 0o060 {
		t.Errorf("app.ts is %o, want group rw", m)
	}
	if m := mode(t, filepath.Join(project, "run.sh")).Perm(); m != 0o775 {
		t.Errorf("run.sh is %o, want 775 (group gets what the owner has)", m)
	}
	for _, p := range []string{".git/hooks", ".git/hooks/pre-commit", ".git/config", ".portenv", ".portenv/agents", ".portenv/agents/lead.md"} {
		m := mode(t, filepath.Join(project, p)).Perm()
		if m&0o022 != 0 {
			t.Errorf("%s is %o: writable by others than work", p, m)
		}
		if m&0o040 == 0 {
			t.Errorf("%s is %o: the team can't read it (CC-0493)", p, m)
		}
	}
	if m := mode(t, filepath.Join(project, ".git", "hooks", "pre-commit")).Perm(); m != 0o755 {
		t.Errorf("pre-commit is %o, want 755 (the team reads and runs it)", m)
	}
	if !slices.Contains(*chowns, chownCall{filepath.Join(project, "src", "app.ts"), -1, TeamGID}) {
		t.Errorf("app.ts not given to group team: %v", *chowns)
	}
	// Protected paths are the team's group to read, never given rwX.
	if !slices.Contains(*chowns, chownCall{filepath.Join(project, ".git", "config"), -1, TeamGID}) {
		t.Errorf(".git/config isn't in group team: %v", *chowns)
	}
	for _, p := range []string{".", "src", "src/app.ts", "run.sh", ".git"} {
		if !slices.Contains(run.acls, filepath.Join(project, p)) {
			t.Errorf("%s wasn't given g:team:rwX; gave %q", p, run.acls)
		}
	}
	for _, p := range []string{".git/hooks", ".git/hooks/pre-commit", ".git/config", ".portenv", ".portenv/agents", ".portenv/agents/lead.md"} {
		full := filepath.Join(project, p)
		if slices.Contains(run.acls, full) {
			t.Errorf("protected %s was given g:team:rwX", p)
		}
		if !slices.Contains(run.cleared, full) {
			t.Errorf("protected %s's ACL wasn't removed; removed %q", p, run.cleared)
		}
		if !slices.Contains(run.readable, full) {
			t.Errorf("protected %s wasn't given g:team:rX; gave %q", p, run.readable)
		}
	}
	for _, want := range []string{
		"runuser -u work -- " + teamIOBinary + " team-io open-project " + project,
		"git -C " + project + " config core.sharedRepository group",
		"git config --system --get-all safe.directory",
		"git config --system --add safe.directory " + "/home/work/shop",
	} {
		if !run.ran(want) {
			t.Errorf("didn't run %q; ran %q", want, run.calls)
		}
	}
}

func TestPrepareTeamProjectRefuses(t *testing.T) {
	ctx := context.Background()
	t.Run("an open dotfile folder is made private, and said", func(t *testing.T) {
		tu, _, _, project := newProjectTeam(t)
		ssh := filepath.Join(filepath.Dir(project), ".ssh")
		if err := os.Chmod(ssh, 0o755); err != nil {
			t.Fatal(err)
		}
		fixed, err := tu.PrepareProject(ctx, "/home/work/shop")
		if err != nil {
			t.Fatal(err)
		}
		if m := mode(t, ssh).Perm(); m != 0o750 {
			t.Errorf(".ssh is %o, want 750", m)
		}
		if !slices.ContainsFunc(fixed, func(s string) bool { return strings.Contains(s, ".ssh") }) {
			t.Errorf("fixed = %v, want a line naming .ssh", fixed)
		}
	})
	t.Run("no ACLs", func(t *testing.T) {
		tu, run, _, _ := newProjectTeam(t)
		run.noACL = true
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err == nil || !strings.Contains(err.Error(), "ACL") {
			t.Fatalf("err = %v, want a line about ACLs", err)
		}
	})
	t.Run("a symlinked project", func(t *testing.T) {
		tu, _, chowns, project := newProjectTeam(t)
		real := project + "-real"
		if err := os.Rename(project, real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, project); err != nil {
			t.Fatal(err)
		}
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err == nil {
			t.Error("prepared a symlinked project")
		}
		if len(*chowns) > 0 {
			t.Errorf("chowned: %v", *chowns)
		}
	})
	t.Run("a link inside is left alone", func(t *testing.T) {
		tu, _, chowns, project := newProjectTeam(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(project, "src", "out")); err != nil {
			t.Fatal(err)
		}
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		for _, c := range *chowns {
			if strings.HasPrefix(c.path, outside) || strings.HasSuffix(c.path, "/out") {
				t.Errorf("followed or changed a link: %v", c)
			}
		}
	})
	for _, bad := range []string{"/home/work", "/home/work/../root", "/etc", "/home/work/a/b", "/home/work/.ssh", "relative"} {
		tu, _, _, _ := newProjectTeam(t)
		if _, err := tu.PrepareProject(ctx, bad); err == nil {
			t.Errorf("PrepareProject(%q) accepted", bad)
		}
	}
}

// The first team start on a new workspace: group team doesn't exist yet
// (agent users, which used to make it, come after the project is opened).
// It is made, with its gid checks, and work joins it, before open-project.
func TestPrepareTeamProjectMakesTheTeamGroupFirst(t *testing.T) {
	ctx := context.Background()
	fresh := func(t *testing.T) (*TeamUsers, *projectRunner) {
		tu, run, _, _ := newProjectTeam(t)
		delete(run.sys.groups, TeamGroup)
		delete(run.sys.members, TeamGroup)
		return tu, run
	}
	t.Run("a new workspace", func(t *testing.T) {
		tu, run := fresh(t)
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		if gid, ok := run.sys.groups[TeamGroup]; !ok || gid != TeamGID {
			t.Fatalf("group %s = %d, %v; want gid %d", TeamGroup, gid, ok, TeamGID)
		}
		if !run.sys.members[TeamGroup]["work"] {
			t.Error("work isn't in group team")
		}
		at := func(prefix string) int {
			return slices.IndexFunc(run.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
		}
		order := []string{
			"getent group " + TeamGroup,
			"getent group 2000",
			"groupadd --gid 2000 " + TeamGroup,
			"usermod -a -G " + TeamGroup + " work",
			"runuser -u work -- " + teamIOBinary + " team-io open-project",
		}
		for i, c := range order {
			if at(c) < 0 {
				t.Fatalf("didn't run %q; ran %q", c, run.calls)
			}
			if i > 0 && at(order[i-1]) > at(c) {
				t.Errorf("%q ran after %q; ran %q", order[i-1], c, run.calls)
			}
		}
	})
	t.Run("the group exists but work isn't in it", func(t *testing.T) {
		// Without it, open-project (as work) can't chgrp to team.
		tu, run, _, _ := newProjectTeam(t)
		delete(run.sys.members, TeamGroup)
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		join := slices.Index(run.calls, "usermod -a -G "+TeamGroup+" work")
		open := slices.IndexFunc(run.calls, func(c string) bool { return strings.Contains(c, "team-io open-project") })
		if join < 0 || open < 0 || join > open {
			t.Errorf("work didn't join team before open-project; ran %q", run.calls)
		}
		if run.ran("groupadd") {
			t.Errorf("made the group again: %q", run.calls)
		}
	})
	t.Run("a group team with another gid is refused", func(t *testing.T) {
		tu, run := fresh(t)
		run.sys.groups[TeamGroup] = 2001
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err == nil || !strings.Contains(err.Error(), "gid 2001") {
			t.Fatalf("err = %v, want the wrong gid refused", err)
		}
		if run.ran("runuser") || run.ran("groupadd") {
			t.Errorf("went on: %q", run.calls)
		}
	})
	t.Run("gid 2000 held by another group is refused", func(t *testing.T) {
		tu, run := fresh(t)
		run.sys.groups["builders"] = TeamGID
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err == nil || !strings.Contains(err.Error(), "builders") {
			t.Fatalf("err = %v, want gid 2000's owner named", err)
		}
		if run.ran("runuser") || run.ran("groupadd") {
			t.Errorf("went on: %q", run.calls)
		}
	})
}

// Any other ACL error is said as it is, not as the no-ACL line.
func TestOpenProjectSaysWhatTheACLCallSaid(t *testing.T) {
	_, run, chowns, project := newProjectTeam(t)
	run.aclErr = syscall.EINVAL
	err := openProject(context.Background(), project, run, run.ops(chowns))
	if err == nil || errors.Is(err, errNoACL) || strings.Contains(err.Error(), "don't keep ACLs") {
		t.Fatalf("err = %v, want the call's own error, not the no-ACL line", err)
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("err = %v, want EINVAL in it", err)
	}
}

// CC-0494: once an agent has made a file in the project (lead ran git, so
// .git/index is lead's), the next start must not fail on it. What an agent
// owns is left as it is, folders with all they hold; the rest is opened.
func TestOpenProjectSkipsWhatAgentsOwn(t *testing.T) {
	_, run, chowns, project := newProjectTeam(t)
	for f, m := range map[string]os.FileMode{".git/index": 0o644, "src/lead.ts": 0o600, "notes/a.md": 0o644, "notes/deep/b.md": 0o644} {
		full := filepath.Join(project, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), m); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, m); err != nil {
			t.Fatal(err)
		}
	}
	run.foreign = map[string]int{".git/index": 2001, "src/lead.ts": 2001, "notes": 2002}
	before := map[string]os.FileMode{}
	for _, f := range []string{".git/index", "src/lead.ts", "notes", "notes/a.md", "notes/deep", "notes/deep/b.md"} {
		before[f] = mode(t, filepath.Join(project, f))
	}
	if err := openProject(context.Background(), project, run, run.ops(chowns)); err != nil {
		t.Fatalf("open-project failed on agents' files: %v", err)
	}
	for f, m := range before {
		full := filepath.Join(project, f)
		if got := mode(t, full); got != m {
			t.Errorf("%s changed: %v → %v", f, m, got)
		}
		if slices.Contains(run.acls, full) || slices.ContainsFunc(*chowns, func(c chownCall) bool { return c.path == full }) {
			t.Errorf("%s (an agent's, or in an agent's folder) was changed", f)
		}
	}
	// The rest still opened.
	if !slices.Contains(run.acls, filepath.Join(project, "src", "app.ts")) {
		t.Errorf("app.ts wasn't opened: %q", run.acls)
	}
}

// A protected path work doesn't own could hold an agent's hook or config:
// the team isn't started, with a line naming it.
func TestOpenProjectRefusesAProtectedPathWorkDoesntOwn(t *testing.T) {
	for _, p := range []string{".git/config", ".git/hooks/pre-commit", ".portenv/agents/lead.md", ".git/hooks"} {
		t.Run(p, func(t *testing.T) {
			_, run, chowns, project := newProjectTeam(t)
			run.foreign = map[string]int{p: 2001}
			err := openProject(context.Background(), project, run, run.ops(chowns))
			if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "uid 2001") {
				t.Fatalf("err = %v, want %s named as uid 2001's", err, p)
			}
		})
	}
}

// A protected path that's a link is refused (git would follow it); a hard
// link to a protected file elsewhere stays closed.
func TestOpenProjectProtectsByIdentity(t *testing.T) {
	t.Run("a link at .git/config", func(t *testing.T) {
		_, run, chowns, project := newProjectTeam(t)
		cfg := filepath.Join(project, ".git", "config")
		if err := os.Rename(cfg, filepath.Join(project, "src", "config")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../src/config", cfg); err != nil {
			t.Fatal(err)
		}
		if err := openProject(context.Background(), project, run, run.ops(chowns)); err == nil || !strings.Contains(err.Error(), "link") {
			t.Fatalf("err = %v, want the link refused", err)
		}
		if len(*chowns) > 0 || len(run.acls) > 0 {
			t.Errorf("changed files before refusing: %v %q", *chowns, run.acls)
		}
	})
	t.Run("a link at .git", func(t *testing.T) {
		_, run, chowns, project := newProjectTeam(t)
		git := filepath.Join(project, ".git")
		if err := os.Rename(git, filepath.Join(project, "src", "git")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("src/git", git); err != nil {
			t.Fatal(err)
		}
		if err := openProject(context.Background(), project, run, run.ops(chowns)); err == nil || !strings.Contains(err.Error(), ".git") {
			t.Fatalf("err = %v, want the link refused", err)
		}
	})
	t.Run("a hard link to .git/config", func(t *testing.T) {
		_, run, chowns, project := newProjectTeam(t)
		other := filepath.Join(project, "src", "cfg")
		if err := os.Link(filepath.Join(project, ".git", "config"), other); err != nil {
			t.Fatal(err)
		}
		if err := openProject(context.Background(), project, run, run.ops(chowns)); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(run.acls, other) {
			t.Error("a hard link to .git/config was given g:team:rwX")
		}
		if !slices.Contains(run.readable, other) {
			t.Error("a hard link to .git/config wasn't closed like it")
		}
		if m := mode(t, other).Perm(); m&0o022 != 0 {
			t.Errorf("the hard link is %o: writable by others than work", m)
		}
	})
}

func TestTheChannelRelaysPrepareToInit(t *testing.T) {
	var got string
	s := &apiServer{prepareProject: func(_ context.Context, dir string) ([]string, error) { got = dir; return nil, nil }}
	if _, err := s.PrepareTeamProject(context.Background(), &agentv1.PrepareTeamProjectRequest{ProjectDir: "/home/work/shop"}); err != nil || got != "/home/work/shop" {
		t.Errorf("relay: %q, %v", got, err)
	}
	if _, err := (&apiServer{}).PrepareTeamProject(context.Background(), &agentv1.PrepareTeamProjectRequest{}); err == nil {
		t.Error("a server without init's relay prepared a project")
	}
}

// R-0262: everything directly in /home/work, except the project and
// .portenv, loses its "other" bits and never belongs to group team;
// links are left alone; what changed is reported.
func TestTheOwnersHomeIsTightened(t *testing.T) {
	tu, _, chowns, project := newProjectTeam(t)
	work := filepath.Dir(project)
	for f, m := range map[string]os.FileMode{".npmrc": 0o644, ".netrc": 0o604, ".gitconfig": 0o664, ".bash_history": 0o600} {
		if err := os.WriteFile(filepath.Join(work, f), []byte("x"), m); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(work, f), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(work, ".docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(work, ".docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".portenv"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	tu.groupOf = func(string) int { return TeamGID } // pretend every entry is in group team
	fixed, err := tu.tightenHome(work, []string{"shop"})
	if err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]os.FileMode{".npmrc": 0o640, ".netrc": 0o600, ".gitconfig": 0o640, ".bash_history": 0o600, ".docker": 0o750} {
		got := mode(t, filepath.Join(work, f)).Perm()
		if got != want {
			t.Errorf("%s is %o, want %o", f, got, want)
		}
	}
	if m := mode(t, filepath.Join(work, ".portenv")).Perm(); m != 0o755 {
		t.Errorf(".portenv changed to %o", m)
	}
	if m := mode(t, project).Perm(); m&0o005 == 0 && m != mode(t, project).Perm() {
		t.Error("the project was changed")
	}
	if b, _ := os.Stat(outside); b.Mode().Perm() != 0o644 {
		t.Errorf("a link's target was changed: %o", b.Mode().Perm())
	}
	for _, c := range *chowns {
		if c.gid == TeamGID {
			t.Errorf("gave %s to team", c.path)
		}
		if filepath.Base(c.path) == "shop" || filepath.Base(c.path) == ".portenv" || filepath.Base(c.path) == "link" {
			t.Errorf("changed %s", c.path)
		}
	}
	if !slices.ContainsFunc(*chowns, func(c chownCall) bool { return filepath.Base(c.path) == ".npmrc" && c.gid == 1000 }) {
		t.Errorf(".npmrc wasn't given back to group work: %v", *chowns)
	}
	if len(fixed) == 0 || !slices.ContainsFunc(fixed, func(s string) bool { return strings.Contains(s, ".npmrc") }) {
		t.Errorf("fixed = %v", fixed)
	}
	// A second pass finds nothing to fix.
	tu.groupOf = func(string) int { return 1000 }
	if again, _ := tu.tightenHome(work, []string{"shop"}); len(again) != 0 {
		t.Errorf("second pass fixed %v", again)
	}
}

// R-0262 item 3: a box start with a team re-checks the home and keeps what
// it fixed for the next team start's notice; without a team, nothing.
func TestTheHomeIsRecheckedAtBoxStart(t *testing.T) {
	tu, sys, _ := newTestTeam(t)
	ctx := context.Background()
	work := filepath.Join(tu.Cfg.HomeRoot, "work")
	if err := os.WriteFile(filepath.Join(work, ".npmrc"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(work, ".npmrc"), 0o644); err != nil {
		t.Fatal(err)
	}
	tu.Restore(ctx) // no team yet
	if m := mode(t, filepath.Join(work, ".npmrc")).Perm(); m != 0o644 {
		t.Errorf("changed without a team: %o", m)
	}
	if _, err := tu.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	_ = sys
	tu.Restore(ctx)
	if err := tu.RestoreErr(); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, filepath.Join(work, ".npmrc")).Perm(); m != 0o640 {
		t.Errorf("after a box start with a team: %o, want 640", m)
	}
	if got := tu.takeHomeFixes(); len(got) == 0 {
		t.Error("the box start's fix wasn't kept for the notice")
	}
	if got := tu.takeHomeFixes(); len(got) != 0 {
		t.Errorf("fixes reported twice: %v", got)
	}
}

// What a box start fixed reaches the next team start's answer (and so the
// owner's notice), once.
func TestBoxStartFixesReachTheNextTeamStart(t *testing.T) {
	tu, _, _, project := newProjectTeam(t)
	ctx := context.Background()
	tu.homeFixes = []string{"~/.npmrc: 644 → 640"}
	fixed, err := tu.PrepareProject(ctx, "/home/work/shop")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fixed, "~/.npmrc: 644 → 640") {
		t.Errorf("fixed = %v", fixed)
	}
	again, _ := tu.PrepareProject(ctx, "/home/work/shop")
	if slices.Contains(again, "~/.npmrc: 644 → 640") {
		t.Error("reported twice")
	}
	_ = project
}

// CC-0493: git's core.sharedRepository write makes a new .git/config (the
// owner's session saw 0600, with a --- mask for the team); it's closed
// again after, readable by the team and not writable.
func TestOpenProjectLetsTheTeamReadGitConfig(t *testing.T) {
	_, run, chowns, project := newProjectTeam(t)
	cfg := filepath.Join(project, ".git", "config")
	rewrote := false
	run.git = func(args []string) {
		if !slices.Contains(args, "core.sharedRepository") {
			return
		}
		rewrote = true
		tmp := cfg + ".lock"
		if err := os.WriteFile(tmp, []byte("[core]\n\tsharedRepository = group\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(tmp, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, cfg); err != nil {
			t.Fatal(err)
		}
		run.readable = nil
	}
	if err := openProject(context.Background(), project, run, run.ops(chowns)); err != nil {
		t.Fatal(err)
	}
	if !rewrote {
		t.Fatal("git didn't write core.sharedRepository")
	}
	if m := mode(t, cfg).Perm(); m != 0o640 {
		t.Errorf(".git/config is %o after git's write, want 640", m)
	}
	// run.git cleared the list: this is the pass after git's write.
	if !slices.Contains(run.readable, cfg) {
		t.Errorf(".git/config wasn't given g:team:r after git's write: %q", run.readable)
	}
}

// makeWorktree lays out what "git worktree add .worktrees/<name>" makes in
// the project, with the paths git writes in the box (/home/work/shop).
func makeWorktree(t *testing.T, project, name string) {
	t.Helper()
	wt := filepath.Join(project, ".worktrees", name)
	reg := filepath.Join(project, ".git", "worktrees", name)
	for _, d := range []string{wt, reg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for f, b := range map[string]string{
		filepath.Join(reg, "gitdir"): "/home/work/shop/.worktrees/" + name + "/.git\n",
		filepath.Join(wt, ".git"):    "gitdir: /home/work/shop/.git/worktrees/" + name + "\n",
	} {
		if err := os.WriteFile(f, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// CC-0493: work's worktrees inside the project are trusted for agents'
// git, each by name and once; nothing else under the project is.
func TestPrepareTeamProjectTrustsWorksWorktrees(t *testing.T) {
	ctx := context.Background()
	added := func(run *projectRunner) []string {
		var out []string
		for _, c := range run.calls {
			if d, ok := strings.CutPrefix(c, "git config --system --add safe.directory "); ok {
				out = append(out, d)
			}
		}
		return out
	}
	t.Run("a worktree work made", func(t *testing.T) {
		tu, run, _, project := newProjectTeam(t)
		makeWorktree(t, project, "writer")
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		if got := added(run); !slices.Equal(got, []string{"/home/work/shop", "/home/work/shop/.worktrees/writer"}) {
			t.Errorf("added %q", got)
		}
		for _, c := range run.calls {
			if strings.Contains(c, "safe.directory") && strings.Contains(c, "*") {
				t.Errorf("a wildcard: %q", c)
			}
		}
	})
	t.Run("already trusted: not added again", func(t *testing.T) {
		tu, run, _, project := newProjectTeam(t)
		makeWorktree(t, project, "writer")
		run.safeDirs = "/home/work/shop\n/home/work/shop/.worktrees/writer\n"
		if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		if got := added(run); len(got) != 0 {
			t.Errorf("added again: %q", got)
		}
	})
	for name, spoil := range map[string]func(t *testing.T, tu *TeamUsers, project string){
		"an agent's folder": func(t *testing.T, tu *TeamUsers, project string) {
			wt := filepath.Join(project, ".worktrees", "writer")
			tu.ownerOf = func(p string) int {
				if p == wt {
					return 2001
				}
				return 1000
			}
		},
		"an agent's .git file": func(t *testing.T, tu *TeamUsers, project string) {
			f := filepath.Join(project, ".worktrees", "writer", ".git")
			tu.ownerOf = func(p string) int {
				if p == f {
					return 2001
				}
				return 1000
			}
		},
		"an agent's registration": func(t *testing.T, tu *TeamUsers, project string) {
			reg := filepath.Join(project, ".git", "worktrees", "writer")
			tu.ownerOf = func(p string) int {
				if p == reg {
					return 2001
				}
				return 1000
			}
		},
		"registered outside the project": func(t *testing.T, _ *TeamUsers, project string) {
			writeFile(t, filepath.Join(project, ".git", "worktrees", "writer", "gitdir"), "/home/work/.ssh/.git\n")
		},
		"registered inside .git": func(t *testing.T, _ *TeamUsers, project string) {
			writeFile(t, filepath.Join(project, ".git", "worktrees", "writer", "gitdir"), "/home/work/shop/.git/hooks/.git\n")
		},
		"a path with ..": func(t *testing.T, _ *TeamUsers, project string) {
			writeFile(t, filepath.Join(project, ".git", "worktrees", "writer", "gitdir"), "/home/work/shop/../shop/.worktrees/writer/.git\n")
		},
		"a .git file pointing elsewhere": func(t *testing.T, _ *TeamUsers, project string) {
			writeFile(t, filepath.Join(project, ".worktrees", "writer", ".git"), "gitdir: /home/team/evil\n")
		},
		"a link on the way": func(t *testing.T, _ *TeamUsers, project string) {
			wts := filepath.Join(project, ".worktrees")
			if err := os.Rename(wts, project+"-wts"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(project+"-wts", wts); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			tu, run, _, project := newProjectTeam(t)
			makeWorktree(t, project, "writer")
			spoil(t, tu, project)
			if _, err := tu.PrepareProject(ctx, "/home/work/shop"); err != nil {
				t.Fatal(err)
			}
			if got := added(run); !slices.Equal(got, []string{"/home/work/shop"}) {
				t.Errorf("added %q, want the project only", got)
			}
		})
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
