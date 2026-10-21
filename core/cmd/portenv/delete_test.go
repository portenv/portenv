// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// fakeDeleting is portenvd's two delete calls, recording each DeleteBox.
type fakeDeleting struct {
	plan  *daemonv1.GetDeletePlanResponse
	resp  *daemonv1.DeleteBoxResponse
	calls []*daemonv1.DeleteBoxRequest
}

func (f *fakeDeleting) GetDeletePlan(context.Context, *daemonv1.GetDeletePlanRequest, ...grpc.CallOption) (*daemonv1.GetDeletePlanResponse, error) {
	return f.plan, nil
}

func (f *fakeDeleting) DeleteBox(_ context.Context, r *daemonv1.DeleteBoxRequest, _ ...grpc.CallOption) (*daemonv1.DeleteBoxResponse, error) {
	f.calls = append(f.calls, r)
	return f.resp, nil
}

func keyStep() *daemonv1.DeleteBoxResponse {
	return &daemonv1.DeleteBoxResponse{Step: daemonv1.DeleteStep_DELETE_STEP_KEY, DeleteKeyIds: []string{"box-0123456789ab"}}
}

// TestCLIDeleteNeedsItsFlagOrTheTypedName (ADR 0029): without
// --yes-delete-everything and with no terminal to type the name at, nothing
// is asked of portenvd; at a terminal, the typed name must be exact.
func TestCLIDeleteNeedsItsFlagOrTheTypedName(t *testing.T) {
	f := &fakeDeleting{plan: &daemonv1.GetDeletePlanResponse{SaveCount: 3}, resp: keyStep()}
	var out bytes.Buffer
	err := deleteWorkspace(context.Background(), f, "acme-api", nil, strings.NewReader(""), &out, false)
	if err == nil || !strings.Contains(err.Error(), "--yes-delete-everything") || len(f.calls) != 0 {
		t.Fatalf("no flag, no terminal: %v, %d calls", err, len(f.calls))
	}
	for _, typed := range []string{"acme\n", "Acme-api\n", "\n"} {
		err := deleteWorkspace(context.Background(), f, "acme-api", nil, strings.NewReader(typed), &out, true)
		if err == nil || len(f.calls) != 0 {
			t.Fatalf("typed %q: %v, %d calls", typed, err, len(f.calls))
		}
	}
	_ = deleteWorkspace(context.Background(), f, "acme-api", nil, strings.NewReader("acme-api\n"), &out, true)
	if len(f.calls) != 1 || f.calls[0].GetConfirmName() != "acme-api" {
		t.Fatalf("typed the name: %v", f.calls)
	}
	f.calls = nil
	_ = deleteWorkspace(context.Background(), f, "acme-api", []string{"--yes-delete-everything"}, strings.NewReader(""), &out, false)
	if len(f.calls) != 1 || f.calls[0].GetConfirmName() != "acme-api" || f.calls[0].GetConfirmSavesElsewhere() {
		t.Fatalf("with the flag: %v", f.calls)
	}
}

// TestCLIDeleteNeverDeletesTheKey: the CLI never touches the Keychain and
// never says it did: it never sends keys_deleted, and when the key remains
// it says to finish in the app, and fails.
func TestCLIDeleteNeverDeletesTheKey(t *testing.T) {
	f := &fakeDeleting{plan: &daemonv1.GetDeletePlanResponse{}, resp: keyStep()}
	var out bytes.Buffer
	err := deleteWorkspace(context.Background(), f, "acme-api", []string{"--yes-delete-everything"}, strings.NewReader(""), &out, false)
	if err == nil || !strings.Contains(err.Error(), "Delete Workspace") || !strings.Contains(err.Error(), "Keychain") {
		t.Fatalf("key left: %v", err)
	}
	for _, c := range f.calls {
		if c.GetKeysDeleted() {
			t.Fatal("the CLI said the keys were deleted")
		}
	}
}

// TestCLIDeleteSavesElsewhereNeedTheirOwnFlag: saves on a server need
// --saves-elsewhere-stay as well; with it, the request carries the second
// confirmation and the CLI says where the saves stay.
func TestCLIDeleteSavesElsewhereNeedTheirOwnFlag(t *testing.T) {
	f := &fakeDeleting{plan: &daemonv1.GetDeletePlanResponse{SavesElsewhere: true, SavesPlace: "test-server"},
		resp: &daemonv1.DeleteBoxResponse{Finished: true, SavesLeftAt: "test-server"}}
	var out bytes.Buffer
	err := deleteWorkspace(context.Background(), f, "far-api", []string{"--yes-delete-everything"}, strings.NewReader(""), &out, false)
	if err == nil || !strings.Contains(err.Error(), "--saves-elsewhere-stay") || len(f.calls) != 0 {
		t.Fatalf("without the second flag: %v", err)
	}
	if err := deleteWorkspace(context.Background(), f, "far-api", []string{"--yes-delete-everything", "--saves-elsewhere-stay"}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !f.calls[0].GetConfirmSavesElsewhere() || !strings.Contains(out.String(), "test-server") {
		t.Fatalf("calls %v, said %q", f.calls, out.String())
	}
}

// TestCLIDeleteSaysPortenvdsRefusal: a refusal in the plan (open, agents
// running) is said as it is, and nothing is deleted.
func TestCLIDeleteSaysPortenvdsRefusal(t *testing.T) {
	f := &fakeDeleting{plan: &daemonv1.GetDeletePlanResponse{Refusal: "acme-api has agents running. Stop its agents first (stop its team), then delete it."}}
	var out bytes.Buffer
	err := deleteWorkspace(context.Background(), f, "acme-api", []string{"--yes-delete-everything"}, strings.NewReader(""), &out, false)
	if err == nil || !strings.Contains(err.Error(), "agents running") || len(f.calls) != 0 {
		t.Fatalf("refusal: %v", err)
	}
}

// TestCLIDeleteIsRefusedInsideAWorkspace (R-0358): the CLI's delete is the
// owner's, on their Mac; inside a workspace it never reaches portenvd.
func TestCLIDeleteIsRefusedInsideAWorkspace(t *testing.T) {
	home := insideBox(t)
	code, stderr := runCaught(t, "app", "delete", "acme-api", "--yes-delete-everything")
	if code != 2 || stderr != "This is the owner's command; run it on your Mac.\n" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	nothingIn(t, home, "app delete")
}
