// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// projectRunner answers setfacl, getfacl, runuser and git like a box with
// ACLs (unless noACL).
type projectRunner struct {
	calls []string
	noACL bool
	open  func(project string) error
}

func (r *projectRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	switch name {
	case "setfacl":
		if r.noACL {
			return []byte("setfacl: Operation not supported"), errors.New("exit status 1")
		}
		return nil, nil
	case "getfacl":
		if r.noACL {
			return nil, errors.New("exit status 1")
		}
		return []byte("default:group:team:rwx\n"), nil
	case "runuser":
		if i := slices.Index(args, "open-project"); i >= 0 && r.open != nil {
			return nil, r.open(args[i+1])
		}
		return nil, nil
	case "git":
		return nil, nil
	}
	return nil, errors.New("unexpected " + call)
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
	run := &projectRunner{}
	var chowns []chownCall
	tu := &TeamUsers{
		Cfg:   Config{User: "work", UID: 1000, HomeRoot: root},
		Run:   run,
		Chown: func(p string, uid, gid int) error { chowns = append(chowns, chownCall{p, uid, gid}); return nil },
	}
	// team-io open-project, as work, in this process.
	run.open = func(project string) error {
		return openProject(context.Background(), project, run, func(r *os.Root, rel string, uid, gid int) error {
			chowns = append(chowns, chownCall{filepath.Join(project, rel), uid, gid})
			return nil
		})
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
		if m := mode(t, filepath.Join(project, p)).Perm(); m&0o020 != 0 {
			t.Errorf("%s is %o: group-writable", p, m)
		}
	}
	if !slices.Contains(*chowns, chownCall{filepath.Join(project, "src", "app.ts"), -1, TeamGID}) {
		t.Errorf("app.ts not given to group team: %v", *chowns)
	}
	for _, c := range *chowns {
		if strings.Contains(c.path, "/.git/hooks") || strings.HasSuffix(c.path, "/.git/config") || strings.Contains(c.path, "/.portenv") {
			t.Errorf("a protected path was given to team: %v", c)
		}
	}
	for _, want := range []string{
		"runuser -u work -- " + teamIOBinary + " team-io open-project " + project,
		"setfacl -R -P -m g:team:rwX,d:g:team:rwX " + project,
		"setfacl -R -P -b " + filepath.Join(project, ".git", "hooks"),
		"setfacl -b " + filepath.Join(project, ".git", "config"),
		"setfacl -R -P -b " + filepath.Join(project, ".portenv"),
		"git -C " + project + " config core.sharedRepository group",
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
