// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// testGit is homeGit with this machine's git from PATH (the box's is
// /usr/bin/git), recording each call.
func testGit(t *testing.T, home string, calls *[]string) gitFunc {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git here")
	}
	return func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(args, " "))
		cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- the test's git
		cmd.Dir = dir
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.Bytes(), err
	}
}

// ownerProject is work's project, a repository with one commit on main.
func ownerProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "work", "shop")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []string
	git := testGit(t, root, &calls)
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"-c", "user.name=Owner", "-c", "user.email=owner@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "first"},
	} {
		if out, err := git(context.Background(), project, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return project
}

func agentHomeDir(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "builder")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func cloneIn(t *testing.T, project, home, quota string) (CloneResult, []string, error) {
	t.Helper()
	var calls []string
	var out bytes.Buffer
	err := cloneOwn(context.Background(), project, home, "builder", quota, testGit(t, home, &calls), &out)
	var r CloneResult
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &r); jerr != nil {
			t.Fatalf("clone-own said %q: %v", out.String(), jerr)
		}
	}
	return r, calls, err
}

func gitOut(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	var calls []string
	out, err := testGit(t, home, &calls)(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// ADR 0022 §2a point 2: a full clone at ~/<workspace>, on agent/<id>,
// remote "owner"; the agent's own ~/.gitconfig names the project as safe,
// once.
func TestCloneOwnMakesTheAgentsClone(t *testing.T) {
	project, home := ownerProject(t), agentHomeDir(t)
	r, calls, err := cloneIn(t, project, home, "1073741824")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "shop")
	if r != (CloneResult{Dir: dest, Made: true, Branch: "agent/builder"}) {
		t.Errorf("result %+v", r)
	}
	if got := gitOut(t, home, dest, "symbolic-ref", "--short", "HEAD"); got != "agent/builder" {
		t.Errorf("branch %q, want agent/builder", got)
	}
	if got := gitOut(t, home, dest, "remote"); got != "owner" {
		t.Errorf("remotes %q, want owner alone", got)
	}
	if got := gitOut(t, home, dest, "remote", "get-url", "owner"); got != project {
		t.Errorf("owner is %q, want %s", got, project)
	}
	if got := gitOut(t, home, dest, "log", "--format=%s"); got != "first" {
		t.Errorf("log %q, want the owner's commit", got)
	}
	if !slices.ContainsFunc(calls, func(c string) bool {
		return strings.HasPrefix(c, "clone --quiet --no-hardlinks --origin owner -- "+project+" ")
	}) {
		t.Errorf("didn't clone with --no-hardlinks and remote owner: %q", calls)
	}
	// A full copy: no alternates borrowing the owner's objects.
	if _, err := os.Stat(filepath.Join(dest, ".git", "objects", "info", "alternates")); err == nil {
		t.Error("the clone borrows the owner's objects (alternates)")
	}
	safe := gitOut(t, home, home, "config", "--global", "--get-all", "safe.directory")
	if safe != project+"\n"+project+"/.git" {
		t.Errorf("~/.gitconfig safe.directory = %q, want the project and its .git", safe)
	}
	if _, err := os.Stat(filepath.Join(home, ".portenv-clone.shop")); err == nil {
		t.Error("the temporary clone was left")
	}

	// The next start leaves it as it is, and runs no git.
	r, calls, err = cloneIn(t, project, home, "1073741824")
	if err != nil || r != (CloneResult{Dir: dest}) || len(calls) != 0 {
		t.Errorf("second start: %+v %v, ran %q", r, err, calls)
	}
	if got := gitOut(t, home, home, "config", "--global", "--get-all", "safe.directory"); got != safe {
		t.Errorf("safe.directory added again: %q", got)
	}
}

// The owner's repository already has agent/<id>: the clone starts there.
func TestCloneOwnUsesTheOwnersAgentBranch(t *testing.T) {
	project, home := ownerProject(t), agentHomeDir(t)
	root := filepath.Dir(filepath.Dir(project))
	gitOut(t, root, project, "branch", "agent/builder")
	gitOut(t, root, project, "-c", "user.name=Owner", "-c", "user.email=o@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "on main")
	if _, _, err := cloneIn(t, project, home, "1073741824"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "shop")
	if got := gitOut(t, home, dest, "log", "--format=%s", "-1"); got != "first" {
		t.Errorf("agent/builder's tip is %q, want the owner's agent/builder (first)", got)
	}
}

// An empty project: the branch is agent/<id> from its first commit.
func TestCloneOwnOfAnEmptyProject(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "work", "shop")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, project, "init", "--quiet")
	home := agentHomeDir(t)
	if _, _, err := cloneIn(t, project, home, "1073741824"); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, home, filepath.Join(home, "shop"), "symbolic-ref", "--short", "HEAD"); got != "agent/builder" {
		t.Errorf("branch %q", got)
	}
}

func TestCloneOwnRefuses(t *testing.T) {
	t.Run("a project bigger than the agent's free space", func(t *testing.T) {
		project, home := ownerProject(t), agentHomeDir(t)
		_, calls, err := cloneIn(t, project, home, "1024")
		if err == nil || !strings.Contains(err.Error(), "builder's files can't hold a copy of the project") || !strings.Contains(err.Error(), "its limit is 1 KB") {
			t.Fatalf("err = %v, want the plain line", err)
		}
		if slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "clone") }) {
			t.Errorf("cloned anyway: %q", calls)
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		project, home := filepath.Join(t.TempDir(), "shop"), agentHomeDir(t)
		if err := os.Mkdir(project, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := cloneIn(t, project, home, "1073741824"); err == nil || !strings.Contains(err.Error(), "isn't a git repository") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a file or link where the clone goes", func(t *testing.T) {
		project := ownerProject(t)
		for _, kind := range []string{"file", "link"} {
			home := agentHomeDir(t)
			dest := filepath.Join(home, "shop")
			var err error
			if kind == "file" {
				err = os.WriteFile(dest, nil, 0o600)
			} else {
				err = os.Symlink(project, dest)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, calls, err := cloneIn(t, project, home, "1073741824"); err == nil || !strings.Contains(err.Error(), "isn't a folder") || len(calls) > 0 {
				t.Errorf("%s: err = %v, ran %q", kind, err, calls)
			}
		}
	})
	t.Run("bad arguments", func(t *testing.T) {
		project, home := ownerProject(t), agentHomeDir(t)
		for _, args := range [][4]string{
			{"relative/shop", home, "builder", "1"},
			{filepath.Join(filepath.Dir(project), ".hidden"), home, "builder", "1"},
			{project, "relative", "builder", "1"},
			{project, home, "Builder", "1"},
			{project, home, "builder", "0"},
		} {
			var calls []string
			if err := cloneOwn(context.Background(), args[0], args[1], args[2], args[3], testGit(t, home, &calls), &bytes.Buffer{}); err == nil {
				t.Errorf("clone-own %v accepted", args)
			}
		}
	})
}

// StartTeamAgent with clone_project_dir: the clone is made as the agent
// (team-io clone-own through as-agent), before the terminal, and the
// runner starts in it.
func TestStartTeamAgentMakesItsClone(t *testing.T) {
	s, _, _, started, _ := newStartServer(t)
	s.cfg = Config{User: "work", UID: 1000, HomeRoot: "/home"}
	var asked [][]string
	s.asAgentIO = func(_ context.Context, uid int, home string, args []string, _ io.Reader) ([]byte, error) {
		asked = append(asked, append([]string{strconv.Itoa(uid), home}, args...))
		return json.Marshal(CloneResult{Dir: "/home/builder/shop", Made: true, Branch: "agent/builder"})
	}
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{
		AgentId: "builder", Argv: []string{"claude"}, HomeQuotaBytes: 2 << 30, CloneProjectDir: "/home/work/shop",
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"2001", "/home/builder", "clone-own", "/home/work/shop", "/home/builder", "builder", "2147483648"}
	if len(asked) != 1 || !slices.Equal(asked[0], want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
	if len(*started) != 1 || (*started)[0].WorkDir != "/home/builder/shop" {
		t.Errorf("started %+v, want the runner in the clone", *started)
	}

	// The clone can't be made: the start stops with the plain line, and no
	// terminal starts.
	s2, _, _, started2, _ := newStartServer(t)
	s2.cfg = s.cfg
	s2.asAgentIO = func(context.Context, int, string, []string, io.Reader) ([]byte, error) {
		return nil, errors.New("builder's files can't hold a copy of the project (3.1 GB; its limit is 2.0 GB). Raise its limit in Settings")
	}
	_, err := s2.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, HomeQuotaBytes: 2 << 30, CloneProjectDir: "/home/work/shop"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "can't hold a copy") || len(*started2) != 0 {
		t.Errorf("err = %v, started %v", err, *started2)
	}

	// A project folder outside work's home is refused before anything runs.
	for _, bad := range []string{"/home/lead/shop", "/home/work/../lead", "/home/work/.ssh", "/etc"} {
		s3, _, _, _, _ := newStartServer(t)
		s3.cfg = s.cfg
		s3.asAgentIO = func(context.Context, int, string, []string, io.Reader) ([]byte, error) {
			t.Errorf("%s: ran clone-own", bad)
			return nil, nil
		}
		if _, err := s3.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{AgentId: "builder", Argv: []string{"claude"}, HomeQuotaBytes: 1, CloneProjectDir: bad}); err == nil {
			t.Errorf("clone of %s accepted", bad)
		}
	}
}

// as-agent runs team-io only for the clone's operations, as an agent.
func TestAsAgentTeamIO(t *testing.T) {
	uid, gid, home, argv, err := parseAsAgent([]string{"2001", "2001", "/home/builder", "--", "team-io", "clone-own", "/home/work/shop", "/home/builder", "builder", "1"})
	if err != nil || uid != 2001 || gid != 2001 || home != "/home/builder" || argv[0] != "team-io" || argv[1] != "clone-own" {
		t.Fatalf("parse: %d %d %q %q %v", uid, gid, home, argv, err)
	}
	for _, bad := range [][]string{
		{"2001", "2001", "/home/builder", "--", "team-io", "open-project", "/home/work/shop"},
		{"2001", "2001", "/home/builder", "--", "team-io", "write-home", "/home/builder", "x", "1"},
		{"1000", "1000", "/home/work", "--", "team-io", "clone-own", "/home/work/shop", "/home/work", "work", "1"},
		{"0", "0", "/root", "--", "team-io", "clone-own"},
		{"2001", "2001", "/home/builder", "--", "bash", "-c", "id"},
	} {
		if _, _, _, _, err := parseAsAgent(bad); err == nil {
			t.Errorf("as-agent %q accepted", bad)
		}
	}
}
