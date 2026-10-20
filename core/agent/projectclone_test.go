// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// New workspace's From GitHub (ADR 0025 §2): the helper clones as work,
// hardened, with the size checked before and while bytes arrive.

type cloneCall struct {
	env  []string
	dir  string
	args []string
}

// fakeClone records git's call and does what fill says in the
// destination (the last argument).
func fakeClone(calls *[]cloneCall, fill func(ctx context.Context, dest string) ([]byte, error)) cloneRunner {
	return func(ctx context.Context, env []string, dir string, args ...string) ([]byte, error) {
		*calls = append(*calls, cloneCall{env, dir, args})
		if len(args) > 0 && args[0] == "-C" {
			return nil, nil // remote set-url and the like, after the clone
		}
		return fill(ctx, args[len(args)-1])
	}
}

func madeRepo(_ context.Context, dest string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		return nil, err
	}
	return nil, os.WriteFile(filepath.Join(dest, "README.md"), []byte("hi"), 0o644)
}

func plentyFree(string) (uint64, error) { return 100 << 30, nil }

func TestCloneProjectIsHardened(t *testing.T) {
	project := filepath.Join(t.TempDir(), "acme")
	var calls []cloneCall
	res, err := cloneProject(context.Background(), cloneRequest{Project: project, URL: "https://github.com/acme/api", Copies: 3}, fakeClone(&calls, madeRepo), plentyFree)
	if err != nil || res.Existed {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(project, "README.md")); err != nil {
		t.Fatal("the clone isn't in place")
	}
	c := calls[0]
	args := strings.Join(c.args, " ")
	for _, want := range []string{"-c protocol.allow=never", "-c protocol.https.allow=always", "-c credential.helper=", "-c core.hooksPath=/dev/null",
		"clone", "--no-recurse-submodules", "--template=", "-- https://github.com/acme/api"} {
		if !strings.Contains(args, want) {
			t.Errorf("git's arguments lack %q: %s", want, args)
		}
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_ASKPASS=/bin/false"} {
		if !slices.Contains(c.env, want) {
			t.Errorf("git's environment lacks %q: %v", want, c.env)
		}
	}
	for _, e := range c.env {
		if strings.HasPrefix(e, "HOME=") && (e == "HOME=/home/work" || !strings.Contains(e, "portenv-clone-home")) {
			t.Errorf("git's HOME is %q, not an empty one of its own", e)
		}
	}
	// The temporary folder is next to the project, then renamed into it.
	if dest := c.args[len(c.args)-1]; filepath.Dir(dest) != filepath.Dir(project) || dest == project {
		t.Errorf("cloned into %s", dest)
	}
	ents, _ := os.ReadDir(filepath.Dir(project))
	if len(ents) != 1 {
		t.Errorf("left behind: %v", ents)
	}
}

// A project already there (an earlier try finished it) isn't cloned again.
func TestCloneProjectKeepsAnExistingProject(t *testing.T) {
	project := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []cloneCall
	res, err := cloneProject(context.Background(), cloneRequest{Project: project, URL: "https://github.com/acme/api", Copies: 1}, fakeClone(&calls, madeRepo), plentyFree)
	if err != nil || !res.Existed || len(calls) != 0 {
		t.Fatalf("%+v %v %v", res, err, calls)
	}
}

// Too big by GitHub's size: refused before git runs, with ADR §2's line.
func TestCloneProjectRefusesBeforeAnyByte(t *testing.T) {
	project := filepath.Join(t.TempDir(), "acme")
	var calls []cloneCall
	free := func(string) (uint64, error) { return 5_000_000_000, nil }
	_, err := cloneProject(context.Background(), cloneRequest{Project: project, URL: "https://github.com/acme/api", Copies: 3, Estimate: 2_100_000_000}, fakeClone(&calls, madeRepo), free)
	var ce *cloneError
	if !errors.As(err, &ce) || ce.kind != cloneTooBig || len(calls) != 0 {
		t.Fatalf("%v %v", err, calls)
	}
	if ce.line != "acme needs about 13.6 GB with its team (3 copies of 2.1 GB, each with its checkout). This workspace has 5 GB free." {
		t.Fatalf("%q", ce.line)
	}
}

// Too big while bytes arrive: git is stopped, nothing is left.
func TestCloneProjectStopsWhenItOutgrowsTheRoom(t *testing.T) {
	project := filepath.Join(t.TempDir(), "acme")
	var calls []cloneCall
	free := func(string) (uint64, error) { return 1<<30 + 3<<20, nil } // 1 GB of margin, 3 MB for the copies
	grow := func(ctx context.Context, dest string) ([]byte, error) {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return nil, err
		}
		for i := 0; ; i++ {
			if err := os.WriteFile(filepath.Join(dest, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), make([]byte, 1<<20), 0o644); err != nil {
				return nil, err
			}
			select {
			case <-ctx.Done():
				return []byte("killed"), ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	_, err := cloneProject(context.Background(), cloneRequest{Project: project, URL: "https://github.com/acme/api", Copies: 2, sizeEvery: 10 * time.Millisecond}, fakeClone(&calls, grow), free)
	var ce *cloneError
	if !errors.As(err, &ce) || ce.kind != cloneTooBig || !strings.Contains(ce.line, "This workspace has") {
		t.Fatalf("%v", err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(project)); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
}

// A repository that wants a sign-in says so; any other failure gives
// git's last line; neither leaves a partial project.
func TestCloneProjectFailures(t *testing.T) {
	for out, want := range map[string]cloneErrorKind{
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled": cloneNeedsSignIn,
		"remote: Repository not found.\nfatal: repository 'https://github.com/acme/api/' not found": cloneNeedsSignIn,
		"fatal: Authentication failed for 'https://github.com/acme/api/'":                         cloneNeedsSignIn,
		"fatal: unable to access 'https://github.com/acme/api/': Could not resolve host: github.com": cloneFailed,
	} {
		project := filepath.Join(t.TempDir(), "acme")
		var calls []cloneCall
		fail := func(_ context.Context, dest string) ([]byte, error) {
			_ = os.MkdirAll(dest, 0o755)
			return []byte(out), errors.New("exit status 128")
		}
		_, err := cloneProject(context.Background(), cloneRequest{Project: project, URL: "https://github.com/acme/api", Copies: 1}, fakeClone(&calls, fail), plentyFree)
		var ce *cloneError
		if !errors.As(err, &ce) || ce.kind != want {
			t.Errorf("%q: %v", out, err)
			continue
		}
		if want == cloneNeedsSignIn && ce.line != "This repository needs a sign-in. Sign in with GitHub, then try again." {
			t.Errorf("%q", ce.line)
		}
		if want == cloneFailed && !strings.HasPrefix(ce.line, "Portenv couldn’t clone github.com/acme/api: ") {
			t.Errorf("%q", ce.line)
		}
		if ents, _ := os.ReadDir(filepath.Dir(project)); len(ents) != 0 {
			t.Errorf("%q: left behind %v", out, ents)
		}
	}
}

func TestCloneProjectRefusesOtherAddresses(t *testing.T) {
	for _, u := range []string{"http://github.com/acme/api", "file:///etc", "git@github.com:acme/api", "https://x@github.com/acme/api", "https://gitlab.com/a/b"} {
		var calls []cloneCall
		if _, err := cloneProject(context.Background(), cloneRequest{Project: filepath.Join(t.TempDir(), "acme"), URL: u, Copies: 1}, fakeClone(&calls, madeRepo), plentyFree); err == nil || len(calls) != 0 {
			t.Errorf("%q: %v %v", u, err, calls)
		}
	}
}

// With git itself, against a local repository: a real clone, its remote
// the plain address. (Tests allow file:// in place of https.)
func TestCloneProjectWithGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	src := filepath.Join(t.TempDir(), "src")
	for _, args := range [][]string{{"init", "-q", "-b", "main", src}, {"-C", src, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "one"}} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil { // #nosec G204 -- the test's own setup
			t.Fatalf("%v: %s", err, out)
		}
	}
	project := filepath.Join(t.TempDir(), "acme")
	run := gitCloneRunner(git)
	res, err := cloneProject(context.Background(), cloneRequest{Project: project, URL: "https://github.com/acme/api", Copies: 1, testSource: "file://" + src}, run, plentyFree)
	if err != nil || res.Existed {
		t.Fatalf("%+v %v", res, err)
	}
	out, err := exec.Command(git, "-C", project, "remote", "get-url", "origin").CombinedOutput() // #nosec G204 -- the test's own check
	if err != nil || strings.TrimSpace(string(out)) != "https://github.com/acme/api" {
		t.Fatalf("origin %q %v", out, err)
	}
	if out, _ := exec.Command(git, "-C", project, "symbolic-ref", "HEAD").CombinedOutput(); strings.TrimSpace(string(out)) != "refs/heads/main" { // #nosec G204
		t.Fatalf("HEAD %q", out)
	}
}

// Through the RPC: only a project folder in work's home, and the refusal
// codes the app tells apart.
func TestCloneProjectRPC(t *testing.T) {
	s, _ := newBoardServer(t)
	for _, bad := range []string{"/home/work", "/home/other/acme", "/home/work/a/b", "/home/work/.x"} {
		_, err := s.CloneProject(context.Background(), &agentv1.CloneProjectRequest{ProjectDir: bad, Url: "https://github.com/acme/api", Copies: 1})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for kind, code := range map[cloneErrorKind]codes.Code{cloneNeedsSignIn: codes.Unauthenticated, cloneTooBig: codes.ResourceExhausted, cloneFailed: codes.FailedPrecondition} {
		if got := status.Code(cloneStatus(&cloneError{kind: kind, line: "x"})); got != code {
			t.Errorf("%v: %v", kind, got)
		}
	}
}
