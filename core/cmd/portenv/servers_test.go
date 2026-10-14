// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/local"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb2N0iUmGyJ4e6dN0B0n0qsVqk3uG3xdy0cxH8kYgSx"

// TestTrustServerIsOwnerOnly (R-0183): pinning a key nobody looked at is for
// the owner's own scripts. Inside a workspace (the box's environment
// marker) it's refused, so a workspace's agent can't pin a server for the
// owner; nothing is written.
func TestTrustServerIsOwnerOnly(t *testing.T) {
	e := &local.Env{Dir: t.TempDir(), Machine: "mac-1"}
	t.Setenv(agent.BoxIDEnv, "box-0123456789ab")
	err := cmdApp(context.Background(), e, "trust-server", []string{"me@test-server", testKey})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("inside a workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "servers.json")); err == nil {
		t.Fatal("servers.json was written")
	}
	if err := cmdApp(context.Background(), e, "remove-server", []string{"test-server"}); err == nil {
		t.Fatal("remove-server inside a workspace")
	}
	t.Setenv(agent.BoxIDEnv, "")
	if err := cmdApp(context.Background(), e, "trust-server", []string{"me@test-server", testKey}); err != nil {
		t.Fatalf("the owner's script: %v", err)
	}
	// The changed-key line's fallback works by the name the server shows by.
	if err := cmdApp(context.Background(), e, "remove-server", []string{"test-server"}); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.ServerTargets(); len(ts) != 0 {
		t.Fatalf("left %v", ts)
	}
}
