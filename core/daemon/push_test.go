// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// Push from the workspace (R-0391): the owner's calls from work's socket,
// answered here with the app's sign-in and, when it's needed, the owner's
// yes; pushed to the workspace's own repository with go-git. The sign-in
// is nowhere afterwards.

// pushRemote is GitHub for acme/api: a pushable git http-backend that
// takes only testToken, and a local clone to make bundles from.
type pushRemote struct {
	t          *testing.T
	git        string
	base, bare string
	local      string
	mu         sync.Mutex
	auths      []string
}

func newPushRemote(t *testing.T) *pushRemote {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	p := &pushRemote{t: t, git: git}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.bare = filepath.Join(root, "acme", "api")
	seed := filepath.Join(t.TempDir(), "seed")
	p.run("", "init", "-q", "-b", "main", seed)
	p.commit(seed, "one")
	p.run("", "clone", "-q", "--bare", seed, p.bare)
	p.run(p.bare, "config", "http.receivepack", "true")
	p.local = filepath.Join(t.TempDir(), "local")
	p.run("", "clone", "-q", p.bare, p.local)
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.auths = append(p.auths, r.Header.Get("Authorization"))
		p.mu.Unlock()
		if _, pw, ok := r.BasicAuth(); !ok || pw != testToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	p.base = srv.URL
	return p
}

func (p *pushRemote) run(dir string, args ...string) string {
	p.t.Helper()
	cmd := exec.Command(p.git, args...) // #nosec G204 -- the test's own setup
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+p.t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (p *pushRemote) commit(dir, msg string) string {
	p.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, msg), []byte(msg), 0o644); err != nil {
		p.t.Fatal(err)
	}
	p.run(dir, "add", ".")
	p.run(dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", msg)
	return p.run(dir, "rev-parse", "HEAD")
}

func (p *pushRemote) ref(name string) string {
	cmd := exec.Command(p.git, "rev-parse", "--verify", "--quiet", name) // #nosec G204 -- the test's own check
	cmd.Dir = p.bare
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func (p *pushRemote) bundle(n string, not string) []byte {
	p.t.Helper()
	p.run(p.local, "update-ref", "refs/portenv-push/0", n)
	f := filepath.Join(p.t.TempDir(), "b")
	p.run(p.local, "bundle", "create", "-q", f, "refs/portenv-push/0", "--not", not)
	p.run(p.local, "update-ref", "-d", "refs/portenv-push/0")
	b, _ := os.ReadFile(f)
	return b
}

// pushServer is portenvd with acme-api open, its helper the fake, its
// GitHub the remote, and the workspace's repository recorded.
func pushServer(t *testing.T) (*Server, *fakeProjectAgent, *pushRemote, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	r := newPushRemote(t)
	s, fake, _ := servingProject(t, func(s *Server) {
		s.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		s.githubGit = r.base
	})
	if err := keepGitHubRepo(filepath.Join(s.env.Dir, "state", "box-1"), "https://github.com/acme/api"); err != nil {
		t.Fatal(err)
	}
	return s, fake, r, &logs
}

// app answers each question as the window's app would: the sign-in, and
// yes or no when one is asked for.
func app(t *testing.T, s *Server, yes bool, signedOut bool, seen *[]*daemonv1.PushQuestion) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for ctx.Err() == nil {
			for _, q := range s.pendingPushes("acme-api") {
				*seen = append(*seen, q)
				allow := yes || !q.GetNeedsYes()
				req := &daemonv1.AnswerPushRequest{Name: "acme-api", Id: q.GetId(), Allow: allow && !signedOut, SignedOut: signedOut}
				if !signedOut {
					req.GithubToken = testToken
				}
				_, _ = s.AnswerPush(ownerCtx(), req)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return cancel
}

func ownerPush(updates ...*agentv1.PushUpdate) *agentv1.TeamCallsResponse {
	return &agentv1.TeamCallsResponse{CallId: "c1", Owner: true, Op: &agentv1.TeamCallsResponse_Push{Push: &agentv1.PushRefs{Updates: updates}}}
}

func TestListForPushWithTheSignIn(t *testing.T) {
	s, _, r, _ := pushServer(t)
	var seen []*daemonv1.PushQuestion
	defer app(t, s, false, false, &seen)()
	reply := s.handlePush(context.Background(), "acme-api", &agentv1.TeamCallsResponse{CallId: "c", Owner: true, Op: &agentv1.TeamCallsResponse_PushList{PushList: &agentv1.PushList{}}})
	if !reply.GetOk() || len(reply.GetPushList().GetRefs()) != 1 || reply.GetPushList().GetRefs()[0].GetSha() != r.ref("main") {
		t.Fatalf("%v", reply)
	}
	if len(seen) != 1 || !seen[0].GetList() || seen[0].GetNeedsYes() || seen[0].GetRepo() != "github.com/acme/api" {
		t.Fatalf("questions %v", seen)
	}
}

// The first push asks the owner; the next doesn't; a force asks every
// time, and a no leaves GitHub as it was.
func TestPushAsksWhenItShould(t *testing.T) {
	s, fake, r, logs := pushServer(t)
	var seen []*daemonv1.PushQuestion
	stop := app(t, s, true, false, &seen)
	old := r.ref("main")
	n := r.commit(r.local, "two")
	fake.pushBundles = map[string][]byte{"b1": r.bundle(n, old)}
	c := ownerPush(&agentv1.PushUpdate{Dst: "refs/heads/main", NewSha: n, OldSha: old})
	c.GetPush().BundleId = "b1"
	reply := s.handlePush(context.Background(), "acme-api", c)
	if !reply.GetOk() || !reply.GetPush().GetStatuses()[0].GetOk() || r.ref("main") != n {
		t.Fatalf("%v", reply)
	}
	if len(seen) != 1 || !seen[0].GetFirst() || !seen[0].GetNeedsYes() {
		t.Fatalf("the first push didn't ask: %v", seen)
	}
	// The next one doesn't ask.
	n2 := r.commit(r.local, "three")
	fake.pushBundles = map[string][]byte{"b2": r.bundle(n2, n)}
	c = ownerPush(&agentv1.PushUpdate{Dst: "refs/heads/main", NewSha: n2, OldSha: n})
	c.GetPush().BundleId = "b2"
	if reply := s.handlePush(context.Background(), "acme-api", c); !reply.GetOk() || r.ref("main") != n2 {
		t.Fatalf("%v", reply)
	}
	if len(seen) != 2 || seen[1].GetNeedsYes() || seen[1].GetFirst() {
		t.Fatalf("the second push asked: %v", seen[1:])
	}
	stop()
	// A force, declined.
	seen = nil
	defer app(t, s, false, false, &seen)()
	r.run(r.local, "reset", "-q", "--hard", old)
	n3 := r.commit(r.local, "rewritten")
	fake.pushBundles = map[string][]byte{"b3": r.bundle(n3, old)}
	c = ownerPush(&agentv1.PushUpdate{Dst: "refs/heads/main", NewSha: n3, OldSha: n2, Force: true})
	c.GetPush().BundleId = "b3"
	reply = s.handlePush(context.Background(), "acme-api", c)
	if reply.GetOk() || !strings.Contains(reply.GetLine(), "didn’t allow") || r.ref("main") != n2 {
		t.Fatalf("a declined force push: %v", reply)
	}
	if len(seen) != 1 || !seen[0].GetNeedsYes() || !seen[0].GetUpdates()[0].GetForce() {
		t.Fatalf("the force push didn't ask: %v", seen)
	}
	// A delete asks too.
	seen = nil
	c = ownerPush(&agentv1.PushUpdate{Dst: "refs/heads/main", OldSha: n2})
	if reply := s.handlePush(context.Background(), "acme-api", c); reply.GetOk() || len(seen) != 1 || !seen[0].GetUpdates()[0].GetDelete() || !seen[0].GetNeedsYes() {
		t.Fatalf("a delete: %v %v", reply, seen)
	}
	scanFor(t, []byte(testToken), s.env.Dir, fake.root, os.TempDir())
	if strings.Contains(logs.String(), testToken) {
		t.Fatal("the sign-in is in portenvd's log")
	}
	if ents, _ := os.ReadDir(filepath.Join(s.env.Dir, "push")); len(ents) != 0 {
		t.Fatalf("a bundle was left behind: %v", ents)
	}
}

func TestPushRefusals(t *testing.T) {
	s, fake, r, _ := pushServer(t)
	old := r.ref("main")
	n := r.commit(r.local, "two")
	bundle := r.bundle(n, old)
	push := func() *agentv1.TeamCallsResponse {
		fake.mu.Lock()
		fake.pushBundles = map[string][]byte{"b": bundle} // read once per push
		fake.mu.Unlock()
		c := ownerPush(&agentv1.PushUpdate{Dst: "refs/heads/main", NewSha: n, OldSha: old})
		c.GetPush().BundleId = "b"
		return c
	}
	// Signed out in Portenv.
	var seen []*daemonv1.PushQuestion
	stop := app(t, s, true, true, &seen)
	if reply := s.handlePush(context.Background(), "acme-api", push()); reply.GetOk() || reply.GetLine() != "Sign in with GitHub in Portenv, then push again." {
		t.Fatalf("signed out: %v", reply)
	}
	stop()
	// No window to ask.
	pushAnswerWait = 50 * time.Millisecond
	defer func() { pushAnswerWait = 80 * time.Second }()
	if reply := s.handlePush(context.Background(), "acme-api", push()); reply.GetOk() || reply.GetLine() != "Open acme-api’s window in Portenv to push: Portenv asks you there." {
		t.Fatalf("no window: %v", reply)
	}
	// A bad ref, refused before anyone is asked.
	seen = nil
	defer app(t, s, true, false, &seen)()
	c := ownerPush(&agentv1.PushUpdate{Dst: "refs/pull/1/head", NewSha: n})
	if reply := s.handlePush(context.Background(), "acme-api", c); reply.GetOk() || len(seen) != 0 {
		t.Fatalf("a pull ref: %v %v", reply, seen)
	}
	// Not the owner's call.
	if reply := s.handlePush(context.Background(), "acme-api", &agentv1.TeamCallsResponse{AgentId: "lead", Op: &agentv1.TeamCallsResponse_PushList{PushList: &agentv1.PushList{}}}); reply.GetOk() {
		t.Fatalf("an agent's push: %v", reply)
	}
	if r.ref("main") != old {
		t.Fatal("something was pushed")
	}
}

// A workspace with no GitHub repository of its own has nothing to push to.
func TestPushNeedsTheWorkspacesRepository(t *testing.T) {
	s, _, _, _ := pushServer(t)
	if err := os.Remove(filepath.Join(s.env.Dir, "state", "box-1", githubRepoFile)); err != nil {
		t.Fatal(err)
	}
	reply := s.handlePush(context.Background(), "acme-api", &agentv1.TeamCallsResponse{Owner: true, Op: &agentv1.TeamCallsResponse_PushList{PushList: &agentv1.PushList{}}})
	if reply.GetOk() || reply.GetLine() != "This workspace has no GitHub repository to push to." {
		t.Fatalf("%v", reply)
	}
}

// Only the owner's app answers.
func TestAnswerPushIsTheAppsAlone(t *testing.T) {
	s, _, _, _ := pushServer(t)
	_, err := s.AnswerPush(context.Background(), &daemonv1.AnswerPushRequest{Name: "acme-api", Id: "x", Allow: true})
	if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.Unauthenticated {
		t.Fatalf("%v", err)
	}
}

// The clone records the workspace's repository.
func TestTheCloneRecordsTheRepository(t *testing.T) {
	s, _, c := servingProject(t, withGitHub(newFakeGitHub(t, false)))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	state := filepath.Join(s.env.Dir, "state", "box-1")
	if err := keepSetup(state, &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api", false)}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"}); err != nil || !r.GetApplied() {
		t.Fatalf("%v %v", r, err)
	}
	if got, _ := loadGitHubRepo(state); got != "https://github.com/acme/api" {
		t.Fatalf("%q", got)
	}
}

// The window's stream: each question, then its going once answered.
func TestWatchPushesShowsEachQuestion(t *testing.T) {
	r := newPushRemote(t)
	s, _, c := servingProject(t, func(s *Server) { s.githubGit = r.base })
	if err := keepGitHubRepo(filepath.Join(s.env.Dir, "state", "box-1"), "https://github.com/acme/api"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := c.WatchPushes(ctx, &daemonv1.WatchPushesRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *agentv1.TeamCallsRequest, 1)
	go func() {
		done <- s.handlePush(ctx, "acme-api", &agentv1.TeamCallsResponse{Owner: true, Op: &agentv1.TeamCallsResponse_PushList{PushList: &agentv1.PushList{}}})
	}()
	m, err := stream.Recv()
	if err != nil || !m.GetQuestion().GetList() || m.GetQuestion().GetRepo() != "github.com/acme/api" {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := c.AnswerPush(ctx, &daemonv1.AnswerPushRequest{Name: "acme-api", Id: m.GetQuestion().GetId(), Allow: true, GithubToken: testToken}); err != nil {
		t.Fatal(err)
	}
	if reply := <-done; !reply.GetOk() {
		t.Fatalf("%v", reply)
	}
	m, err = stream.Recv()
	if err != nil || !m.GetQuestion().GetGone() {
		t.Fatalf("%v %v", m, err)
	}
}

// R-0393: a crafted bundle (the current tip as a prerequisite, an
// unrelated commit as the new value, no force) is treated as a force: the
// owner is asked, and a no leaves GitHub as it was.
func TestACraftedFastForwardAsksTheOwner(t *testing.T) {
	s, fake, r, _ := pushServer(t)
	if err := writePrivateAtomic(filepath.Join(s.env.Dir, "state", "box-1", pushesAllowedFile), []byte("yes\n")); err != nil {
		t.Fatal(err)
	}
	old := r.ref("main")
	r.run(r.local, "checkout", "-q", "--orphan", "unrelated")
	n := r.commit(r.local, "unrelated")
	r.run(r.local, "update-ref", "refs/portenv-push/0", n)
	f := filepath.Join(t.TempDir(), "b")
	r.run(r.local, "bundle", "create", "-q", f, "refs/portenv-push/0")
	b, _ := os.ReadFile(f)
	fake.pushBundles = map[string][]byte{"c": bytes.Replace(b, []byte("# v2 git bundle\n"), []byte("# v2 git bundle\n-"+old+" one\n"), 1)}
	var seen []*daemonv1.PushQuestion
	defer app(t, s, false, false, &seen)()
	c := ownerPush(&agentv1.PushUpdate{Dst: "refs/heads/main", NewSha: n, OldSha: old})
	c.GetPush().BundleId = "c"
	reply := s.handlePush(context.Background(), "acme-api", c)
	if reply.GetOk() || len(seen) != 1 || !seen[0].GetNeedsYes() || !seen[0].GetUpdates()[0].GetForce() {
		t.Fatalf("%v %v", reply, seen)
	}
	if r.ref("main") != old {
		t.Fatal("history was rewritten without the owner's yes")
	}
}
