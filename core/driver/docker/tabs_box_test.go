// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/drivertest"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// TestTabsInABox: the tab calls (PLAN.md 1.2) work in a real box, through
// the agent channel only: a new box has one "shell" tab; new and rename
// work; a window work opens itself inside the box shows in the watch; and
// tmux runs as work, never as root or portenv-agent.
func TestTabsInABox(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	d, err := New(Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := driver.BoxID("tabs-" + hex.EncodeToString(b))
	home := "portenv-test-home-" + string(id)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = d.Stop(ctx, id, time.Second)
		_ = d.Destroy(ctx, id)
		_, _ = d.cli.VolumeRemove(ctx, home, client.VolumeRemoveOptions{Force: true})
		_, _ = d.cli.VolumeRemove(ctx, d.cacheVolume(id), client.VolumeRemoveOptions{Force: true})
	})
	if _, err := d.Create(ctx, driver.Box{ID: id, Name: string(id), ToolboxImage: image}); err != nil {
		t.Fatal(err)
	}
	if err := d.MountHome(ctx, id, driver.HomeStorage{Ref: home, Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Exec here is the test acting inside the box (as a program of work's
	// would), never an access path.
	sh := func(user, script string) string {
		t.Helper()
		res, err := d.probe(ctx, id, drivertest.ProbeRequest{Argv: []string{"sh", "-c", script}, User: user, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	for range 100 {
		if strings.HasPrefix(sh("", "portenv-agent ready"), "READINESS_STATE_READY") {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	ch, err := d.AgentChannel(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	c := agentv1.NewAgentServiceClient(conn)
	names := func(ts []*typesv1.Tab) string {
		var n []string
		for _, x := range ts {
			s := x.GetName()
			if x.GetActive() {
				s += "*"
			}
			n = append(n, s)
		}
		return strings.Join(n, ",")
	}

	l, err := c.ListTabs(ctx, &agentv1.ListTabsRequest{Session: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l.GetTabs()); got != "shell*" {
		t.Fatalf("a new box's tabs: %q, want shell*", got)
	}
	nt, err := c.NewTab(ctx, &agentv1.NewTabRequest{Session: "main", Name: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenameTab(ctx, &agentv1.RenameTabRequest{Session: "main", Id: nt.GetTab().GetId(), Name: "tests"}); err != nil {
		t.Fatal(err)
	}
	w, err := c.WatchTabs(ctx, &agentv1.WatchTabsRequest{Session: "main"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := names(m.GetTabs()); got != "shell,tests*" {
		t.Fatalf("tabs %q, want shell,tests*", got)
	}
	// A program of work's opens a window itself: it's a tab at once.
	sh("work", "tmux new-window -t =main: -n sneaky")
	if m, err = w.Recv(); err != nil {
		t.Fatal(err)
	}
	if got := names(m.GetTabs()); got != "shell,tests,sneaky*" {
		t.Fatalf("after work's own new-window: %q, want shell,tests,sneaky*", got)
	}
	if got := sh("", "ps -eo user=,comm= | awk '$2 ~ /^tmux/ {print $1}' | sort -u"); !onlyWork(got) {
		t.Errorf("tmux runs as %q, want only work", got)
	}
}

// onlyWork: every non-empty line is "work".
func onlyWork(s string) bool {
	seen := false
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		if l != "work" {
			return false
		}
		seen = true
	}
	return seen
}
