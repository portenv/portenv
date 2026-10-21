// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// New workspace's Project step (ADR 0025 §2, R-0391): portenvd fetches on
// the Mac (with the owner's sign-in only for a private repository, in
// memory), streams a whole bundle to the helper, then writes the team into
// the cloned project. A failed fetch or clone writes and pins nothing,
// keeps the setup for Try Again, and the sign-in is nowhere afterwards.

// The sign-ins are made at run time, so the test's own binary holds
// neither.
var (
	testToken  = "gho_" + randomHex(16)
	wrongToken = "gho_" + randomHex(16)
)

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// fakeGitHub is GitHub for one repository, acme/api: its git smart HTTP
// (git http-backend over a local bare repository) and its API's size. With
// private, both answer only testToken.
type fakeGitHub struct {
	git, api string
	auths    []string
}

func newFakeGitHub(t *testing.T, private bool) *fakeGitHub {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	work := filepath.Join(t.TempDir(), "w")
	run := func(dir string, args ...string) {
		cmd := exec.Command(git, args...) // #nosec G204 -- the test's own setup
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(root, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", ".")
	run(work, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "one")
	run(root, "clone", "-q", "--bare", work, filepath.Join(root, "acme", "api"))
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	f := &fakeGitHub{}
	allowed := func(r *http.Request) bool {
		got := r.Header.Get("Authorization")
		f.auths = append(f.auths, got)
		if !private {
			return true
		}
		_, pw, ok := r.BasicAuth()
		return (ok && pw == testToken) || got == "Bearer "+testToken
	}
	gitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"size": 1}`))
	}))
	t.Cleanup(gitSrv.Close)
	t.Cleanup(apiSrv.Close)
	f.git, f.api = gitSrv.URL, apiSrv.URL
	return f
}

func githubURL(u string, signIn bool) *daemonv1.ProjectSource {
	return &daemonv1.ProjectSource{Source: &daemonv1.ProjectSource_GithubUrl{GithubUrl: u}, UseSignIn: signIn}
}

func emptyProject() *daemonv1.ProjectSource {
	return &daemonv1.ProjectSource{Source: &daemonv1.ProjectSource_Empty{Empty: true}}
}

// withGitHub points the server at a fake GitHub before it serves.
func withGitHub(gh *fakeGitHub) func(*Server) {
	return func(s *Server) { s.githubAPI, s.githubGit = gh.api, gh.git }
}

func TestApplyClonesThenWritesTheTeam(t *testing.T) {
	gh := newFakeGitHub(t, false)
	s, fake, c := servingProject(t, withGitHub(gh))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := keepSetup(filepath.Join(s.env.Dir, "state", "box-1"), &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api.git", false), Team: approvedChoice(t, "starter")}); err != nil {
		t.Fatal(err)
	}
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetApplied() || r.GetProjectError() != "" || len(r.GetPinned()) != 3 {
		t.Fatalf("%v %v", r, err)
	}
	if len(fake.clones) != 1 {
		t.Fatalf("clones %v", fake.clones)
	}
	st := fake.clones[0]
	if st.GetUrl() != "https://github.com/acme/api" || st.GetProjectDir() != "/home/work/acme-api" || st.GetHead() != "refs/heads/main" || st.GetMaxBytes() <= 0 {
		t.Fatalf("start %v", st)
	}
	if !bytes.HasPrefix(fake.bundles[0], []byte("# v2 git bundle\n")) {
		t.Fatalf("not a bundle: %q", fake.bundles[0][:min(40, len(fake.bundles[0]))])
	}
	for _, a := range gh.auths {
		if a != "" {
			t.Fatalf("a public clone sent a sign-in: %q", a)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(fake.root, "acme-api", "README.md")); string(b) != "cloned" {
		t.Fatal("the team wasn't written into the cloned project")
	}
	if _, err := os.Stat(filepath.Join(fake.root, "acme-api", ".portenv", "agents", "lead.md")); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(filepath.Join(s.env.Dir, "clone")); len(ents) != 0 {
		t.Fatalf("the bundle was left behind: %v", ents)
	}
}

// scanFor fails if any file under roots holds b.
func scanFor(t *testing.T, b []byte, roots ...string) {
	t.Helper()
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if data, err := os.ReadFile(p); err == nil && bytes.Contains(data, b) { // #nosec G304 -- the test's own folders
				t.Errorf("the sign-in is in %s", p)
			}
			return nil
		})
	}
}

// A private repository: the sign-in is asked for only when the setup
// needs it, sent only to GitHub, and is nowhere afterwards, whether the
// clone worked or not.
func TestAPrivateCloneKeepsTheSignInOnTheMac(t *testing.T) {
	var logs bytes.Buffer
	gh := newFakeGitHub(t, true)
	s, fake, c := servingProject(t, withGitHub(gh), func(s *Server) {
		s.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	state := filepath.Join(s.env.Dir, "state", "box-1")
	if err := keepSetup(state, &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api", true), Team: approvedChoice(t, "starter")}); err != nil {
		t.Fatal(err)
	}
	// No sign-in given: asked for, nothing done.
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetNeedsToken() || r.GetApplied() || len(gh.auths) != 0 || len(fake.clones) != 0 {
		t.Fatalf("no token: %v %v %v", r, err, gh.auths)
	}
	// A sign-in GitHub refuses: the line, the setup kept.
	r, err = c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api", GithubToken: wrongToken})
	if err != nil || r.GetApplied() || !r.GetNeedsSignIn() || !strings.HasPrefix(r.GetProjectError(), "Your GitHub sign-in in Portenv has ended") || fake.writes != 0 {
		t.Fatalf("a refused sign-in: %v %v", r, err)
	}
	scanFor(t, []byte(wrongToken), s.env.Dir, fake.root, os.TempDir())
	// The right one.
	r, err = c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api", GithubToken: testToken})
	if err != nil || !r.GetApplied() || len(r.GetPinned()) != 3 {
		t.Fatalf("with the sign-in: %v %v", r, err)
	}
	for _, b := range fake.bundles {
		if bytes.Contains(b, []byte(testToken)) {
			t.Fatal("the sign-in went into the workspace with the bundle")
		}
	}
	for _, st := range fake.clones {
		if strings.Contains(st.String(), testToken) {
			t.Fatal("the sign-in went into the workspace with the clone's start")
		}
	}
	scanFor(t, []byte(testToken), s.env.Dir, fake.root, os.TempDir())
	if strings.Contains(logs.String(), testToken) || strings.Contains(logs.String(), wrongToken) {
		t.Fatal("the sign-in is in portenvd's log")
	}
}

// A failed clone: one plain line, nothing written or pinned, the setup
// kept; Try Again goes through.
func TestAFailedCloneKeepsTheSetup(t *testing.T) {
	gh := newFakeGitHub(t, false)
	s, fake, c := servingProject(t, withGitHub(gh))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	state := filepath.Join(s.env.Dir, "state", "box-1")
	if err := keepSetup(state, &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api", false), Team: approvedChoice(t, "starter")}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		set       func()
		signIn    bool
		wantStart string
	}{
		{"a private repository, no sign-in", func() { s.githubGit = gh.git + "/nope" }, true, "This repository needs a sign-in. Sign in with GitHub, then try again."},
		{"the helper refuses the bundle", func() {
			s.githubGit = gh.git
			fake.cloneErr = status.Error(codes.FailedPrecondition, "Portenv couldn’t clone github.com/acme/api: its bundle was refused.")
		}, false, "Portenv couldn’t clone github.com/acme/api"},
		{"no room", func() { fake.free = 1 << 20 }, false, "acme-api needs about"},
		{"GitHub unreachable", func() { fake.free = 0; s.githubGit = "http://127.0.0.1:1" }, false, "Portenv couldn’t clone github.com/acme/api"},
	} {
		tc.set()
		r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
		if err != nil || r.GetApplied() || r.GetNeedsSignIn() != tc.signIn || !strings.HasPrefix(r.GetProjectError(), tc.wantStart) || len(r.GetPinned()) != 0 {
			t.Fatalf("%s: %v %v", tc.name, r, err)
		}
		if fake.writes != 0 {
			t.Fatalf("%s: agent files were written after a failed clone", tc.name)
		}
		if kept, _ := loadSetup(state); kept == nil || kept.GetTeam().GetTemplate() != "starter" {
			t.Fatalf("%s: the setup wasn't kept", tc.name)
		}
		if ents, _ := os.ReadDir(filepath.Join(s.env.Dir, "clone")); len(ents) != 0 {
			t.Fatalf("%s: the bundle was left behind: %v", tc.name, ents)
		}
	}
	s.githubGit = gh.git
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetApplied() || len(r.GetPinned()) != 3 {
		t.Fatalf("Try Again: %v %v", r, err)
	}
	if kept, _ := loadSetup(state); kept != nil {
		t.Fatal("the setup is still kept after it was applied")
	}
}

// Empty with no team: the project is made (git on main), nothing else.
func TestApplyEmptyWithNoTeam(t *testing.T) {
	s, fake, c := servingProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := keepSetup(filepath.Join(s.env.Dir, "state", "box-1"), &daemonv1.WorkspaceSetup{Project: emptyProject()}); err != nil {
		t.Fatal(err)
	}
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetApplied() || len(fake.clones) != 0 || len(fake.written) != 1 || len(fake.written[0]) != 0 {
		t.Fatalf("%v %v clones %v written %v", r, err, fake.clones, fake.written)
	}
}

// A GitHub repository with no team is cloned, and that's all.
func TestApplyCloneWithNoTeam(t *testing.T) {
	s, fake, c := servingProject(t, withGitHub(newFakeGitHub(t, false)))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := keepSetup(filepath.Join(s.env.Dir, "state", "box-1"), &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api", false)}); err != nil {
		t.Fatal(err)
	}
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetApplied() || len(fake.clones) != 1 || fake.writes != 0 {
		t.Fatalf("%v %v", r, err)
	}
}

// The room: work's copy and each coding agent's, each with its checkout.
func TestCloneRoom(t *testing.T) {
	if got := cloneLimit(100_000_000_000, 4); got != (100_000_000_000-cloneMargin)/8 {
		t.Fatalf("%d", got)
	}
	if got := cloneLimit(cloneMargin/2, 1); got != 0 {
		t.Fatalf("no room: %d", got)
	}
	if got := tooBigLine("acme-api", 2_100_000_000, 3, 5_000_000_000); got != "acme-api needs about 14 GB with its team (3 copies of 2.1 GB, each with its checkout). This workspace has 5 GB free." {
		t.Fatalf("%q", got)
	}
}

// CreateBox's check: only a GitHub https address, or Empty.
func TestCheckSetup(t *testing.T) {
	ok := []*daemonv1.WorkspaceSetup{
		{Project: emptyProject()},
		{Project: githubURL("https://github.com/acme/api", false)},
		{Project: githubURL("https://github.com/acme/api", true), Team: approvedChoice(t, "starter")},
		{Team: approvedChoice(t, "starter")},
	}
	for _, s := range ok {
		if err := checkSetup(s, 4); err != nil {
			t.Errorf("%v: %v", s, err)
		}
	}
	for _, u := range []string{"http://github.com/a/b", "https://u:p@github.com/a/b", "file:///etc", "git@github.com:a/b"} {
		if err := checkSetup(&daemonv1.WorkspaceSetup{Project: githubURL(u, false)}, 4); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%q: %v", u, err)
		}
	}
	if err := checkSetup(&daemonv1.WorkspaceSetup{Project: &daemonv1.ProjectSource{}}, 4); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a project with no source: %v", err)
	}
}

// GitHub's size first: a repository too big for the workspace with its
// team is refused before any byte is fetched.
func TestTooBigIsRefusedBeforeAnyByte(t *testing.T) {
	gitHits := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"size": 80000000}`)) // 82 GB
	}))
	defer api.Close()
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { gitHits++; http.NotFound(w, nil) }))
	defer counting.Close()
	s, fake, c := servingProject(t, func(s *Server) { s.githubAPI, s.githubGit = api.URL, counting.URL })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := keepSetup(filepath.Join(s.env.Dir, "state", "box-1"), &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api", false)}); err != nil {
		t.Fatal(err)
	}
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || r.GetApplied() || !strings.HasPrefix(r.GetProjectError(), "acme-api needs about") || gitHits != 0 || len(fake.clones) != 0 {
		t.Fatalf("%v %v hits %d", r, err, gitHits)
	}
}

// A public repository never gets the sign-in, even when the app has one.
func TestAPublicCloneNeverSendsTheSignIn(t *testing.T) {
	gh := newFakeGitHub(t, false)
	s, _, c := servingProject(t, withGitHub(gh))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := keepSetup(filepath.Join(s.env.Dir, "state", "box-1"), &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api", false)}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api", GithubToken: testToken}); err != nil || !r.GetApplied() {
		t.Fatalf("%v %v", r, err)
	}
	for _, a := range gh.auths {
		if a != "" {
			t.Fatalf("the sign-in went to GitHub for a public repository: %q", a)
		}
	}
}
