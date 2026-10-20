// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// New workspace's Project step (ADR 0025 §2): the project is brought in
// first, then the team is written into it; a failed clone writes and pins
// nothing, and keeps the setup for the next try.

func githubURL(u string) *daemonv1.ProjectSource {
	return &daemonv1.ProjectSource{Source: &daemonv1.ProjectSource_GithubUrl{GithubUrl: u}}
}

func emptyProject() *daemonv1.ProjectSource {
	return &daemonv1.ProjectSource{Source: &daemonv1.ProjectSource_Empty{Empty: true}}
}

func fakeGitHubAPI(t *testing.T, sizeKB string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the public size check sent a sign-in")
		}
		_, _ = w.Write([]byte(`{"size": ` + sizeKB + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestApplyClonesThenWritesTheTeam(t *testing.T) {
	s, fake, c := servingProject(t)
	s.githubAPI = fakeGitHubAPI(t, "2048")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	setup := &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api.git"), Team: approvedChoice(t, "starter")}
	if err := keepSetup(filepath.Join(s.env.Dir, "state", "box-1"), setup); err != nil {
		t.Fatal(err)
	}
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetApplied() || r.GetProjectError() != "" || len(r.GetPinned()) != 3 {
		t.Fatalf("%v %v", r, err)
	}
	if len(fake.clones) != 1 {
		t.Fatalf("clones %v", fake.clones)
	}
	cl := fake.clones[0]
	// work's copy, then builder's and reviewer's own clones (lead too:
	// starter's agents all have checkout own).
	if cl.GetUrl() != "https://github.com/acme/api" || cl.GetProjectDir() != "/home/work/acme-api" || cl.GetCopies() != 4 || cl.GetEstimateBytes() != 2048*1024 {
		t.Fatalf("clone %v", cl)
	}
	if b, _ := os.ReadFile(filepath.Join(fake.root, "acme-api", "README.md")); string(b) != "cloned" {
		t.Fatal("the team wasn't written into the cloned project")
	}
	if _, err := os.Stat(filepath.Join(fake.root, "acme-api", ".portenv", "agents", "lead.md")); err != nil {
		t.Fatal(err)
	}
}

// A failed clone: one plain line, nothing written or pinned, the setup
// kept; Try Again goes through.
func TestAFailedCloneKeepsTheSetup(t *testing.T) {
	s, fake, c := servingProject(t)
	s.githubAPI = fakeGitHubAPI(t, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state := filepath.Join(s.env.Dir, "state", "box-1")
	if err := keepSetup(state, &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/private"), Team: approvedChoice(t, "starter")}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		err       error
		signIn    bool
		wantStart string
	}{
		{status.Error(codes.Unauthenticated, "This repository needs a sign-in. Sign in with GitHub, then try again."), true, "This repository needs a sign-in."},
		{status.Error(codes.ResourceExhausted, "acme-api needs about 13.6 GB with its team."), false, "acme-api needs about"},
		{status.Error(codes.FailedPrecondition, "Portenv couldn’t clone github.com/acme/private: Could not resolve host"), false, "Portenv couldn’t clone"},
	} {
		fake.cloneErr = tc.err
		r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
		if err != nil || r.GetApplied() || r.GetNeedsSignIn() != tc.signIn || !strings.HasPrefix(r.GetProjectError(), tc.wantStart) || len(r.GetPinned()) != 0 {
			t.Fatalf("%v: %v %v", tc.err, r, err)
		}
		if fake.writes != 0 {
			t.Fatal("agent files were written after a failed clone")
		}
		if kept, _ := loadSetup(state); kept == nil || kept.GetTeam().GetTemplate() != "starter" {
			t.Fatal("the setup wasn't kept after a failed clone")
		}
	}
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
	s, fake, c := servingProject(t)
	s.githubAPI = fakeGitHubAPI(t, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := keepSetup(filepath.Join(s.env.Dir, "state", "box-1"), &daemonv1.WorkspaceSetup{Project: githubURL("https://github.com/acme/api")}); err != nil {
		t.Fatal(err)
	}
	r, err := c.ApplyTeamPlan(ctx, &daemonv1.ApplyTeamPlanRequest{Name: "acme-api"})
	if err != nil || !r.GetApplied() || len(fake.clones) != 1 || fake.clones[0].GetCopies() != 1 || fake.writes != 0 {
		t.Fatalf("%v %v", r, err)
	}
}

// CreateBox's check: only a GitHub https address, or Empty.
func TestCheckSetup(t *testing.T) {
	ok := []*daemonv1.WorkspaceSetup{
		{Project: emptyProject()},
		{Project: githubURL("https://github.com/acme/api")},
		{Project: githubURL("https://github.com/acme/api"), Team: approvedChoice(t, "starter")},
		{Team: approvedChoice(t, "starter")},
	}
	for _, s := range ok {
		if err := checkSetup(s, 4); err != nil {
			t.Errorf("%v: %v", s, err)
		}
	}
	for _, u := range []string{"http://github.com/a/b", "https://u:p@github.com/a/b", "file:///etc", "git@github.com:a/b"} {
		if err := checkSetup(&daemonv1.WorkspaceSetup{Project: githubURL(u)}, 4); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%q: %v", u, err)
		}
	}
	if err := checkSetup(&daemonv1.WorkspaceSetup{Project: &daemonv1.ProjectSource{}}, 4); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a project with no source: %v", err)
	}
}
