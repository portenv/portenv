// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// oldTeam is a workspace prepared the old way (the shared project), with
// a real git repository, two agents (lead 2001, builder 2002) and fakes
// for what only root can do: who owns what (foreign, relative to the
// project), and the changes recorded instead of made.
type oldTeam struct {
	t        *testing.T
	root     string
	project  string
	tu       *TeamUsers
	run      *projectRunner
	running  []int
	reowned  []string
	links    []string
	asAgent  []string // "uid op"
	agentErr error
}

func newOldTeam(t *testing.T) *oldTeam {
	t.Helper()
	tu, run, _, _ := newProjectTeam(t)
	root, err := filepath.EvalSymlinks(tu.Cfg.HomeRoot)
	if err != nil {
		t.Fatal(err)
	}
	tu.Cfg.HomeRoot = root
	project := filepath.Join(root, "work", "shop")
	run.project = project
	o := &oldTeam{t: t, root: root, project: project, tu: tu, run: run}
	if err := os.WriteFile(filepath.Join(root, ".portenv", "team", "users.json"), []byte(`{"version":1,"users":[{"id":"lead","uid":2001},{"id":"builder","uid":2002}]}`), 0o644); err != nil {
		if err := os.MkdirAll(filepath.Join(root, ".portenv", "team"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".portenv", "team", "users.json"), []byte(`{"version":1,"users":[{"id":"lead","uid":2001},{"id":"builder","uid":2002}]}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"lead", "builder"} {
		if err := os.Mkdir(filepath.Join(root, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A real repository: the project's .git from newProjectTeam is a stub.
	if err := os.RemoveAll(filepath.Join(project, ".git")); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		gitOut(t, root, project, args...)
	}
	git("init", "--quiet", "--initial-branch=main")
	git("add", "-A")
	git("-c", "user.name=O", "-c", "user.email=o@example.invalid", "commit", "--quiet", "-m", "first")
	// R-0280's worktree for builder, where builder left uncommitted work.
	git("worktree", "add", "--quiet", ".worktrees/builder-old", "-b", "agent/builder")
	for f, c := range map[string]string{
		".worktrees/builder-old/wip.txt": "builder's work in progress\n",
		".git/hooks/post-checkout":       "#!/bin/sh\necho builder's hook\n",
		"lead-notes.txt":                 "lead's notes\n",
		"vendor/x/.git/config":           "[core]\n",
		"vendor/x/a.txt":                 "lead's repository\n",
	} {
		full := filepath.Join(project, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run.foreign = map[string]int{
		".worktrees/builder-old/wip.txt": 2002,
		".git/hooks/post-checkout":       2002,
		"lead-notes.txt":                 2001,
		"vendor/x":                       2001,
		"vendor/x/.git":                  2001,
		"vendor/x/a.txt":                 2001,
	}
	run.writable = map[string]bool{".git/config": true} // group team could write it
	run.shared = "group"
	run.safeDirs = []string{"/home/work/shop", "/home/work/shop/.worktrees/builder-old"}
	uidOf := func(full string) int {
		rel, err := filepath.Rel(project, full)
		if err != nil {
			return 1000
		}
		if uid, ok := run.foreign[rel]; ok {
			return uid
		}
		return 1000
	}
	tu.migrate = &migrateOps{
		owner:       func(rel string, _ *unix.Stat_t) int { return uidOf(filepath.Join(project, rel)) },
		othersWrite: func(f *os.File, _ *unix.Stat_t, _ bool) (bool, error) { return run.writable[run.rel(f)], nil },
		reown: func(f *os.File, _ bool) error {
			o.reowned = append(o.reowned, run.rel(f))
			return nil
		},
		reownLink: func(dir *os.File, name string) error {
			o.links = append(o.links, filepath.Join(run.rel(dir), name))
			return nil
		},
		running: func() ([]int, error) { return o.running, nil },
		asAgent: func(ctx context.Context, uid int, home string, args []string, stdin io.Reader) ([]byte, error) {
			o.asAgent = append(o.asAgent, strings.Join([]string{filepath.Base(home), args[0]}, " "))
			if o.agentErr != nil {
				return nil, o.agentErr
			}
			var out bytes.Buffer
			var err error
			switch args[0] {
			case "clone-own":
				var calls []string
				err = cloneOwn(ctx, args[1], args[2], args[3], args[4], testGit(t, home, &calls), &out)
			case "migrate-copy":
				owns := func(full string, _ *unix.Stat_t) bool { return uidOf(full) == uid }
				err = migrateCopy(args[1], args[2], args[3], args[4], owns, stdin, &out)
			default:
				t.Errorf("as an agent: %q", args)
			}
			return out.Bytes(), err
		},
	}
	return o
}

func (o *oldTeam) prepare() ([]*typesv1.MigrationItem, error) {
	_, items, err := o.tu.PrepareProject(context.Background(), "/home/work/shop", PrepareOptions{CloneAgents: []string{"lead", "builder"}, HomeQuota: 1 << 30})
	return items, err
}

func itemByPath(items []*typesv1.MigrationItem, p string) *typesv1.MigrationItem {
	for _, i := range items {
		if i.GetPath() == p {
			return i
		}
	}
	return nil
}

// ADR 0022 §2a point 6: the record, each agent's clone with its files,
// .git and the tree back to work, the settings gone; nothing deleted.
func TestTheMoveToAgentsOwnClones(t *testing.T) {
	o := newOldTeam(t)
	items, err := o.prepare()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		".git/hooks/post-checkout": "git builder",
		".git/config":              "git ",
		".worktrees/builder-old":   "worktree builder",
		"lead-notes.txt":           "file lead",
		"vendor/x":                 "repo lead",
	}
	for p, w := range want {
		i := itemByPath(items, p)
		if i == nil || i.GetKind()+" "+i.GetAgent() != w || !i.GetChoice() {
			t.Errorf("%s: %v, want %q, a choice", p, i, w)
		}
	}
	var settings []string
	for _, i := range items {
		if i.GetKind() == "setting" {
			settings = append(settings, i.GetPath())
			if i.GetChoice() {
				t.Errorf("a setting is a choice: %v", i)
			}
		}
		if strings.Contains(i.GetPath(), ".sample") {
			t.Errorf("a sample hook was recorded: %v", i)
		}
	}
	if len(settings) != 3 {
		t.Errorf("settings %q, want sharedRepository and the two safe.directory entries", settings)
	}
	// Each agent's clone, with the files it made: never committed.
	if got, err := os.ReadFile(filepath.Join(o.root, "builder", "shop", "wip.txt")); err != nil || string(got) != "builder's work in progress\n" {
		t.Errorf("builder's clone lacks its worktree's file: %q %v", got, err)
	}
	if got := gitOut(t, filepath.Join(o.root, "builder"), filepath.Join(o.root, "builder", "shop"), "symbolic-ref", "--short", "HEAD"); got != "agent/builder" {
		t.Errorf("builder's clone is on %q, want its branch", got)
	}
	if st := gitOut(t, filepath.Join(o.root, "builder"), filepath.Join(o.root, "builder", "shop"), "status", "--porcelain"); !strings.Contains(st, "?? wip.txt") {
		t.Errorf("builder's file isn't uncommitted in its clone: %q", st)
	}
	if got, err := os.ReadFile(filepath.Join(o.root, "lead", "shop", "lead-notes.txt")); err != nil || string(got) != "lead's notes\n" {
		t.Errorf("lead's clone lacks its file: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(o.root, "lead", "shop", "wip.txt")); err == nil {
		t.Error("lead got builder's file")
	}
	if !strings.Contains(itemByPath(items, ".worktrees/builder-old").GetNote(), "1 files copied into builder's copy") {
		t.Errorf("the worktree's note: %q", itemByPath(items, ".worktrees/builder-old").GetNote())
	}
	// Nothing was deleted.
	for _, p := range []string{".git/hooks/post-checkout", ".worktrees/builder-old/wip.txt", "lead-notes.txt", "vendor/x/a.txt"} {
		if _, err := os.Stat(filepath.Join(o.project, p)); err != nil {
			t.Errorf("%s is gone: %v", p, err)
		}
	}
	// .git back to work, all of it; the tree's agent files too; the
	// recorded repository and worktree left as they are.
	for _, p := range []string{".git", ".git/config", ".git/hooks/post-checkout", ".git/worktrees/builder-old", "lead-notes.txt"} {
		if !slices.Contains(o.reowned, p) {
			t.Errorf("%s wasn't made work's: %q", p, o.reowned)
		}
	}
	for _, p := range o.reowned {
		if strings.HasPrefix(p, "vendor/x") || strings.HasPrefix(p, ".worktrees/builder-old") {
			t.Errorf("a recorded repository or worktree changed hands: %s", p)
		}
	}
	for _, want := range []string{
		"runuser -u work -- " + agentGitPath + " config --file " + filepath.Join(o.project, ".git", "config") + " --unset-all core.sharedRepository",
		"git config --system --fixed-value --unset-all safe.directory /home/work/shop",
		"git config --system --fixed-value --unset-all safe.directory /home/work/shop/.worktrees/builder-old",
	} {
		if !o.run.ran(want) {
			t.Errorf("didn't run %q; ran %q", want, o.run.calls)
		}
	}
	// The record waits for the owner: the next start gets it again, and
	// nothing runs.
	calls := len(o.asAgent)
	again, err := o.prepare()
	if err != nil || len(again) != len(items) || len(o.asAgent) != calls {
		t.Errorf("a second start: %d items %v, ran %q", len(again), err, o.asAgent[calls:])
	}
}

func TestTheMoveRefuses(t *testing.T) {
	t.Run("an agent still running", func(t *testing.T) {
		o := newOldTeam(t)
		o.running = []int{2002}
		if _, err := o.prepare(); !errors.Is(err, errAgentsRunning) {
			t.Fatalf("err = %v", err)
		}
		if len(o.reowned) > 0 || len(o.asAgent) > 0 {
			t.Errorf("went on: %q %q", o.reowned, o.asAgent)
		}
		if _, err := os.Stat(o.tu.migrationFile()); err == nil {
			t.Error("recorded a move that didn't happen")
		}
	})
	t.Run("a link in .git work didn't make", func(t *testing.T) {
		o := newOldTeam(t)
		if err := os.Symlink("/etc/passwd", filepath.Join(o.project, ".git", "hooks", "pre-push")); err != nil {
			t.Fatal(err)
		}
		o.run.foreign[".git/hooks/pre-push"] = 2001
		if _, err := o.prepare(); err == nil || !strings.Contains(err.Error(), ".git/hooks/pre-push is a link that you didn't make") {
			t.Fatalf("err = %v", err)
		}
		if len(o.reowned) > 0 || len(o.asAgent) > 0 {
			t.Errorf("went on: %q %q", o.reowned, o.asAgent)
		}
	})
	t.Run("an agent's clone can't be made", func(t *testing.T) {
		o := newOldTeam(t)
		o.agentErr = errors.New("builder's files can't hold a copy of the project")
		if _, err := o.prepare(); err == nil || !strings.Contains(err.Error(), "can't hold a copy") {
			t.Fatalf("err = %v", err)
		}
		if len(o.reowned) > 0 {
			t.Errorf("re-owned before every clone was made: %q", o.reowned)
		}
	})
}

// The owner's answer: removed items go (a worktree with its
// registration), kept ones become work's; nothing else is deleted.
func TestTheOwnersAnswerToTheMove(t *testing.T) {
	o := newOldTeam(t)
	items, err := o.prepare()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hook, wt := itemByPath(items, ".git/hooks/post-checkout"), itemByPath(items, ".worktrees/builder-old")
	setting := ""
	for _, i := range items {
		if i.GetKind() == "setting" {
			setting = i.GetId()
		}
	}
	for _, bad := range [][]string{{"m999"}, {setting}} {
		if err := o.tu.ResolveMigration(ctx, "/home/work/shop", bad); err == nil {
			t.Errorf("removing %q was accepted", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(o.project, ".git", "hooks", "post-checkout")); err != nil {
		t.Fatalf("a refused answer removed something: %v", err)
	}
	o.running = []int{2001}
	if err := o.tu.ResolveMigration(ctx, "/home/work/shop", []string{hook.GetId()}); !errors.Is(err, errAgentsRunning) {
		t.Errorf("with lead running: %v", err)
	}
	o.running = nil
	o.reowned = nil
	if err := o.tu.ResolveMigration(ctx, "/home/work/shop", []string{hook.GetId(), wt.GetId()}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git/hooks/post-checkout", ".worktrees/builder-old", ".git/worktrees/builder-old"} {
		if _, err := os.Lstat(filepath.Join(o.project, p)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s wasn't removed: %v", p, err)
		}
	}
	for _, p := range []string{"lead-notes.txt", "vendor/x/a.txt", ".git/config"} {
		if _, err := os.Stat(filepath.Join(o.project, p)); err != nil {
			t.Errorf("kept %s is gone: %v", p, err)
		}
	}
	for _, p := range []string{"vendor/x", "vendor/x/a.txt", "vendor/x/.git", "lead-notes.txt"} {
		if !slices.Contains(o.reowned, p) {
			t.Errorf("kept %s wasn't made work's: %q", p, o.reowned)
		}
	}
	// builder's work in progress is still in its clone.
	if _, err := os.Stat(filepath.Join(o.root, "builder", "shop", "wip.txt")); err != nil {
		t.Errorf("builder's clone lost its file: %v", err)
	}
	if _, err := os.Stat(o.tu.migrationFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the record stayed: %v", err)
	}
	if err := o.tu.ResolveMigration(ctx, "/home/work/shop", nil); err == nil {
		t.Error("a second answer was accepted")
	}
}

// migrate-copy never writes over a file already in the clone, and copies
// only what the agent owns, never through a link.
func TestMigrateCopyNeverOverwrites(t *testing.T) {
	home := agentHomeDir(t)
	clone := filepath.Join(home, "shop")
	src := t.TempDir()
	for f, c := range map[string]string{"a.txt": "new a", "same.txt": "same", "dir/b.txt": "b", "theirs.txt": "x", ".git/config": "[core]"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, c := range map[string]string{"a.txt": "agent's newer a", "same.txt": "same"} {
		if err := os.WriteFile(filepath.Join(clone, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	owns := func(full string, _ *unix.Stat_t) bool { return filepath.Base(full) != "theirs.txt" }
	var out bytes.Buffer
	if err := migrateCopy(home, src, clone, "1073741824", owns, strings.NewReader(`{"exclude":[]}`), &out); err != nil {
		t.Fatal(err)
	}
	var r copyResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	slices.Sort(r.Copied)
	if !slices.Equal(r.Copied, []string{"dir/b.txt", "same.txt"}) || !slices.Equal(r.Skipped, []string{"a.txt"}) {
		t.Errorf("copied %q, skipped %q", r.Copied, r.Skipped)
	}
	if got, _ := os.ReadFile(filepath.Join(clone, "a.txt")); string(got) != "agent's newer a" {
		t.Errorf("a.txt was written over: %q", got)
	}
	for _, p := range []string{"theirs.txt", ".git/config", "link"} {
		if _, err := os.Lstat(filepath.Join(clone, p)); err == nil {
			t.Errorf("%s was copied", p)
		}
	}
	for _, bad := range [][2]string{{filepath.Join(t.TempDir(), "shop"), "1"}, {clone, "0"}, {filepath.Join(home, ".hidden"), "1"}} {
		if err := migrateCopy(home, src, bad[0], bad[1], owns, strings.NewReader(`{}`), io.Discard); err == nil {
			t.Errorf("migrate-copy into %v accepted", bad)
		}
	}
}

// The board takes a move's decision only with plain items; the channel
// relays the owner's answer to init.
func TestTheMovesDecisionOnTheBoard(t *testing.T) {
	d := func(items ...*typesv1.MigrationItem) *agentv1.AppendBoardRequest {
		return &agentv1.AppendBoardRequest{Line: &agentv1.AppendBoardRequest_Decision{Decision: &typesv1.Decision{
			Id: "01ABC", From: "portenv", Kind: typesv1.DecisionKind_DECISION_KIND_TEAM_START,
			State: typesv1.DecisionState_DECISION_STATE_PENDING, Summary: "s", Detail: "d", MigrationItems: items,
		}}}
	}
	if _, err := checkBoardLine(d(&typesv1.MigrationItem{Id: "m1", Path: "lead-notes.txt", Kind: "file", Agent: "lead", Choice: true})); err != nil {
		t.Errorf("a plain item: %v", err)
	}
	for _, bad := range []*typesv1.MigrationItem{
		{Id: "m1", Path: "a\x1b[2Jb", Kind: "file"},
		{Id: "m1", Path: "a\u202eb", Kind: "file"},
		{Id: "m1", Path: "a", Kind: "other"},
		{Id: "m 1", Path: "a", Kind: "file"},
		{Id: "m1", Path: "a", Kind: "file", Agent: "Lead!"},
	} {
		if _, err := checkBoardLine(d(bad)); err == nil {
			t.Errorf("item %v was accepted", bad)
		}
	}
	if got := showPath("a\x1b[2Jb"); got != `"a\x1b[2Jb"` {
		t.Errorf("showPath = %q", got)
	}
	var got *agentv1.ResolveTeamMigrationRequest
	s := &apiServer{resolveMigration: func(_ context.Context, r *agentv1.ResolveTeamMigrationRequest) error { got = r; return nil }}
	if _, err := s.ResolveTeamMigration(context.Background(), &agentv1.ResolveTeamMigrationRequest{ProjectDir: "/home/work/shop", RemoveItems: []string{"m1"}}); err != nil || got.GetRemoveItems()[0] != "m1" {
		t.Errorf("relay: %v %v", got, err)
	}
	if _, err := (&apiServer{}).ResolveTeamMigration(context.Background(), &agentv1.ResolveTeamMigrationRequest{}); err == nil {
		t.Error("a server without init's relay resolved a move")
	}
}
