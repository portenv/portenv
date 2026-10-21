// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A git smart-HTTP server over a local repository (git http-backend), as
// GitHub serves it; with a token, it answers only that token.
func gitServer(t *testing.T, token string) (url, src string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	src = filepath.Join(root, "api.git")
	work := filepath.Join(t.TempDir(), "w")
	run := func(dir string, args ...string) {
		cmd := exec.Command(git, args...) // #nosec G204 -- the test's own setup
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(root, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", ".")
	run(work, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "one")
	run(work, "tag", "v1")
	run(work, "branch", "dev")
	run(root, "clone", "-q", "--bare", work, src)
	run(src, "update-ref", "refs/pull/1/head", "HEAD") // GitHub's pull refs: never fetched
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			if _, pw, ok := r.BasicAuth(); !ok || pw != token {
				w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api.git", src
}

func cloneBundle(t *testing.T, bundle []byte) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "b.bundle")
	if err := os.WriteFile(file, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "clone", "-q", file, filepath.Join(dir, "c")).CombinedOutput() // #nosec G204 -- the test's own check
	if err != nil {
		t.Fatalf("the bundle doesn't clone: %v %s", err, out)
	}
	return filepath.Join(dir, "c")
}

func TestFetchBundleIsAWholeBundleOfBranchesAndTags(t *testing.T) {
	url, _ := gitServer(t, "")
	var b bytes.Buffer
	res, err := FetchBundle(context.Background(), http.DefaultClient, url, "", &b, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Head != "refs/heads/main" || res.Empty {
		t.Fatalf("%+v", res)
	}
	header, _, _ := bytes.Cut(b.Bytes(), []byte("\n\n"))
	h := string(header)
	if !strings.HasPrefix(h, "# v2 git bundle\n") || !strings.Contains(h, " refs/heads/main") || !strings.Contains(h, " refs/heads/dev") ||
		!strings.Contains(h, " refs/tags/v1") || strings.Contains(h, "refs/pull") || strings.Contains(h, "\n-") {
		t.Fatalf("header:\n%s", h)
	}
	c := cloneBundle(t, b.Bytes())
	if got, _ := os.ReadFile(filepath.Join(c, "README.md")); string(got) != "hello\n" {
		t.Fatalf("README %q", got)
	}
}

func TestFetchBundleWithASignIn(t *testing.T) {
	url, _ := gitServer(t, "s3cr3t-token")
	var b bytes.Buffer
	if _, err := FetchBundle(context.Background(), http.DefaultClient, url, "", &b, 1<<20); !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("no token: %v", err)
	}
	if _, err := FetchBundle(context.Background(), http.DefaultClient, url, "wrong", &b, 1<<20); !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("a wrong token: %v", err)
	}
	b.Reset()
	if _, err := FetchBundle(context.Background(), http.DefaultClient, url, "s3cr3t-token", &b, 1<<20); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b.Bytes(), []byte("s3cr3t-token")) {
		t.Fatal("the token is in the bundle")
	}
	cloneBundle(t, b.Bytes())
}

func TestFetchBundleStopsAtItsLimit(t *testing.T) {
	url, _ := gitServer(t, "")
	var b bytes.Buffer
	if _, err := FetchBundle(context.Background(), http.DefaultClient, url, "", &b, 100); !errors.Is(err, ErrTooBig) {
		t.Fatalf("%v", err)
	}
}

func TestFetchBundleOfAnEmptyRepository(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	if out, err := exec.Command(git, "init", "-q", "--bare", filepath.Join(root, "e.git")).CombinedOutput(); err != nil { // #nosec G204
		t.Fatalf("%v %s", err, out)
	}
	srv := httptest.NewServer(&cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	defer srv.Close()
	var b bytes.Buffer
	res, err := FetchBundle(context.Background(), http.DefaultClient, srv.URL+"/e.git", "", &b, 1<<20)
	if err != nil || !res.Empty || b.Len() != 0 {
		t.Fatalf("%+v %v %d", res, err, b.Len())
	}
}
