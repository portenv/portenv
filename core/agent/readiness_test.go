// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// shortSocket returns a socket path short enough for every OS's limit.
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "run", "agent.sock")
}

func TestServeReportsReadiness(t *testing.T) {
	socket := shortSocket(t)
	r := NewReadiness()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, socket, r) }()

	want := func(state agentv1.ReadinessState, detail string) {
		t.Helper()
		var gotState agentv1.ReadinessState
		var gotDetail string
		var err error
		for range 50 {
			gotState, gotDetail, err = CheckReadiness(context.Background(), socket)
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		if gotState != state || gotDetail != detail {
			t.Fatalf("got %v %q, want %v %q", gotState, gotDetail, state, detail)
		}
	}

	want(agentv1.ReadinessState_READINESS_STATE_STARTING, "starting")
	r.Step("installing 2 packages")
	want(agentv1.ReadinessState_READINESS_STATE_STARTING, "installing 2 packages")
	r.Ready()
	want(agentv1.ReadinessState_READINESS_STATE_READY, "ready")
	r.Fail(errors.New("home not found"))
	want(agentv1.ReadinessState_READINESS_STATE_FAILED, "home not found")

	for _, p := range []string{socket, filepath.Dir(socket)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s has mode %v, want no group or other access", p, perm)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCheckReadinessWithoutAgent(t *testing.T) {
	if _, _, err := CheckReadiness(context.Background(), shortSocket(t)); err == nil {
		t.Fatal("want an error when no agent is listening")
	}
}
