// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// Fetch through portenvd (R-0392): the owner's fetch, read from GitHub on
// the Mac with the sign-in (or none, for a public repository), handed in
// as a bundle of only what the workspace lacks.

func ownerFetch(wants []*agentv1.RemoteRef, haves ...string) *agentv1.TeamCallsResponse {
	return &agentv1.TeamCallsResponse{CallId: "f", Owner: true, Op: &agentv1.TeamCallsResponse_Fetch{Fetch: &agentv1.FetchRefs{Wants: wants, Haves: haves}}}
}

func TestAPrivateFetchWithTheSignIn(t *testing.T) {
	s, fake, r, logs := pushServer(t)
	var seen []*daemonv1.PushQuestion
	defer app(t, s, false, false, &seen)()
	old := r.ref("main")
	other := filepath.Join(t.TempDir(), "other")
	r.run("", "clone", "-q", r.bare, other)
	n := r.commit(other, "two")
	r.run(other, "push", "-q", "origin", "main")

	list := s.handlePush(context.Background(), "acme-api", &agentv1.TeamCallsResponse{Owner: true, Op: &agentv1.TeamCallsResponse_PushList{PushList: &agentv1.PushList{Fetch: true}}})
	if !list.GetOk() || list.GetPushList().GetHead() != "refs/heads/main" || list.GetPushList().GetRefs()[0].GetSha() != n {
		t.Fatalf("list: %v", list)
	}
	reply := s.handlePush(context.Background(), "acme-api", ownerFetch([]*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: n}}, old))
	if !reply.GetOk() || reply.GetFetch().GetBundleId() == "" || len(fake.fetchBundles) != 1 || int64(len(fake.fetchBundles[0])) != reply.GetFetch().GetBundleBytes() {
		t.Fatalf("fetch: %v", reply)
	}
	for _, q := range seen {
		if q.GetNeedsYes() || !q.GetFetch() {
			t.Fatalf("a fetch asked for the owner's yes: %v", q)
		}
	}
	// The bundle unbundles in a clone that has the old commit.
	f := filepath.Join(t.TempDir(), "f.bundle")
	if err := os.WriteFile(f, fake.fetchBundles[0], 0o600); err != nil {
		t.Fatal(err)
	}
	r.run(r.local, "bundle", "verify", "-q", f)
	r.run(r.local, "bundle", "unbundle", f)
	r.run(r.local, "cat-file", "-e", n)
	scanFor(t, []byte(testToken), s.env.Dir, fake.root, os.TempDir())
	for _, b := range fake.fetchBundles {
		if strings.Contains(string(b), testToken) {
			t.Fatal("the sign-in is in the bundle")
		}
	}
	if strings.Contains(logs.String(), testToken) {
		t.Fatal("the sign-in is in portenvd's log")
	}
	if ents, _ := os.ReadDir(filepath.Join(s.env.Dir, "fetch")); len(ents) != 0 {
		t.Fatalf("a bundle was left behind: %v", ents)
	}
}

// Signed out: a private repository says to sign in; nothing is handed in.
func TestAPrivateFetchSignedOut(t *testing.T) {
	s, fake, r, _ := pushServer(t)
	var seen []*daemonv1.PushQuestion
	defer app(t, s, true, true, &seen)()
	reply := s.handlePush(context.Background(), "acme-api", ownerFetch([]*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: r.ref("main")}}))
	if reply.GetOk() || !strings.Contains(reply.GetLine(), "Sign in with GitHub") || len(fake.fetchBundles) != 0 {
		t.Fatalf("%v", reply)
	}
}

// A public repository fetches without a sign-in.
func TestAPublicFetchNeedsNoSignIn(t *testing.T) {
	s, fake, _ := servingProject(t)
	gh := newFakeGitHub(t, false)
	useFakeGitHub(s, gh)
	if err := keepGitHubRepo(filepath.Join(s.env.Dir, "state", "box-1"), "https://github.com/acme/api"); err != nil {
		t.Fatal(err)
	}
	var seen []*daemonv1.PushQuestion
	defer app(t, s, true, true, &seen)()
	list := s.handlePush(context.Background(), "acme-api", &agentv1.TeamCallsResponse{Owner: true, Op: &agentv1.TeamCallsResponse_PushList{PushList: &agentv1.PushList{Fetch: true}}})
	if !list.GetOk() || len(list.GetPushList().GetRefs()) == 0 {
		t.Fatalf("%v", list)
	}
	ref := list.GetPushList().GetRefs()[0]
	reply := s.handlePush(context.Background(), "acme-api", ownerFetch([]*agentv1.RemoteRef{ref}))
	if !reply.GetOk() || len(fake.fetchBundles) != 1 {
		t.Fatalf("%v", reply)
	}
	for _, a := range gh.auths {
		if a != "" {
			t.Fatalf("a public fetch sent a sign-in: %q", a)
		}
	}
}

func TestAFetchRefusesWhatItDoesntTake(t *testing.T) {
	s, fake, r, _ := pushServer(t)
	var seen []*daemonv1.PushQuestion
	defer app(t, s, false, false, &seen)()
	for name, c := range map[string]*agentv1.TeamCallsResponse{
		"a pull ref":       ownerFetch([]*agentv1.RemoteRef{{Name: "refs/pull/1/head", Sha: r.ref("main")}}),
		"another object":   ownerFetch([]*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: strings.Repeat("1", 40)}}),
		"a bad have":       ownerFetch([]*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: r.ref("main")}}, "zz"),
		"an agent's fetch": {AgentId: "lead", Op: &agentv1.TeamCallsResponse_Fetch{Fetch: &agentv1.FetchRefs{}}},
	} {
		if reply := s.handlePush(context.Background(), "acme-api", c); reply.GetOk() {
			t.Errorf("%s: %v", name, reply)
		}
	}
	if len(fake.fetchBundles) != 0 {
		t.Fatal("a bundle was handed in")
	}
}

// The inspector's Stop Allowing Pushes (CC-0645): the next push asks again.
func TestStopAllowingPushes(t *testing.T) {
	s, _, _, _ := pushServer(t)
	state := filepath.Join(s.env.Dir, "state", "box-1")
	if err := writePrivateAtomic(filepath.Join(state, pushesAllowedFile), []byte("yes\n")); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetPushes(ownerCtx(), &daemonv1.GetPushesRequest{Name: "acme-api"})
	if err != nil || r.GetRepo() != "github.com/acme/api" || !r.GetAllowed() {
		t.Fatalf("%v %v", r, err)
	}
	if _, err := s.StopAllowingPushes(ownerCtx(), &daemonv1.StopAllowingPushesRequest{Name: "acme-api"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetPushes(ownerCtx(), &daemonv1.GetPushesRequest{Name: "acme-api"}); r.GetAllowed() {
		t.Fatal("still allowed")
	}
	if _, err := s.StopAllowingPushes(context.Background(), &daemonv1.StopAllowingPushesRequest{Name: "acme-api"}); err == nil {
		t.Fatal("anyone but the app could change it")
	}
}
