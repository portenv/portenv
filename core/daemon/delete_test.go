// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/local"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Delete Workspace (ADR 0029). portenvd deletes a box in a fixed order —
// the checks, the instance, the home, the saves on this Mac, then (once the
// app has deleted the key) the record, last — each step safe to repeat, and
// a failure resumable from the step it stopped at.

const (
	delID    = "box-0123456789ab"
	delOther = "box-ffffffffffff"
)

// fakeDeleter stands in for this Mac's engine and the lease check, and runs
// the saves and record steps on the real Portenv folder. It records each
// call, in order, and fails a step as many times as failures says.
type fakeDeleter struct {
	env      *local.Env
	mu       sync.Mutex
	calls    []string
	running  bool
	holder   string
	leaseErr error
	failures map[string]int
}

func (f *fakeDeleter) record(call, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call+" "+id)
	if f.failures[call] > 0 {
		f.failures[call]--
		return errors.New("the engine said no (" + call + ")")
	}
	return nil
}

func (f *fakeDeleter) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.calls)
	f.calls = nil
	return out
}

func (f *fakeDeleter) Running(_ context.Context, c local.BoxConfig) (bool, error) {
	return f.running, f.record("running", c.ID)
}

func (f *fakeDeleter) LeaseHolder(_ context.Context, c local.BoxConfig) (string, error) {
	if err := f.record("lease", c.ID); err != nil {
		return "", err
	}
	return f.holder, f.leaseErr
}

func (f *fakeDeleter) RemoveInstance(_ context.Context, c local.BoxConfig) error {
	return f.record("instance", c.ID)
}

func (f *fakeDeleter) RemoveHome(_ context.Context, c local.BoxConfig) error {
	return f.record("home", c.ID)
}

func (f *fakeDeleter) RemoveSaves(_ context.Context, c local.BoxConfig) error {
	if err := f.record("saves", c.ID); err != nil {
		return err
	}
	return f.env.RemoveLocalRepository(c)
}

func (f *fakeDeleter) RemoveRecord(_ context.Context, c local.BoxConfig) error {
	if err := f.record("record", c.ID); err != nil {
		return err
	}
	if err := f.env.RemoveBoxState(c); err != nil {
		return err
	}
	return f.env.RemoveConfig(c)
}

// asApp is a call from the person at this Mac (the app or the CLI).
func asApp() context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: callerInfo{who: ownerCaller()}})
}

// deleteServer has acme-api (delID) with storage on this Mac: three saves,
// its state and its key held in memory; and another workspace, other-api,
// with its own.
func deleteServer(t *testing.T) (*Server, *fakeDeleter) {
	t.Helper()
	e := &local.Env{Dir: t.TempDir(), Machine: "mac-1"}
	s := New(e, slog.New(slog.DiscardHandler))
	for _, c := range []local.BoxConfig{
		{ID: delID, Name: "acme-api", Image: "img", Recovery: true},
		{ID: delOther, Name: "other-api", Image: "img"},
	} {
		if err := e.PublishConfig(c); err != nil {
			t.Fatal(err)
		}
		repo, _ := e.LocalRepository(c)
		writeFile(t, filepath.Join(repo, "config"), "restic config")
		for _, snap := range []string{"aaa", "bbb", "ccc"} {
			writeFile(t, filepath.Join(repo, "snapshots", snap), "snapshot "+snap)
		}
		writeFile(t, filepath.Join(e.Dir, "state", c.ID, "state.json"), "{}")
		_ = e.Provided.Put(c.ID, []byte("key of "+c.ID))
	}
	f := &fakeDeleter{env: e, failures: map[string]int{}}
	s.deleter = f
	return s, f
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (s *Server) repoOf(t *testing.T, id, name string) string {
	t.Helper()
	repo, ok := s.env.LocalRepository(local.BoxConfig{ID: id, Name: name})
	if !ok {
		t.Fatal("no local repository")
	}
	return repo
}

func listed(t *testing.T, s *Server) map[string]*daemonv1.BoxStatus {
	t.Helper()
	r, err := s.ListBoxes(context.Background(), &daemonv1.ListBoxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*daemonv1.BoxStatus{}
	for _, b := range r.GetBoxes() {
		m[b.GetBox().GetName()] = b
	}
	return m
}

func deleteReq(name string) *daemonv1.DeleteBoxRequest {
	return &daemonv1.DeleteBoxRequest{Name: name, ConfirmName: name}
}

// TestDeleteRunsItsStepsInOrder: the checks (running, lease), the instance,
// the home, the saves; then the app is told which keys to delete, and only
// its call after deleting them removes the record, last. The workspace
// shows as deleting, and can't open, until then.
func TestDeleteRunsItsStepsInOrder(t *testing.T) {
	s, f := deleteServer(t)
	repo := s.repoOf(t, delID, "acme-api")

	r, err := s.DeleteBox(asApp(), deleteReq("acme-api"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.took(), []string{"running " + delID, "lease " + delID, "instance " + delID, "home " + delID, "saves " + delID}; !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	if r.GetFinished() || r.GetStep() != daemonv1.DeleteStep_DELETE_STEP_KEY || !slices.Equal(r.GetDeleteKeyIds(), []string{delID}) {
		t.Fatalf("after the saves: %v", r)
	}
	if exists(repo) {
		t.Fatal("the saves are still there")
	}
	b := listed(t, s)["acme-api"]
	if b == nil || !b.GetDeleting() || b.GetDeleteStep() != daemonv1.DeleteStep_DELETE_STEP_KEY {
		t.Fatalf("listed as %v", b)
	}
	if _, err := s.env.Open("acme-api"); !errors.Is(err, local.ErrDeleting) {
		t.Fatalf("a workspace being deleted opened: %v", err)
	}

	r, err = s.DeleteBox(asApp(), &daemonv1.DeleteBoxRequest{Name: "acme-api", ConfirmName: "acme-api", KeysDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.took(), []string{"record " + delID}; !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	if !r.GetFinished() || len(r.GetDeleteKeyIds()) != 0 {
		t.Fatalf("finished: %v", r)
	}
	if _, ok := listed(t, s)["acme-api"]; ok {
		t.Fatal("still listed")
	}
	if exists(filepath.Join(s.env.Dir, "state", delID)) {
		t.Fatal("its state is still there")
	}
	if _, err := s.env.Provided.Get(delID); !errors.Is(err, keys.ErrNotFound) {
		t.Fatalf("its key is still held in memory: %v", err)
	}
	// Deleting it again: there's nothing to delete, and it says so.
	if _, err := s.DeleteBox(asApp(), deleteReq("acme-api")); status.Code(err) != codes.NotFound {
		t.Fatalf("again: %v", err)
	}
}

// TestDeleteKeysDeletedNeverSkipsASave: keys_deleted counts only once the
// saves are gone (the step is KEY): a first call with it still stops there,
// naming the keys, and nothing goes before the saves.
func TestDeleteKeysDeletedNeverSkipsASave(t *testing.T) {
	s, f := deleteServer(t)
	f.failures["saves"] = 1
	_, err := s.DeleteBox(asApp(), &daemonv1.DeleteBoxRequest{Name: "acme-api", ConfirmName: "acme-api", KeysDeleted: true})
	if err == nil {
		t.Fatal("the failed saves step didn't stop it")
	}
	if !exists(s.repoOf(t, delID, "acme-api")) || listed(t, s)["acme-api"] == nil {
		t.Fatal("the record or the saves went before the saves step finished")
	}
	r, err := s.DeleteBox(asApp(), &daemonv1.DeleteBoxRequest{Name: "acme-api", ConfirmName: "acme-api", KeysDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetFinished() || r.GetStep() != daemonv1.DeleteStep_DELETE_STEP_KEY {
		t.Fatalf("finished without the app deleting the key after the saves: %v", r)
	}
}

// TestDeleteResumesFromTheStepThatFailed: a failure at each step says where
// it stopped, leaves everything after it in place, shows in the list, and a
// call again continues from that step (the steps before it aren't run
// again) to the end.
func TestDeleteResumesFromTheStepThatFailed(t *testing.T) {
	steps := []struct {
		call string
		step daemonv1.DeleteStep
		// what a retry runs, from the step that failed
		rest []string
	}{
		{"instance", daemonv1.DeleteStep_DELETE_STEP_INSTANCE, []string{"instance", "home", "saves"}},
		{"home", daemonv1.DeleteStep_DELETE_STEP_HOME, []string{"home", "saves"}},
		{"saves", daemonv1.DeleteStep_DELETE_STEP_SAVES, []string{"saves"}},
	}
	for _, st := range steps {
		t.Run(st.call, func(t *testing.T) {
			s, f := deleteServer(t)
			repo := s.repoOf(t, delID, "acme-api")
			f.failures[st.call] = 1
			_, err := s.DeleteBox(asApp(), deleteReq("acme-api"))
			if err == nil || !strings.Contains(status.Convert(err).Message(), "stopped") || !strings.Contains(status.Convert(err).Message(), "acme-api") {
				t.Fatalf("failure at %s: %v", st.call, err)
			}
			f.took()
			if !exists(repo) {
				t.Fatal("the saves went although an earlier step failed")
			}
			b := listed(t, s)["acme-api"]
			if b == nil || !b.GetDeleting() || b.GetDeleteStep() != st.step {
				t.Fatalf("listed as %v, want deleting at %v", b, st.step)
			}
			r, err := s.DeleteBox(asApp(), deleteReq("acme-api"))
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, c := range st.rest {
				want = append(want, c+" "+delID)
			}
			if got := f.took(); !slices.Equal(got, want) {
				t.Fatalf("retry ran %v, want %v", got, want)
			}
			if r.GetStep() != daemonv1.DeleteStep_DELETE_STEP_KEY || exists(repo) {
				t.Fatalf("after the retry: %v (saves there: %v)", r, exists(repo))
			}
		})
	}
	t.Run("record", func(t *testing.T) {
		s, f := deleteServer(t)
		if _, err := s.DeleteBox(asApp(), deleteReq("acme-api")); err != nil {
			t.Fatal(err)
		}
		f.took()
		f.failures["record"] = 1
		done := &daemonv1.DeleteBoxRequest{Name: "acme-api", ConfirmName: "acme-api", KeysDeleted: true}
		if _, err := s.DeleteBox(asApp(), done); err == nil {
			t.Fatal("the failed record step didn't stop it")
		}
		b := listed(t, s)["acme-api"]
		if b == nil || b.GetDeleteStep() != daemonv1.DeleteStep_DELETE_STEP_RECORD {
			t.Fatalf("listed as %v", b)
		}
		f.took()
		// Its key is already gone: the retry needs no other keys_deleted.
		r, err := s.DeleteBox(asApp(), deleteReq("acme-api"))
		if err != nil {
			t.Fatal(err)
		}
		if got := f.took(); !slices.Equal(got, []string{"record " + delID}) || !r.GetFinished() {
			t.Fatalf("retry ran %v: %v", got, r)
		}
	})
}

// TestDeleteRefusedWhenAnotherMachineHoldsTheLease: nothing is removed and
// nothing is marked; the refusal names the machine. A lease that can't be
// checked is a refusal too.
func TestDeleteRefusedWhenAnotherMachineHoldsTheLease(t *testing.T) {
	s, f := deleteServer(t)
	f.holder = "studio-mac-7f3a2c"
	_, err := s.DeleteBox(asApp(), deleteReq("acme-api"))
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "studio-mac-7f3a2c") {
		t.Fatalf("under another machine's lease: %v", err)
	}
	if got := f.took(); !slices.Equal(got, []string{"running " + delID, "lease " + delID}) {
		t.Fatalf("ran %v", got)
	}
	if b := listed(t, s)["acme-api"]; b == nil || b.GetDeleting() {
		t.Fatalf("listed as %v", b)
	}
	// This Mac's own lease (left by Quit Anyway's background save that has
	// since finished, or a crash) is no refusal.
	f.holder = "mac-1"
	f.failures["instance"] = 1 // stop right after the checks
	if _, err := s.DeleteBox(asApp(), deleteReq("acme-api")); err == nil || status.Code(err) == codes.FailedPrecondition && strings.Contains(err.Error(), "open on") {
		t.Fatalf("this Mac's own lease: %v", err)
	}

	s, f = deleteServer(t)
	f.leaseErr = errors.New("storage unreachable")
	if _, err := s.DeleteBox(asApp(), deleteReq("acme-api")); status.Code(err) != codes.Unavailable {
		t.Fatalf("lease unknown: %v", err)
	}
	if got := f.took(); slices.ContainsFunc(got, func(c string) bool { return !strings.HasPrefix(c, "running") && !strings.HasPrefix(c, "lease") }) {
		t.Fatalf("ran %v", got)
	}
}

// TestDeleteRefusedWhileOpenSavingOrElsewhere: a workspace open here (being
// saved, saved in the background after Quit Anyway, or just open), moving,
// on a server, or running without this portenvd is never deleted; each
// refusal says why and nothing is touched.
func TestDeleteRefusedWhileOpenSavingOrElsewhere(t *testing.T) {
	cases := map[string]struct {
		set  func(s *Server, f *fakeDeleter)
		says string
	}{
		"saving": {func(s *Server, _ *fakeDeleter) {
			ob := &openBox{}
			ob.saving.Store(true)
			s.open["acme-api"] = ob
		}, "being saved"},
		"saving in the background": {func(s *Server, _ *fakeDeleter) {
			ob := &openBox{}
			ob.background.Store(true)
			s.open["acme-api"] = ob
		}, "still saving"},
		"open":   {func(s *Server, _ *fakeDeleter) { s.open["acme-api"] = &openBox{} }, "Close it first"},
		"moving": {func(s *Server, _ *fakeDeleter) { s.moves["acme-api"] = "dev@test-server" }, "moving"},
		"on a server": {func(s *Server, _ *fakeDeleter) {
			s.remote["acme-api"] = &remoteBox{host: "dev@test-server"}
		}, "test-server"},
		"running": {func(_ *Server, f *fakeDeleter) { f.running = true }, "still running"},
		// A push or fetch with GitHub in flight (R-0391), from the
		// workspace's own git: never deleted under it.
		"pushing or fetching": {func(s *Server, _ *fakeDeleter) {
			done := s.gitCall("acme-api")
			t.Cleanup(done)
		}, "pushing to or fetching from GitHub"},
		// R-0358: an agent of its team running (in any running state).
		"an agent running": {func(s *Server, _ *fakeDeleter) {
			b := s.teamEng().box("acme-api")
			b.mu.Lock()
			b.states["builder"] = typesv1.AgentState_AGENT_STATE_IDLE
			b.mu.Unlock()
		}, "Stop its agents first"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, f := deleteServer(t)
			c.set(s, f)
			_, err := s.DeleteBox(asApp(), deleteReq("acme-api"))
			if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), c.says) {
				t.Fatalf("got %v, want a refusal saying %q", err, c.says)
			}
			if got := f.took(); slices.ContainsFunc(got, func(c string) bool { return !strings.HasPrefix(c, "running") }) {
				t.Fatalf("ran %v", got)
			}
			if !exists(s.repoOf(t, delID, "acme-api")) || listed(t, s)["acme-api"].GetDeleting() {
				t.Fatal("something was removed or marked")
			}
		})
	}
}

// TestDeleteNeedsTheExactName: the typed name must be the workspace's name,
// exactly; anything else is refused before any step.
func TestDeleteNeedsTheExactName(t *testing.T) {
	s, f := deleteServer(t)
	for _, typed := range []string{"", "Acme-api", "acme-api ", " acme-api", "acme", "other-api"} {
		_, err := s.DeleteBox(asApp(), &daemonv1.DeleteBoxRequest{Name: "acme-api", ConfirmName: typed})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("typed %q: %v", typed, err)
		}
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("ran %v", got)
	}
}

// TestDeleteSavesElsewhereNeedTheSecondConfirmation: a workspace whose saves
// are on a server is refused without the second confirmation. With it, the
// saves are left where they are and so is the key (no workspace ends up
// with saves and no key): no saves step, no key to delete, and the record
// goes.
func TestDeleteSavesElsewhereNeedTheSecondConfirmation(t *testing.T) {
	s, f := deleteServer(t)
	c := local.BoxConfig{ID: "box-00000000000a", Name: "far-api", Image: "img",
		Storage: "sftp:dev@test-server:/srv/portenv", StorageHostKey: "ssh-ed25519 AAAA"}
	if err := s.env.PublishConfig(c); err != nil {
		t.Fatal(err)
	}
	_, err := s.DeleteBox(asApp(), deleteReq("far-api"))
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "test-server") {
		t.Fatalf("without the second confirmation: %v", err)
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("ran %v", got)
	}
	r, err := s.DeleteBox(asApp(), &daemonv1.DeleteBoxRequest{Name: "far-api", ConfirmName: "far-api", ConfirmSavesElsewhere: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"running " + c.ID, "lease " + c.ID, "instance " + c.ID, "home " + c.ID, "record " + c.ID}
	if got := f.took(); !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	if !r.GetFinished() || len(r.GetDeleteKeyIds()) != 0 || r.GetSavesLeftAt() != "test-server" {
		t.Fatalf("got %v", r)
	}
}

// TestDeleteNeverTouchesAnotherWorkspace: deleting acme-api leaves
// other-api's record, saves, state and key exactly as they were, and every
// step names only acme-api's ID.
func TestDeleteNeverTouchesAnotherWorkspace(t *testing.T) {
	s, f := deleteServer(t)
	other := s.repoOf(t, delOther, "other-api")
	if _, err := s.DeleteBox(asApp(), deleteReq("acme-api")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteBox(asApp(), &daemonv1.DeleteBoxRequest{Name: "acme-api", ConfirmName: "acme-api", KeysDeleted: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.took() {
		if !strings.HasSuffix(c, " "+delID) {
			t.Fatalf("a step for another workspace: %s", c)
		}
	}
	if b := listed(t, s)["other-api"]; b == nil || b.GetDeleting() {
		t.Fatalf("other-api listed as %v", b)
	}
	if n, _, err := local.CountSaves(other); err != nil || n != 3 {
		t.Fatalf("other-api's saves: %d, %v", n, err)
	}
	if !exists(filepath.Join(s.env.Dir, "state", delOther, "state.json")) {
		t.Fatal("other-api's state is gone")
	}
	if _, err := s.env.Provided.Get(delOther); err != nil {
		t.Fatalf("other-api's key: %v", err)
	}
}

// TestDeleteIsNeverAnAgents: only the person at this Mac deletes a
// workspace; a caller with an agent's identity (ADR 0016), or none, is
// refused before anything is read.
func TestDeleteIsNeverAnAgents(t *testing.T) {
	s, f := deleteServer(t)
	agentCtx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: callerInfo{who: caller{kind: callerAgent, id: "grok", name: "Grok Bot", box: "acme-api"}}})
	if _, err := s.DeleteBox(agentCtx, deleteReq("acme-api")); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("an agent: %v", err)
	}
	if _, err := s.DeleteBox(context.Background(), deleteReq("acme-api")); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no caller: %v", err)
	}
	if _, err := s.GetDeletePlan(agentCtx, &daemonv1.GetDeletePlanRequest{Name: "acme-api"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("an agent's plan: %v", err)
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("ran %v", got)
	}
}

// TestDeletePlanSaysWhatGoes: the sheet's facts, read without a key: the
// number of saves and their size here, saves elsewhere with their place,
// open here, a refusal, and a deletion that stopped.
func TestDeletePlanSaysWhatGoes(t *testing.T) {
	s, f := deleteServer(t)
	p, err := s.GetDeletePlan(asApp(), &daemonv1.GetDeletePlanRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if p.GetBoxId() != delID || p.GetSaveCount() != 3 || p.GetSavesBytes() <= 0 || p.GetSavesElsewhere() ||
		p.GetOpenHere() || p.GetRefusal() != "" || !p.GetCarriesRecoveryKey() || p.GetStoppedAt() != daemonv1.DeleteStep_DELETE_STEP_UNSPECIFIED {
		t.Fatalf("plan %v", p)
	}
	s.open["acme-api"] = &openBox{}
	if p, _ = s.GetDeletePlan(asApp(), &daemonv1.GetDeletePlanRequest{Name: "acme-api"}); !p.GetOpenHere() {
		t.Fatalf("open: %v", p)
	}
	delete(s.open, "acme-api")
	s.moves["acme-api"] = "dev@test-server"
	if p, _ = s.GetDeletePlan(asApp(), &daemonv1.GetDeletePlanRequest{Name: "acme-api"}); !strings.Contains(p.GetRefusal(), "moving") {
		t.Fatalf("moving: %v", p)
	}
	delete(s.moves, "acme-api")
	f.failures["home"] = 1
	_, _ = s.DeleteBox(asApp(), deleteReq("acme-api"))
	if p, _ = s.GetDeletePlan(asApp(), &daemonv1.GetDeletePlanRequest{Name: "acme-api"}); p.GetStoppedAt() != daemonv1.DeleteStep_DELETE_STEP_HOME {
		t.Fatalf("stopped: %v", p)
	}
	c := local.BoxConfig{ID: "box-00000000000a", Name: "far-api", Image: "img",
		Storage: "sftp:dev@test-server:/srv/portenv", StorageHostKey: "ssh-ed25519 AAAA"}
	if err := s.env.PublishConfig(c); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.GetDeletePlan(asApp(), &daemonv1.GetDeletePlanRequest{Name: "far-api"}); !p.GetSavesElsewhere() || p.GetSavesPlace() != "test-server" {
		t.Fatalf("elsewhere: %v", p)
	}
}

// TestOpenRefusesAWorkspaceBeingDeleted: OpenBox never starts a workspace
// whose deletion stopped part-way (its home or saves may be gone), and says
// to finish deleting it.
func TestOpenRefusesAWorkspaceBeingDeleted(t *testing.T) {
	s, f := deleteServer(t)
	f.failures["saves"] = 1
	_, _ = s.DeleteBox(asApp(), deleteReq("acme-api"))
	_, err := s.OpenBox(context.Background(), &daemonv1.OpenBoxRequest{Name: "acme-api"})
	if err == nil || !strings.Contains(err.Error(), "Delete Workspace") {
		t.Fatalf("open: %v", err)
	}
}

// TestNoAgentCanDeleteAWorkspace (R-0358): deleting a workspace is in no
// agent's surface. The workspace helper's API (what agents' calls reach,
// through the team's stream) has no such method or team call, no decision
// an agent can ask for deletes a workspace, and no grant gives an agent a
// delete command.
func TestNoAgentCanDeleteAWorkspace(t *testing.T) {
	deletes := func(name string) bool {
		n := strings.ToLower(name)
		return strings.Contains(n, "delete") && (strings.Contains(n, "box") || strings.Contains(n, "workspace")) ||
			strings.Contains(n, "remove_box") || strings.Contains(n, "removebox")
	}
	svcs := agentv1.File_portenv_agent_v1_agent_proto.Services()
	for i := range svcs.Len() {
		ms := svcs.Get(i).Methods()
		for j := range ms.Len() {
			if deletes(string(ms.Get(j).Name())) {
				t.Errorf("the workspace helper's API has %s", ms.Get(j).FullName())
			}
		}
	}
	ops := (&agentv1.TeamCallsResponse{}).ProtoReflect().Descriptor().Oneofs().ByName("op").Fields()
	for i := range ops.Len() {
		if n := string(ops.Get(i).Name()); deletes(n) || strings.Contains(n, "delete") {
			t.Errorf("an agent's team call deletes: %s", n)
		}
	}
	for _, n := range typesv1.DecisionKind_name {
		if deletes(n) {
			t.Errorf("a decision an agent can ask for deletes a workspace: %s", n)
		}
	}
	allow := typesv1.GrantMode_GRANT_MODE_ALLOW
	for _, c := range commandsFor(&typesv1.AgentGrant{Panes: allow, AskOwner: true, TeamStatus: true}) {
		if strings.Contains(c, "delete") {
			t.Errorf("a grant gives an agent %q", c)
		}
	}
}
