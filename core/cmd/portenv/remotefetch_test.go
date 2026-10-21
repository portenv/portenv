// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/portenv/portenv/core/internal/githubrepo"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// git fetch, pull and ls-remote through git-remote-portenv (R-0392). The
// socket here plays the helper and portenvd together: it reads GitHub (a
// git http-backend) with githubrepo, as portenvd does, and hands back the
// bundle after its answer, as the helper does.

type fetchSocket struct {
	url string
	// mangle changes the bundle before it's handed back.
	mangle func([]byte) []byte
	calls  []*agentv1.TeamCallsResponse
}

func (f *fetchSocket) serve(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return sock
}

func (f *fetchSocket) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	line, _ := bufio.NewReader(c).ReadBytes('\n')
	var call agentv1.TeamCallsResponse
	_ = protojson.Unmarshal(line, &call)
	f.calls = append(f.calls, &call)
	reply := &agentv1.TeamCallsRequest{Ok: true}
	var body []byte
	switch {
	case call.GetPushList() != nil:
		refs, head, err := githubrepo.ListForFetch(context.Background(), http.DefaultClient, f.url, "")
		if err != nil {
			reply = &agentv1.TeamCallsRequest{Line: err.Error()}
			break
		}
		out := &agentv1.PushListResult{Head: head}
		for _, r := range refs {
			out.Refs = append(out.Refs, &agentv1.RemoteRef{Name: r.Name, Sha: r.SHA})
		}
		reply.Result = &agentv1.TeamCallsRequest_PushList{PushList: out}
	case call.GetFetch() != nil:
		var wants []githubrepo.Ref
		for _, w := range call.GetFetch().GetWants() {
			wants = append(wants, githubrepo.Ref{Name: w.GetName(), SHA: w.GetSha()})
		}
		var b bytes.Buffer
		if err := githubrepo.FetchRefs(context.Background(), http.DefaultClient, f.url, "", wants, call.GetFetch().GetHaves(), &b, 1<<30); err != nil {
			reply = &agentv1.TeamCallsRequest{Line: err.Error()}
			break
		}
		body = b.Bytes()
		if f.mangle != nil {
			body = f.mangle(body)
		}
		reply.Result = &agentv1.TeamCallsRequest_Fetch{Fetch: &agentv1.FetchResult{BundleBytes: int64(len(body))}}
	}
	out, _ := protojson.Marshal(reply)
	_, _ = c.Write(append(out, '\n'))
	_, _ = c.Write(body)
}

// fetchRig: GitHub (a bare repository behind git http-backend), a working
// clone whose origin is portenv::github, and git-remote-portenv on PATH.
type fetchRig struct {
	t                     *testing.T
	bare, other, box, bin string
	sock                  string
	sockets               *fetchSocket
}

func newFetchRig(t *testing.T) *fetchRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	r := &fetchRig{t: t}
	root := t.TempDir()
	r.bare = filepath.Join(root, "api.git")
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, "", "init", "-q", "-b", "main", seed)
	r.commit(seed, "one")
	git(t, "", "clone", "-q", "--bare", seed, r.bare)
	r.other = filepath.Join(t.TempDir(), "other")
	git(t, "", "clone", "-q", r.bare, r.other)
	r.box = filepath.Join(t.TempDir(), "box")
	git(t, "", "clone", "-q", r.bare, r.box)
	git(t, r.box, "remote", "set-url", "origin", "portenv::github")
	srv := httptest.NewServer(&cgi.Handler{Path: mustGit(t), Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(srv.Close)
	r.sockets = &fetchSocket{url: srv.URL + "/api.git"}
	r.sock = r.sockets.serve(t)
	r.bin = filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(r.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(r.bin, "git-remote-portenv"), ".") // #nosec G204 -- this package
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	return r
}

func mustGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	return p
}

func (r *fetchRig) commit(dir, msg string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, msg), []byte(msg), 0o644); err != nil {
		r.t.Fatal(err)
	}
	git(r.t, dir, "add", ".")
	git(r.t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", msg)
	return git(r.t, dir, "rev-parse", "HEAD")
}

// inBox runs git in the workspace's clone, through git-remote-portenv.
func (r *fetchRig) inBox(args ...string) (string, error) {
	cmd := exec.Command("git", args...) // #nosec G204 -- the test's own git
	cmd.Dir = r.box
	// No background gc or maintenance after a pull: it would still be
	// writing in the clone when the test's folders are removed.
	cmd.Env = append(os.Environ(), "PATH="+r.bin+":"+os.Getenv("PATH"), "PORTENV_PUSH_SOCKET="+r.sock, "GIT_CONFIG_NOSYSTEM=1", "HOME="+r.t.TempDir(),
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestGitFetchPullAndLsRemoteThroughPortenv(t *testing.T) {
	r := newFetchRig(t)
	old := git(t, r.box, "rev-parse", "HEAD")
	n := r.commit(r.other, "two")
	git(t, r.other, "tag", "v2")
	git(t, r.other, "push", "-q", "origin", "main", "v2")

	out, err := r.inBox("ls-remote", "origin")
	if err != nil || !strings.Contains(out, n+"\trefs/heads/main") || !strings.Contains(out, n+"\trefs/tags/v2") {
		t.Fatalf("ls-remote: %v %s", err, out)
	}
	if out, err := r.inBox("fetch", "origin"); err != nil {
		t.Fatalf("fetch: %v %s", err, out)
	}
	if got := git(t, r.box, "rev-parse", "refs/remotes/origin/main"); got != n {
		t.Fatalf("origin/main is %s", got)
	}
	if got := git(t, r.box, "rev-parse", "v2"); got != n {
		t.Fatalf("v2 is %s", got)
	}
	// The fetch said what the box has, so the bundle was only the new.
	var fetch *agentv1.FetchRefs
	for _, c := range r.sockets.calls {
		if c.GetFetch() != nil {
			fetch = c.GetFetch()
		}
	}
	if fetch == nil || !contains(fetch.GetHaves(), old) {
		t.Fatalf("the fetch didn't name what the box has: %v", fetch)
	}
	// And pull.
	n2 := r.commit(r.other, "three")
	git(t, r.other, "push", "-q", "origin", "main")
	if out, err := r.inBox("-c", "pull.ff=only", "pull", "origin", "main"); err != nil {
		t.Fatalf("pull: %v %s", err, out)
	}
	if got := git(t, r.box, "rev-parse", "HEAD"); got != n2 {
		t.Fatalf("HEAD is %s", got)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// A bundle that isn't what was asked for, or doesn't verify, is refused
// and nothing is unbundled.
func TestAFetchsBundleIsCheckedFirst(t *testing.T) {
	for name, mangle := range map[string]func([]byte) []byte{
		"a pull ref": func(b []byte) []byte {
			return bytes.Replace(b, []byte(" refs/heads/main"), []byte(" refs/pull/1/he"), 1)
		},
		"a cut pack":   func(b []byte) []byte { return b[:len(b)-30] },
		"not a bundle": func([]byte) []byte { return []byte("hello\n\n") },
		"a ref with ..": func(b []byte) []byte {
			return bytes.Replace(b, []byte(" refs/heads/main"), []byte(" refs/heads/.."), 1)
		},
		"a want missing": func(b []byte) []byte {
			return bytes.Replace(b, []byte(" refs/heads/main"), []byte(" refs/heads/nain"), 1)
		},
		"version 3": func(b []byte) []byte { return append([]byte("# v3 git bundle\n"), b[len("# v2 git bundle\n"):]...) },
	} {
		r := newFetchRig(t)
		n := r.commit(r.other, "two")
		git(t, r.other, "push", "-q", "origin", "main")
		r.sockets.mangle = mangle
		if out, err := r.inBox("fetch", "origin"); err == nil {
			t.Errorf("%s: fetched: %s", name, out)
		}
		if exec.Command("git", "-C", r.box, "cat-file", "-e", n).Run() == nil { // #nosec G204 -- the test's own check
			t.Errorf("%s: the new commit got in", name)
		}
	}
}
