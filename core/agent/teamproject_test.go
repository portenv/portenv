// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
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
// group commands from sys. Its ops are open-project's file looks and
// changes, faked: a test can't own another uid's file or count on ACLs.
// foreign marks paths (relative to the project) another uid owns;
// writable marks paths another uid could write.
type projectRunner struct {
	calls    []string
	noACL    bool
	aclErr   error
	open     func(project string) ([]byte, error)
	sys      *fakeSystem
	readOnly []string // paths given the team's read and no one else's write
	foreign  map[string]int
	writable map[string]bool
	shared   string   // what git config --get core.sharedRepository says
	safeDirs []string // the system config's safe.directory entries
	project  string
	found    string // open-project's last JSON line
}

func (r *projectRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	switch name {
	case "runuser":
		if i := slices.Index(args, "open-project"); i >= 0 && r.open != nil {
			return r.open(args[i+1])
		}
		if slices.Contains(args, "--unset-all") && slices.Contains(args, "core.sharedRepository") && r.shared == "" {
			return nil, errors.New("exit status 5") // not set
		}
		return nil, nil
	case "git":
		switch {
		case slices.Contains(args, "core.sharedRepository"):
			if r.shared == "" {
				return nil, errors.New("exit status 1")
			}
			return []byte(r.shared + "\n"), nil
		case slices.Contains(args, "safe.directory") && slices.Contains(args, "--get-all"):
			if len(r.safeDirs) == 0 {
				return nil, errors.New("exit status 1")
			}
			return []byte(strings.Join(r.safeDirs, "\n") + "\n"), nil
		}
		return nil, nil
	}
	return r.sys.Run(context.Background(), nil, name, args...)
}

// lastFound is open-project's last JSON line.
func (r *projectRunner) lastFound(t *testing.T) string {
	t.Helper()
	if r.found == "" {
		t.Fatal("open-project didn't run")
	}
	return r.found
}

func (r *projectRunner) rel(f *os.File) string {
	rel, err := filepath.Rel(r.project, f.Name())
	if err != nil {
		return f.Name()
	}
	return rel
}

// ops are fake projectOps recording into r.
func (r *projectRunner) ops() projectOps {
	return projectOps{
		self: 1000,
		owner: func(rel string, _ *unix.Stat_t) int {
			if uid, ok := r.foreign[rel]; ok {
				return uid
			}
			return 1000
		},
		othersWrite: func(f *os.File, _ *unix.Stat_t, _ bool) (bool, error) {
			return r.writable[r.rel(f)], nil
		},
		readOnly: func(f *os.File, _ bool) error {
			if r.noACL {
				return syscall.EOPNOTSUPP
			}
			if r.aclErr != nil {
				return r.aclErr
			}
			r.readOnly = append(r.readOnly, r.rel(f))
			return nil
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
	run := &projectRunner{sys: newFakeSystem(), project: project}
	run.sys.groups[TeamGroup] = TeamGID
	run.sys.members[TeamGroup] = map[string]bool{"work": true}
	var chowns []chownCall
	tu := &TeamUsers{
		Cfg:        Config{User: "work", UID: 1000, HomeRoot: root},
		Run:        run,
		Chown:      func(p string, uid, gid int) error { chowns = append(chowns, chownCall{p, uid, gid}); return nil },
		teamDirACL: func(string, int) error { return nil },
	}
	// team-io open-project, as work, in this process: its JSON line.
	run.open = func(project string) ([]byte, error) {
		found, err := openProject(context.Background(), project, run, run.ops(), slices.Contains(run.calls[len(run.calls)-1:], "runuser -u work -- "+teamIOBinary+" team-io open-project "+project+" look"))
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(found)
		run.found = string(b)
		return b, err
	}
	return tu, run, &chowns, project
}

// prepare is PrepareProject for a team whose move to its own clones
// isn't what the test is about: no agents.
func prepare(tu *TeamUsers, ctx context.Context, dir string) ([]string, error) { //nolint:revive // the test's order
	fixed, items, err := tu.PrepareProject(ctx, dir, PrepareOptions{HomeQuota: 1 << 30})
	if err == nil && len(items) > 0 {
		return nil, &SharedProjectError{Found: ProjectFindings{Items: []ProjectFinding{{Path: items[0].GetPath(), What: items[0].GetKind()}}}}
	}
	return fixed, err
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

// ADR 0022 §2a (R-0315): every file and folder of work's in the project,
// .git and .portenv included, gets the team's read and no one else's
// write; nothing is given to group team; no core.sharedRepository and no
// system safe.directory.
func TestPrepareTeamProjectMakesTheProjectReadOnlyForTheTeam(t *testing.T) {
	tu, run, chowns, project := newProjectTeam(t)
	if _, err := prepare(tu, context.Background(), "/home/work/shop"); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, filepath.Dir(project)).Perm(); m != 0o711 {
		t.Errorf("/home/work is %o, want 711", m)
	}
	for _, p := range []string{".", "src", "src/app.ts", "run.sh", ".git", ".git/config", ".git/hooks", ".git/hooks/pre-commit", ".portenv", ".portenv/agents", ".portenv/agents/lead.md"} {
		if !slices.Contains(run.readOnly, p) {
			t.Errorf("%s wasn't made read-only for the team; made %q", p, run.readOnly)
		}
	}
	for _, c := range *chowns {
		if strings.HasPrefix(c.path, project) {
			t.Errorf("a file in the project changed hands: %v", c)
		}
	}
	if !run.ran("runuser -u work -- " + teamIOBinary + " team-io open-project " + project) {
		t.Errorf("didn't run open-project as work; ran %q", run.calls)
	}
	for _, c := range run.calls {
		if strings.Contains(c, "--add safe.directory") || strings.Contains(c, "sharedRepository group") || strings.Contains(c, "--system --add") {
			t.Errorf("ran %q: Portenv adds no safe.directory and no shared repository (R-0315)", c)
		}
	}
}

// What an agent could have changed in what work's git reads stops the
// team's start, named, and nothing is changed: a file that isn't work's
// (anywhere in the project, with all under it), anything in a .git
// another uid could write, core.sharedRepository, and the system config's
// safe.directory entries for the project.
func TestPrepareTeamProjectRefusesTheSharedProject(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		set  func(r *projectRunner)
		want string
	}{
		"an agent's file in .git":                 {func(r *projectRunner) { r.foreign = map[string]int{".git/index": 2001} }, ".git/index (uid 2001's)"},
		"an agent's hook":                         {func(r *projectRunner) { r.foreign = map[string]int{".git/hooks/pre-commit": 2002} }, ".git/hooks/pre-commit (uid 2002's)"},
		"an agent's file in the tree":             {func(r *projectRunner) { r.foreign = map[string]int{"src/lead.ts": 2001} }, "src/lead.ts (uid 2001's)"},
		"a .git/config others can write":          {func(r *projectRunner) { r.writable = map[string]bool{".git/config": true} }, ".git/config (others can write it)"},
		"a hooks folder others can write":         {func(r *projectRunner) { r.writable = map[string]bool{".git/hooks": true} }, ".git/hooks (others can write it)"},
		"a nested repository's config":            {func(r *projectRunner) { r.writable = map[string]bool{"src/.git": true} }, "src/.git (others can write it)"},
		"core.sharedRepository":                   {func(r *projectRunner) { r.shared = "group" }, "core.sharedRepository"},
		"Portenv's safe.directory":                {func(r *projectRunner) { r.safeDirs = []string{"/home/work/shop"} }, "trusts /home/work/shop"},
		"Portenv's safe.directory for a worktree": {func(r *projectRunner) { r.safeDirs = []string{"/srv/x", "/home/work/shop/.worktrees/x"} }, "trusts /home/work/shop/.worktrees/x"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tu, run, _, project := newProjectTeam(t)
			if err := os.MkdirAll(filepath.Join(project, "src", ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			c.set(run)
			tu.migrate = &migrateOps{running: func() ([]int, error) { return []int{2001}, nil }}
			var found ProjectFindings
			tu.onMove = func(f ProjectFindings) { found = f }
			for rel := range run.foreign {
				if err := os.WriteFile(filepath.Join(project, rel), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := prepare(tu, ctx, "/home/work/shop")
			if !errors.Is(err, errAgentsRunning) {
				t.Fatalf("err = %v, want the move to start (and wait for the agents to stop)", err)
			}
			if got := (&SharedProjectError{Found: found}).Error(); !strings.Contains(got, c.want) {
				t.Errorf("found %s, want %q in it", got, c.want)
			}
			if len(run.readOnly) > 0 {
				t.Errorf("changed files before refusing: %q", run.readOnly)
			}
		})
	}
	t.Run("an agent's folder is listed once, with all it holds", func(t *testing.T) {
		tu, run, _, project := newProjectTeam(t)
		if err := os.MkdirAll(filepath.Join(project, "notes", "deep"), 0o755); err != nil {
			t.Fatal(err)
		}
		run.foreign = map[string]int{"notes": 2002}
		tu.migrate = &migrateOps{running: func() ([]int, error) { return []int{2002}, nil }}
		if _, err := prepare(tu, ctx, "/home/work/shop"); !errors.Is(err, errAgentsRunning) {
			t.Fatalf("err = %v", err)
		}
		if found := run.lastFound(t); found != `{"items":[{"path":"notes","what":"owner","uid":2002,"dir":true}]}` {
			t.Fatalf("found %s, want notes alone", found)
		}
	})
	t.Run("a safe.directory elsewhere isn't Portenv's", func(t *testing.T) {
		tu, run, _, _ := newProjectTeam(t)
		run.safeDirs = []string{"/srv/repo", "/home/work/shopping"}
		if _, err := prepare(tu, ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("core.sharedRepository false is fine", func(t *testing.T) {
		tu, run, _, _ := newProjectTeam(t)
		run.shared = "false"
		if _, err := prepare(tu, ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("files outside .git that others can write are made read-only, not refused", func(t *testing.T) {
		tu, run, _, _ := newProjectTeam(t)
		run.writable = map[string]bool{"src/app.ts": true, "src": true}
		if _, err := prepare(tu, ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(run.readOnly, "src/app.ts") {
			t.Errorf("src/app.ts wasn't made read-only: %q", run.readOnly)
		}
	})
}

func TestPrepareTeamProjectRefuses(t *testing.T) {
	ctx := context.Background()
	t.Run("an open dotfile folder is made private, and said", func(t *testing.T) {
		tu, _, _, project := newProjectTeam(t)
		ssh := filepath.Join(filepath.Dir(project), ".ssh")
		if err := os.Chmod(ssh, 0o755); err != nil {
			t.Fatal(err)
		}
		fixed, err := prepare(tu, ctx, "/home/work/shop")
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
		if _, err := prepare(tu, ctx, "/home/work/shop"); err == nil || !strings.Contains(err.Error(), "ACL") {
			t.Fatalf("err = %v, want a line about ACLs", err)
		}
	})
	t.Run("a symlinked project", func(t *testing.T) {
		tu, run, _, project := newProjectTeam(t)
		real := project + "-real"
		if err := os.Rename(project, real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, project); err != nil {
			t.Fatal(err)
		}
		if _, err := prepare(tu, ctx, "/home/work/shop"); err == nil {
			t.Error("prepared a symlinked project")
		}
		if len(run.readOnly) > 0 {
			t.Errorf("changed: %q", run.readOnly)
		}
	})
	t.Run("a link inside is left alone", func(t *testing.T) {
		tu, run, _, project := newProjectTeam(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(project, "src", "out")); err != nil {
			t.Fatal(err)
		}
		if _, err := prepare(tu, ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		for _, p := range run.readOnly {
			if strings.HasPrefix(p, "..") || p == "src/out" {
				t.Errorf("followed or changed a link: %v", p)
			}
		}
	})
	t.Run("a link at .git in a project is left as it is, and its target isn't walked", func(t *testing.T) {
		tu, run, _, project := newProjectTeam(t)
		git := filepath.Join(project, ".git")
		if err := os.Rename(git, filepath.Join(project, "src", "git")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("src/git", git); err != nil {
			t.Fatal(err)
		}
		if _, err := prepare(tu, ctx, "/home/work/shop"); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(run.readOnly, ".git") || slices.Contains(run.readOnly, ".git/config") {
			t.Errorf("walked through the link: %q", run.readOnly)
		}
	})
	for _, bad := range []string{"/home/work", "/home/work/../root", "/etc", "/home/work/a/b", "/home/work/.ssh", "relative"} {
		tu, _, _, _ := newProjectTeam(t)
		if _, err := prepare(tu, ctx, bad); err == nil {
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
		if _, err := prepare(tu, ctx, "/home/work/shop"); err != nil {
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
		if _, err := prepare(tu, ctx, "/home/work/shop"); err != nil {
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
		if _, err := prepare(tu, ctx, "/home/work/shop"); err == nil || !strings.Contains(err.Error(), "gid 2001") {
			t.Fatalf("err = %v, want the wrong gid refused", err)
		}
		if run.ran("runuser") || run.ran("groupadd") {
			t.Errorf("went on: %q", run.calls)
		}
	})
	t.Run("gid 2000 held by another group is refused", func(t *testing.T) {
		tu, run := fresh(t)
		run.sys.groups["builders"] = TeamGID
		if _, err := prepare(tu, ctx, "/home/work/shop"); err == nil || !strings.Contains(err.Error(), "builders") {
			t.Fatalf("err = %v, want gid 2000's owner named", err)
		}
		if run.ran("runuser") || run.ran("groupadd") {
			t.Errorf("went on: %q", run.calls)
		}
	})
}

// Any other ACL error is said as it is, not as the no-ACL line.
func TestOpenProjectSaysWhatTheACLCallSaid(t *testing.T) {
	_, run, _, project := newProjectTeam(t)
	run.aclErr = syscall.EINVAL
	_, err := openProject(context.Background(), project, run, run.ops(), false)
	if err == nil || errors.Is(err, errNoACL) || strings.Contains(err.Error(), "don't keep ACLs") {
		t.Fatalf("err = %v, want the call's own error, not the no-ACL line", err)
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("err = %v, want EINVAL in it", err)
	}
}

func TestTheChannelRelaysPrepareToInit(t *testing.T) {
	var got string
	s := &apiServer{prepareProject: func(_ context.Context, r *agentv1.PrepareTeamProjectRequest) (*agentv1.PrepareTeamProjectResponse, error) {
		got = r.GetProjectDir()
		return &agentv1.PrepareTeamProjectResponse{}, nil
	}}
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
	fixed, err := prepare(tu, ctx, "/home/work/shop")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fixed, "~/.npmrc: 644 → 640") {
		t.Errorf("fixed = %v", fixed)
	}
	again, _ := prepare(tu, ctx, "/home/work/shop")
	if slices.Contains(again, "~/.npmrc: 644 → 640") {
		t.Error("reported twice")
	}
	_ = project
}
