// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A pushable smart-HTTP server (git http-backend with receive-pack on)
// over a bare repository with main, and a local clone of it to push from.
type pushRig struct {
	t                *testing.T
	git, url, remote string
	local            string
	auths            []string
}

func newPushRig(t *testing.T, token string) *pushRig {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	r := &pushRig{t: t, git: git}
	root := t.TempDir()
	r.remote = filepath.Join(root, "api.git")
	r.local = filepath.Join(t.TempDir(), "local")
	seed := filepath.Join(t.TempDir(), "seed")
	r.run("", "init", "-q", "-b", "main", seed)
	r.commit(seed, "one")
	r.run("", "clone", "-q", "--bare", seed, r.remote)
	r.run(r.remote, "config", "http.receivepack", "true")
	r.run("", "clone", "-q", r.remote, r.local)
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.auths = append(r.auths, req.Header.Get("Authorization"))
		if _, pw, ok := req.BasicAuth(); token != "" && (!ok || pw != token) {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.url = srv.URL + "/api.git"
	return r
}

func (r *pushRig) run(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.git, args...) // #nosec G204 -- the test's own setup
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+r.t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *pushRig) commit(dir, msg string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, msg+".txt"), []byte(msg), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.run(dir, "add", ".")
	r.run(dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", msg)
	return r.run(dir, "rev-parse", "HEAD")
}

// bundle is what git-remote-portenv makes: temporary refs for the new
// objects, --not the remote's current values.
func (r *pushRig) bundle(news []string, nots ...string) []byte {
	r.t.Helper()
	var refs []string
	for i, n := range news {
		ref := "refs/portenv-push/" + string(rune('0'+i))
		r.run(r.local, "update-ref", ref, n)
		refs = append(refs, ref)
	}
	file := filepath.Join(r.t.TempDir(), "p.bundle")
	args := append([]string{"bundle", "create", "-q", file}, refs...)
	if len(nots) > 0 {
		args = append(append(args, "--not"), nots...)
	}
	r.run(r.local, args...)
	for _, ref := range refs {
		r.run(r.local, "update-ref", "-d", ref)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r *pushRig) remoteRef(name string) string {
	r.t.Helper()
	cmd := exec.Command(r.git, "rev-parse", "--verify", "--quiet", name) // #nosec G204 -- the test's own check
	cmd.Dir = r.remote
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func ok(t *testing.T, st []RefStatus) {
	t.Helper()
	for _, s := range st {
		if !s.OK {
			t.Fatalf("%s: %s", s.Dst, s.Reason)
		}
	}
}

func TestListRemoteRefs(t *testing.T) {
	r := newPushRig(t, "tok")
	refs, err := ListRemote(context.Background(), http.DefaultClient, r.url, "tok")
	if err != nil || len(refs) != 1 || refs[0].Name != "refs/heads/main" || refs[0].SHA != r.remoteRef("main") {
		t.Fatalf("%v %v", refs, err)
	}
	if _, err := ListRemote(context.Background(), http.DefaultClient, r.url, ""); !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("no sign-in: %v", err)
	}
}

func TestPushAFastForward(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	n := r.commit(r.local, "two")
	st, err := Push(context.Background(), http.DefaultClient, r.url, "tok",
		[]Update{{Dst: "refs/heads/main", New: n, Old: old}}, bytes.NewReader(r.bundle([]string{n}, old)))
	if err != nil {
		t.Fatal(err)
	}
	ok(t, st)
	if r.remoteRef("main") != n {
		t.Fatal("main didn't move")
	}
	for _, a := range r.auths {
		if a != "" && !strings.HasPrefix(a, "Basic ") {
			t.Fatalf("auth %q", a)
		}
	}
}

func TestPushRefusesANonFastForwardWithoutForce(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	r.run(r.local, "checkout", "-q", "--orphan", "other")
	n := r.commit(r.local, "elsewhere")
	b := r.bundle([]string{n}, old)
	st, err := Push(context.Background(), http.DefaultClient, r.url, "tok", []Update{{Dst: "refs/heads/main", New: n, Old: old}}, bytes.NewReader(b))
	if err != nil || len(st) != 1 || st[0].OK || st[0].Reason != "non-fast-forward" || r.remoteRef("main") != old {
		t.Fatalf("%v %v", st, err)
	}
	// With force, it goes.
	st, err = Push(context.Background(), http.DefaultClient, r.url, "tok", []Update{{Dst: "refs/heads/main", New: n, Old: old, Force: true}}, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	ok(t, st)
	if r.remoteRef("main") != n {
		t.Fatal("the force push didn't land")
	}
}

// The remote moved after list: fetch first, whatever the bundle says.
func TestPushRefusesWhenTheRemoteMoved(t *testing.T) {
	r := newPushRig(t, "tok")
	n := r.commit(r.local, "two")
	st, err := Push(context.Background(), http.DefaultClient, r.url, "tok",
		[]Update{{Dst: "refs/heads/main", New: n, Old: strings.Repeat("1", 40)}}, bytes.NewReader(r.bundle([]string{n}, r.remoteRef("main"))))
	if err != nil || st[0].OK || st[0].Reason != "fetch first" {
		t.Fatalf("%v %v", st, err)
	}
}

func TestPushANewBranchATagAndADelete(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	n := r.commit(r.local, "two")
	r.run(r.local, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "tag", "-a", "-m", "v1", "v1")
	tag := r.run(r.local, "rev-parse", "v1")
	st, err := Push(context.Background(), http.DefaultClient, r.url, "tok",
		[]Update{{Dst: "refs/heads/feature", New: n}, {Dst: "refs/tags/v1", New: tag}}, bytes.NewReader(r.bundle([]string{n, tag}, old)))
	if err != nil {
		t.Fatal(err)
	}
	ok(t, st)
	if r.remoteRef("feature") != n || r.remoteRef("refs/tags/v1") != tag {
		t.Fatal("not pushed")
	}
	st, err = Push(context.Background(), http.DefaultClient, r.url, "tok", []Update{{Dst: "refs/heads/feature", Old: n, Force: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ok(t, st)
	if r.remoteRef("feature") != "" {
		t.Fatal("feature wasn't deleted")
	}
}

// A new branch at a commit GitHub has: no bundle (git makes none), and it
// goes; a new object with no bundle doesn't.
func TestPushWithNoNewObjects(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	st, err := Push(context.Background(), http.DefaultClient, r.url, "tok", []Update{{Dst: "refs/heads/again", New: old}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ok(t, st)
	if r.remoteRef("again") != old {
		t.Fatal("not pushed")
	}
	n := r.commit(r.local, "two")
	if _, err := Push(context.Background(), http.DefaultClient, r.url, "tok", []Update{{Dst: "refs/heads/x", New: n}}, nil); err == nil {
		t.Fatal("a new object with no bundle went")
	}
}

func TestPushRefusesWhatItDoesntTake(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	n := r.commit(r.local, "two")
	good := r.bundle([]string{n}, old)
	for name, tc := range map[string]struct {
		updates []Update
		bundle  []byte
	}{
		"a pull ref":         {[]Update{{Dst: "refs/pull/1/head", New: n}}, good},
		"HEAD":               {[]Update{{Dst: "HEAD", New: n}}, good},
		"a ref with ..":      {[]Update{{Dst: "refs/heads/../x", New: n}}, good},
		"twice":              {[]Update{{Dst: "refs/heads/a", New: n}, {Dst: "refs/heads/a", New: n}}, good},
		"no update":          {nil, good},
		"a bad sha":          {[]Update{{Dst: "refs/heads/a", New: "zz"}}, good},
		"not in the bundle":  {[]Update{{Dst: "refs/heads/a", New: strings.Repeat("2", 40)}}, good},
		"another bundle ref": {[]Update{{Dst: "refs/heads/a", New: n}}, bytes.Replace(good, []byte("refs/portenv-push/0"), []byte("refs/heads/sneak00"), 1)},
		"not a bundle":       {[]Update{{Dst: "refs/heads/a", New: n}}, []byte("hello")},
	} {
		var rd io.ReadSeeker
		if tc.bundle != nil {
			rd = bytes.NewReader(tc.bundle)
		}
		if _, err := Push(context.Background(), http.DefaultClient, r.url, "tok", tc.updates, rd); err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	if r.remoteRef("main") != old || r.remoteRef("refs/heads/a") != "" {
		t.Fatal("something was pushed")
	}
}

func TestPushCapsTheBundle(t *testing.T) {
	r := newPushRig(t, "tok")
	n := r.commit(r.local, "two")
	b := r.bundle([]string{n}, r.remoteRef("main"))
	if _, err := pushWithLimit(context.Background(), http.DefaultClient, r.url, "tok", []Update{{Dst: "refs/heads/main", New: n, Old: r.remoteRef("main")}}, bytes.NewReader(b), int64(len(b)-1)); !errors.Is(err, ErrTooBig) {
		t.Fatalf("%v", err)
	}
}

// R-0393: a crafted bundle naming the current tip as a prerequisite, with
// an unrelated commit as the new value and no force, isn't a fast-forward:
// ancestry is proven from the pack, never taken from the prerequisites.
func TestPushRefusesACraftedFastForward(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	r.run(r.local, "checkout", "-q", "--orphan", "unrelated")
	n := r.commit(r.local, "unrelated")
	b := r.bundle([]string{n})
	// The prerequisite line a real fast-forward would have.
	crafted := bytes.Replace(b, []byte("# v2 git bundle\n"), []byte("# v2 git bundle\n-"+old+" one\n"), 1)
	if ff := FastForwards(bytes.NewReader(crafted), []Update{{Dst: "refs/heads/main", New: n, Old: old}}); ff["refs/heads/main"] {
		t.Fatal("the crafted bundle was taken for a fast-forward")
	}
	st, err := Push(context.Background(), http.DefaultClient, r.url, "tok", []Update{{Dst: "refs/heads/main", New: n, Old: old}}, bytes.NewReader(crafted))
	if err != nil || st[0].OK || st[0].Reason != "non-fast-forward" || r.remoteRef("main") != old {
		t.Fatalf("%v %v", st, err)
	}
}

// A real fast-forward, two commits on, is proven.
func TestAFastForwardIsProven(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	r.commit(r.local, "two")
	n := r.commit(r.local, "three")
	b := r.bundle([]string{n}, old)
	if ff := FastForwards(bytes.NewReader(b), []Update{{Dst: "refs/heads/main", New: n, Old: old}}); !ff["refs/heads/main"] {
		t.Fatal("a real fast-forward wasn't proven")
	}
}
