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
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// testStreamGit is streamGit with this machine's git.
func testStreamGit(t *testing.T, home string) func(ctx context.Context, dir string, stdout io.Writer, args ...string) ([]byte, error) {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git here")
	}
	return func(ctx context.Context, dir string, stdout io.Writer, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- the test's git
		cmd.Dir = dir
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = stdout, &stderr
		err := cmd.Run()
		return stderr.Bytes(), err
	}
}

// tamperedClone is builder's clone of the owner's project with a commit
// on agent/builder, and settings and hooks in its own repository that
// would each leave a marker in markers if git honoured them for whoever
// runs git (ADR 0022 §2a point 5's tamper step). Builder's own commit runs
// them, as builder, which shows they're live.
func tamperedClone(t *testing.T, project, home, markers string) string {
	t.Helper()
	if _, _, err := cloneIn(t, project, home, "1073741824"); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(home, filepath.Base(project))
	hooks := filepath.Join(home, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hk := range []string{"pre-commit", "post-commit", "reference-transaction", "post-checkout", "post-merge", "pre-auto-gc", "fsmonitor-watchman"} {
		if err := os.WriteFile(filepath.Join(hooks, hk), []byte("#!/bin/sh\ntouch "+filepath.Join(markers, "hook-"+hk)+"\n"), 0o755); err != nil { // #nosec G306 -- a test hook
			t.Fatal(err)
		}
	}
	run := "touch " + filepath.Join(markers, "config") + "; false"
	for _, kv := range [][2]string{
		{"core.hooksPath", hooks},
		{"core.fsmonitor", run},
		{"core.pager", run},
		{"diff.external", run},
		{"core.sshCommand", run},
		{"filter.evil.clean", run},
		{"filter.evil.smudge", run},
		{"uploadpack.packObjectsHook", run},
	} {
		gitOut(t, home, clone, "config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(clone, ".gitattributes"), []byte("* filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "from-builder.txt"), []byte("from builder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, home, clone, "add", ".gitattributes", "from-builder.txt")
	gitOut(t, home, clone, "-c", "user.name=Builder", "-c", "user.email=builder@example.invalid", "commit", "--quiet", "-m", "from builder")
	if _, err := os.Stat(filepath.Join(markers, "hook-pre-commit")); err != nil {
		t.Fatalf("builder's own hooks didn't run for builder, so the tamper isn't live: %v", err)
	}
	return clone
}

func bundleOf(t *testing.T, home, name, mode string) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	err := bundleOwn(context.Background(), home, name, "builder", mode, testStreamGit(t, home), &out)
	return out.Bytes(), err
}

func markersIn(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func clearMarkers(t *testing.T, dir string) {
	t.Helper()
	for _, n := range markersIn(t, dir) {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
}

// ADR 0022 §2a points 3 and 5: builder's branch arrives in work's
// repository as refs/remotes/agents/builder from the bundle alone; nothing
// in builder's repository runs for work, and the bundle is deleted.
func TestTheBranchArrivesByTheSafeFetch(t *testing.T) {
	project, home, markers := ownerProject(t), agentHomeDir(t), t.TempDir()
	work := filepath.Dir(project)
	clone := tamperedClone(t, project, home, markers)
	want := gitOut(t, home, clone, "rev-parse", "HEAD")
	bundle, err := bundleOf(t, home, "shop", "thin")
	if err != nil {
		t.Fatal(err)
	}
	clearMarkers(t, markers) // what builder's own git ran, as builder
	var calls []string
	var out bytes.Buffer
	if err := fetchAgent(context.Background(), project, "builder", "1048576", testGit(t, work, &calls), bytes.NewReader(bundle), &out); err != nil {
		t.Fatal(err)
	}
	var r FetchResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Ref != "refs/remotes/agents/builder" || r.Commit != want {
		t.Errorf("fetched %+v, want builder's %s", r, want)
	}
	fetch := calls[0]
	for _, want := range []string{"-c protocol.allow=never -c protocol.file.allow=always", "-c core.hooksPath=/dev/null", "-c core.fsmonitor=false", "-c transfer.fsckObjects=true", "-c fetch.fsckObjects=true", "fetch --quiet --no-tags --no-recurse-submodules --no-write-fetch-head " + filepath.Join(work, handoffDir, "builder.bundle") + " +refs/heads/agent/builder:refs/remotes/agents/builder"} {
		if !strings.Contains(fetch, want) {
			t.Errorf("fetch %q lacks %q", fetch, want)
		}
	}
	// work's everyday git in the project, after the fetch.
	for _, args := range [][]string{
		{"status"}, {"log", "--all", "-p"}, {"diff", "HEAD", "refs/remotes/agents/builder"}, {"show", "refs/remotes/agents/builder"},
		{"-c", "user.name=Owner", "-c", "user.email=o@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "owner again"},
	} {
		gitOut(t, work, project, args...)
	}
	if m := markersIn(t, markers); len(m) > 0 {
		t.Errorf("work's git ran builder's settings or hooks: %v", m)
	}
	if fi, err := os.Stat(filepath.Join(work, handoffDir)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the handoff folder: %v %v, want 0700", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(work, handoffDir, "builder.bundle")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the bundle was left: %v", err)
	}
	// The project's own config is as it was: nothing of builder's in it.
	if cfg, _ := os.ReadFile(filepath.Join(project, ".git", "config")); strings.Contains(string(cfg), "hooksPath") || strings.Contains(string(cfg), "evil") {
		t.Errorf("the project's config took builder's settings:\n%s", cfg)
	}
}

func TestBundleAndFetchRefuse(t *testing.T) {
	t.Run("nothing new", func(t *testing.T) {
		project, home := ownerProject(t), agentHomeDir(t)
		if _, _, err := cloneIn(t, project, home, "1073741824"); err != nil {
			t.Fatal(err)
		}
		if _, err := bundleOf(t, home, "shop", "thin"); !errors.Is(err, errNothingToShare) {
			t.Errorf("err = %v, want nothing to share", err)
		}
	})
	t.Run("no clone", func(t *testing.T) {
		home := agentHomeDir(t)
		if _, err := bundleOf(t, home, "shop", "thin"); err == nil || !strings.Contains(err.Error(), "no copy of the project") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("over the limit, and the file is gone", func(t *testing.T) {
		project, home, markers := ownerProject(t), agentHomeDir(t), t.TempDir()
		tamperedClone(t, project, home, markers)
		bundle, err := bundleOf(t, home, "shop", "full")
		if err != nil {
			t.Fatal(err)
		}
		var calls []string
		work := filepath.Dir(project)
		err = fetchAgent(context.Background(), project, "builder", "64", testGit(t, work, &calls), bytes.NewReader(bundle), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "over") || len(calls) > 0 {
			t.Errorf("err = %v, ran %q", err, calls)
		}
		if _, err := os.Lstat(filepath.Join(work, handoffDir, "builder.bundle")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the bundle was left: %v", err)
		}
	})
	t.Run("a thin bundle the owner lacks the base of", func(t *testing.T) {
		project, home, markers := ownerProject(t), agentHomeDir(t), t.TempDir()
		tamperedClone(t, project, home, markers)
		other := filepath.Join(t.TempDir(), "work", "shop") // another history
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		gitOut(t, filepath.Dir(other), other, "init", "--quiet", "--initial-branch=main")
		gitOut(t, filepath.Dir(other), other, "-c", "user.name=Someone", "-c", "user.email=s@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "another history")
		thin, err := bundleOf(t, home, "shop", "thin")
		if err != nil {
			t.Fatal(err)
		}
		var calls []string
		work := filepath.Dir(other)
		if err := fetchAgent(context.Background(), other, "builder", "1048576", testGit(t, work, &calls), bytes.NewReader(thin), io.Discard); !errors.Is(err, errMissingPrerequisite) {
			t.Fatalf("err = %v, want the missing base said", err)
		}
		full, err := bundleOf(t, home, "shop", "full")
		if err != nil {
			t.Fatal(err)
		}
		if err := fetchAgent(context.Background(), other, "builder", "1048576", testGit(t, work, &calls), bytes.NewReader(full), io.Discard); err != nil {
			t.Fatalf("the full branch: %v", err)
		}
	})
	t.Run("bad arguments", func(t *testing.T) {
		project := ownerProject(t)
		for _, a := range [][3]string{{"relative", "builder", "1"}, {project, "Builder", "1"}, {project, "builder", "0"}, {filepath.Join(filepath.Dir(project), ".x"), "builder", "1"}} {
			if err := fetchAgent(context.Background(), a[0], a[1], a[2], nil, strings.NewReader("x"), io.Discard); err == nil {
				t.Errorf("fetch-agent %v accepted", a)
			}
		}
		home := agentHomeDir(t)
		for _, a := range [][3]string{{"../x", "builder", "thin"}, {"shop", "Builder", "thin"}, {"shop", "builder", "fat"}} {
			if err := bundleOwn(context.Background(), home, a[0], a[1], a[2], nil, io.Discard); err == nil {
				t.Errorf("bundle-own %v accepted", a)
			}
		}
	})
}

// The helper's FetchAgentBranch: the bundle as the agent, piped into
// fetch-agent as work; thin first, the whole branch when the owner lacks
// its base.
func TestFetchAgentBranchPipesTheAgentsBundleToWork(t *testing.T) {
	if _, err := os.Stat(agentGitPath); err != nil {
		t.Skip("fetch-agent runs " + agentGitPath)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".portenv", "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".portenv", "team", "users.json"), []byte(`{"version":1,"users":[{"id":"builder","uid":2001}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "work", "shop")
	home := filepath.Join(root, "builder")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, project, "init", "--quiet", "--initial-branch=main")
	gitOut(t, root, project, "-c", "user.name=O", "-c", "user.email=o@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "first")
	markers := t.TempDir()
	clone := tamperedClone(t, project, home, markers)
	want := gitOut(t, home, clone, "rev-parse", "HEAD")
	var modes []string
	s := &apiServer{
		cfg: Config{User: "work", UID: 1000, HomeRoot: root},
		asAgentStream: func(_ context.Context, uid int, h string, args []string) (io.ReadCloser, func() error, error) {
			if uid != 2001 || h != home || args[0] != "bundle-own" {
				t.Errorf("as the agent: %d %s %q", uid, h, args)
			}
			modes = append(modes, args[4])
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			go func() {
				err := bundleOwn(context.Background(), args[1], args[2], args[3], args[4], testStreamGit(t, home), pw)
				_ = pw.CloseWithError(err)
				done <- err
			}()
			return pr, func() error { return <-done }, nil
		},
	}
	r, err := s.FetchAgentBranch(context.Background(), &agentv1.FetchAgentBranchRequest{AgentId: "builder", ProjectDir: "/home/work/shop", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetRef() != "refs/remotes/agents/builder" || r.GetCommit() != want || strings.Join(modes, ",") != "thin" {
		t.Errorf("fetched %v with %v, want %s with a thin bundle", r, modes, want)
	}
	// The agent's own branch, with nothing new: its plain line.
	gitOut(t, home, clone, "fetch", "--quiet", "owner") // owner has nothing new; agent/builder is still ahead
	if _, err := s.FetchAgentBranch(context.Background(), &agentv1.FetchAgentBranchRequest{AgentId: "ghost", ProjectDir: "/home/work/shop", MaxBytes: 1}); status.Code(err) != codes.NotFound {
		t.Errorf("a stranger: %v", err)
	}
	if _, err := s.FetchAgentBranch(context.Background(), &agentv1.FetchAgentBranchRequest{AgentId: "builder", ProjectDir: "/home/lead/shop", MaxBytes: 1}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("another project: %v", err)
	}
}
