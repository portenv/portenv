// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// New workspace's From GitHub (R-0391): portenvd fetches on the Mac and
// streams a whole bundle; the helper checks it, then clones from that file
// as work, with no network and nothing of git's config but the project's.

const projectURL = "https://github.com/acme/api"

func needGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	return git
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(needGit(t), args...) // #nosec G204 -- the test's own setup
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sourceRepo is a repository with main, dev and a tag, bundled by git
// (the format FetchBundle writes) with the refs given.
func sourceRepo(t *testing.T, refs ...string) []byte {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	runGit(t, t.TempDir(), "init", "-q", "-b", "main", src)
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", ".")
	runGit(t, src, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "one")
	runGit(t, src, "tag", "v1")
	runGit(t, src, "branch", "dev")
	runGit(t, src, "update-ref", "refs/pull/1/head", "HEAD")
	if len(refs) == 0 {
		refs = []string{"refs/heads/main", "refs/heads/dev", "refs/tags/v1", "HEAD"}
	}
	file := filepath.Join(t.TempDir(), "b.bundle")
	runGit(t, src, append([]string{"bundle", "create", "-q", file}, refs...)...)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func clone(t *testing.T, home string, bundle []byte, max int64, head string) (CloneProjectResult, error) {
	t.Helper()
	return cloneFromBundle(context.Background(), cloneBundleRequest{Project: filepath.Join(home, "acme"), URL: projectURL, MaxBytes: max, Head: head},
		bytes.NewReader(bundle), bundleGit(needGit(t)))
}

// left is what's in the home besides .portenv, and in .portenv/clone.
func left(home string) []string {
	var names []string
	ents, _ := os.ReadDir(home)
	for _, e := range ents {
		if e.Name() != ".portenv" {
			names = append(names, e.Name())
		}
	}
	clones, _ := os.ReadDir(filepath.Join(home, ".portenv", "clone"))
	for _, e := range clones {
		names = append(names, ".portenv/clone/"+e.Name())
	}
	return names
}

func TestCloneFromABundle(t *testing.T) {
	home := t.TempDir()
	res, err := clone(t, home, sourceRepo(t), 10<<20, "refs/heads/main")
	if err != nil || res.Existed {
		t.Fatalf("%+v %v", res, err)
	}
	p := filepath.Join(home, "acme")
	if b, _ := os.ReadFile(filepath.Join(p, "README.md")); string(b) != "hello\n" {
		t.Fatalf("README %q", b)
	}
	if got := runGit(t, p, "remote", "get-url", "origin"); got != projectURL {
		t.Fatalf("origin %q", got)
	}
	if got := runGit(t, p, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD %q", got)
	}
	if got := runGit(t, p, "rev-parse", "--abbrev-ref", "main@{upstream}"); got != "origin/main" {
		t.Fatalf("upstream %q", got)
	}
	if got := runGit(t, p, "for-each-ref", "--format=%(refname)"); !strings.Contains(got, "refs/remotes/origin/dev") || !strings.Contains(got, "refs/tags/v1") {
		t.Fatalf("refs %q", got)
	}
	cfg, _ := os.ReadFile(filepath.Join(p, ".git", "config"))
	if bytes.Contains(cfg, []byte("bundle")) || bytes.Contains(cfg, []byte("@")) {
		t.Fatalf(".git/config names the bundle or a user:\n%s", cfg)
	}
	if ents, _ := os.ReadDir(filepath.Join(p, ".git", "hooks")); len(ents) != 0 {
		t.Fatal("hooks came from a template")
	}
	if got := left(home); strings.Join(got, ",") != "acme" {
		t.Fatalf("left behind: %v", got)
	}
}

// A refused bundle leaves no project and no partial clone.
func TestABundleIsCheckedFirst(t *testing.T) {
	good := sourceRepo(t)
	header, pack, _ := bytes.Cut(good, []byte("\n\n"))
	prereq := append([]byte("# v2 git bundle\n-"+strings.Repeat("a", 40)+" one\n"), header[len("# v2 git bundle\n"):]...)
	prereq = append(append(prereq, '\n', '\n'), pack...)
	for name, tc := range map[string]struct {
		bundle []byte
		max    int64
		kind   cloneErrorKind
	}{
		"a pull ref":          {sourceRepo(t, "refs/heads/main", "refs/pull/1/head"), 10 << 20, cloneFailed},
		"over the limit":      {good, int64(len(good) - 1), cloneTooBig},
		"a cut pack":          {good[:len(good)-30], 10 << 20, cloneFailed},
		"a prerequisite":      {prereq, 10 << 20, cloneFailed},
		"version 3":           {append([]byte("# v3 git bundle\n@object-format=sha1\n"), good[len("# v2 git bundle\n"):]...), 10 << 20, cloneFailed},
		"not a bundle":        {[]byte("hello\n"), 10 << 20, cloneFailed},
		"a ref with ..":       {bytes.Replace(good, []byte("refs/heads/dev"), []byte("refs/heads/.."), 1), 10 << 20, cloneFailed},
		"no header end":       {[]byte("# v2 git bundle\n" + strings.Repeat("a", 40) + " refs/heads/main\n"), 10 << 20, cloneFailed},
		"empty":               {nil, 10 << 20, cloneFailed},
		"a ref outside heads": {bytes.Replace(good, []byte("refs/tags/v1"), []byte("refs/note/v1"), 1), 10 << 20, cloneFailed},
	} {
		home := t.TempDir()
		_, err := clone(t, home, tc.bundle, tc.max, "refs/heads/main")
		var ce *cloneError
		if !errors.As(err, &ce) || ce.kind != tc.kind {
			t.Errorf("%s: %v", name, err)
		}
		if got := left(home); len(got) != 0 {
			t.Errorf("%s: left behind %v", name, got)
		}
	}
}

func TestCloneKeepsAnExistingProject(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := clone(t, home, nil, 10<<20, "")
	if err != nil || !res.Existed {
		t.Fatalf("%+v %v", res, err)
	}
}

// An empty repository on GitHub: an empty project with git, its origin set.
func TestCloneOfAnEmptyRepository(t *testing.T) {
	home := t.TempDir()
	_, err := cloneFromBundle(context.Background(), cloneBundleRequest{Project: filepath.Join(home, "acme"), URL: projectURL, Empty: true},
		bytes.NewReader(nil), bundleGit(needGit(t)))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "acme")
	if runGit(t, p, "remote", "get-url", "origin") != projectURL || runGit(t, p, "symbolic-ref", "HEAD") != "refs/heads/main" {
		t.Fatal("not an empty project with its origin")
	}
}

func TestCloneRefusesOtherAddresses(t *testing.T) {
	for _, u := range []string{"http://github.com/acme/api", "file:///etc", "https://x@github.com/acme/api", "https://gitlab.com/a/b"} {
		home := t.TempDir()
		_, err := cloneFromBundle(context.Background(), cloneBundleRequest{Project: filepath.Join(home, "acme"), URL: u, MaxBytes: 1 << 20},
			bytes.NewReader(sourceRepo(t)), bundleGit(needGit(t)))
		if err == nil {
			t.Errorf("%q: not refused", u)
		}
	}
}

// The RPC: the first message names the project, the chunks are the bundle.
type cloneStream struct {
	msgs []*agentv1.CloneProjectRequest
	resp *agentv1.CloneProjectResponse
}

func (c *cloneStream) Recv() (*agentv1.CloneProjectRequest, error) {
	if len(c.msgs) == 0 {
		return nil, io.EOF
	}
	m := c.msgs[0]
	c.msgs = c.msgs[1:]
	return m, nil
}
func (c *cloneStream) SendAndClose(r *agentv1.CloneProjectResponse) error { c.resp = r; return nil }
func (c *cloneStream) Context() context.Context                           { return context.Background() }
func (c *cloneStream) SetHeader(metadata.MD) error                        { return nil }
func (c *cloneStream) SendHeader(metadata.MD) error                       { return nil }
func (c *cloneStream) SetTrailer(metadata.MD)                             {}
func (c *cloneStream) SendMsg(any) error                                  { return nil }
func (c *cloneStream) RecvMsg(any) error                                  { return nil }

func TestCloneProjectRPC(t *testing.T) {
	s, home := newBoardServer(t)
	bundle := sourceRepo(t)
	start := &agentv1.CloneProjectRequest{Msg: &agentv1.CloneProjectRequest_Start{Start: &agentv1.CloneProjectStart{
		ProjectDir: "/home/work/acme", Url: projectURL, MaxBytes: 10 << 20, Head: "refs/heads/main"}}}
	st := &cloneStream{msgs: []*agentv1.CloneProjectRequest{start,
		{Msg: &agentv1.CloneProjectRequest_Chunk{Chunk: bundle[:100]}}, {Msg: &agentv1.CloneProjectRequest_Chunk{Chunk: bundle[100:]}}}}
	if err := s.CloneProject(st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "work", "acme", "README.md")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"/home/work", "/home/other/acme", "/home/work/a/b", "/home/work/.x"} {
		st := &cloneStream{msgs: []*agentv1.CloneProjectRequest{{Msg: &agentv1.CloneProjectRequest_Start{Start: &agentv1.CloneProjectStart{
			ProjectDir: bad, Url: projectURL, MaxBytes: 1 << 20}}}}}
		if err := s.CloneProject(st); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for kind, code := range map[cloneErrorKind]codes.Code{cloneTooBig: codes.ResourceExhausted, cloneFailed: codes.FailedPrecondition} {
		if got := status.Code(cloneStatus(&cloneError{kind: kind, line: "x"})); got != code {
			t.Errorf("%v: %v", kind, got)
		}
	}
}

func TestProjectRoom(t *testing.T) {
	s, home := newBoardServer(t)
	r, err := s.GetProjectRoom(context.Background(), &agentv1.GetProjectRoomRequest{ProjectDir: "/home/work/acme"})
	if err != nil || r.GetExists() || r.GetFreeBytes() == 0 {
		t.Fatalf("%v %v", r, err)
	}
	if err := os.MkdirAll(filepath.Join(home, "work", "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetProjectRoom(context.Background(), &agentv1.GetProjectRoomRequest{ProjectDir: "/home/work/acme"}); !r.GetExists() {
		t.Fatal("the project is there")
	}
}
