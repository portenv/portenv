// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeServers stands in for ssh: each host answers `portenv machine-id`
// with its machine ID and `portenv app ping` as told: "ok", "fail", or
// "hang" (never answers).
func fakeServers(t *testing.T, hosts map[string][2]string) {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("#!/bin/sh\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nhost=$1; shift\ncase \"$host\" in\n")
	for host, v := range hosts {
		b.WriteString("  " + host + ") id=" + v[0] + "; ping=" + v[1] + " ;;\n")
	}
	b.WriteString("  *) echo \"ssh: connect to host $host: Connection refused\" >&2; exit 255 ;;\nesac\n")
	b.WriteString("case \"$*\" in\n")
	b.WriteString("  *machine-id*) echo \"$id\" ;;\n")
	b.WriteString("  *'app ping'*) case $ping in ok) echo ok ;; fail) echo 'portenv: portenvd is not running' >&2; exit 1 ;; hang) sleep 30 ;; esac ;;\n")
	b.WriteString("  *) exit 2 ;;\nesac\n")
	fake := filepath.Join(dir, "fake-ssh")
	if err := os.WriteFile(fake, []byte(b.String()), 0o700); err != nil { // #nosec G306 -- test helper
		t.Fatal(err)
	}
	t.Setenv("PORTENV_SSH", fake)
}

// roomToAnswer gives fake servers that do answer longer than 2 s, so a
// loaded test machine doesn't read them as down (the deadline itself is
// tested with one that never answers).
func roomToAnswer(t *testing.T) {
	saved := PingDeadline
	PingDeadline = 15 * time.Second
	t.Cleanup(func() { PingDeadline = saved })
}

// TestServerForMachine: a lease names a machine ID; the server holding it
// is found by asking each of this Mac's servers once, and remembered.
func TestServerForMachine(t *testing.T) {
	roomToAnswer(t)
	fakeServers(t, map[string][2]string{"me@one": {"one-1a2b3c", "ok"}, "me@two": {"two-4d5e6f", "ok"}})
	e := confirmedServers(t, "me@one", "me@two")
	ctx := context.Background()
	host, ok := e.ServerForMachine(ctx, []string{"me@one", "me@two"}, "two-4d5e6f")
	if !ok || host != "me@two" {
		t.Fatalf("got %q, %v", host, ok)
	}
	// Remembered: found again with no server listed to ask.
	if host, ok := e.ServerForMachine(ctx, nil, "two-4d5e6f"); !ok || host != "me@two" {
		t.Fatalf("not remembered: %q, %v", host, ok)
	}
	if _, ok := e.ServerForMachine(ctx, []string{"me@one"}, "another-mac-777777"); ok {
		t.Fatal("a machine no server is was found")
	}
}

// TestServerAnswers: `portenv app ping` over SSH, within 2 s (R-0140).
func TestServerAnswers(t *testing.T) {
	fakeServers(t, map[string][2]string{"me@ok": {"a", "ok"}, "me@down": {"b", "fail"}, "me@hung": {"c", "hang"}})
	confirmedServers(t, "me@ok", "me@down", "me@hung", "me@gone")
	ctx := context.Background()
	saved := PingDeadline
	roomToAnswer(t)
	if !ServerAnswers(ctx, "me@ok") {
		t.Fatal("an answering server")
	}
	if ServerAnswers(ctx, "me@down") || ServerAnswers(ctx, "me@gone") {
		t.Fatal("a server whose portenvd or SSH is down")
	}
	PingDeadline = saved // the real 2 s for the one that hangs
	start := time.Now()
	if ServerAnswers(ctx, "me@hung") {
		t.Fatal("a server that never answers")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the probe took %v, over its 2 s deadline", d)
	}
}

// TestServerIDsAndNames: a server's ID is stable and carries no address; its
// name is the app's ServerName.display rule (GUIDELINES §2).
func TestServerIDsAndNames(t *testing.T) {
	id := ServerID("ubuntu@test-server.example")
	if id != ServerID("ubuntu@test-server.example") || !strings.HasPrefix(id, "srv-") || len(id) != 16 {
		t.Fatalf("id %q", id)
	}
	if strings.Contains(id, "ubuntu") || strings.Contains(id, "example") || id == ServerID("ubuntu@other.example") {
		t.Fatalf("id %q", id)
	}
	if host, ok := ServerByID([]string{"me@a", "ubuntu@test-server.example"}, id); !ok || host != "ubuntu@test-server.example" {
		t.Fatalf("by id: %q, %v", host, ok)
	}
	if _, ok := ServerByID([]string{"me@a"}, id); ok {
		t.Fatal("found an ID among servers that don't have it")
	}
	for in, want := range map[string]string{"ubuntu@test-server": "test-server", "this-mac": "This Mac", "studio": "studio", "a@b@c": "c"} {
		if got := ServerDisplayName(in); got != want {
			t.Errorf("ServerDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}
