// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/drivertest"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// storageBox is a box on a real engine whose storage folder is one of
// several boxes' repositories under parent, the way this Mac's
// Repositories folder holds them.
type storageBox struct {
	d    *Driver
	id   driver.BoxID
	home string
}

func newStorageBox(ctx context.Context, t *testing.T, image, parent string, storageDir, storageRepo string) storageBox {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := driver.BoxID("storage-" + hex.EncodeToString(b))
	if storageDir == "" {
		storageDir = filepath.Join(parent, string(id))
		storageRepo = StorageMount + "/" + string(id)
	}
	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Set directly, so a test can build the old whole-folder mount that
	// Config.check now refuses.
	d.cfg.StorageDir, d.cfg.StorageRepo = storageDir, storageRepo
	sb := storageBox{d: d, id: id, home: "portenv-test-home-" + string(id)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = d.Stop(ctx, id, time.Second)
		_ = d.Destroy(ctx, id)
		_, _ = d.cli.VolumeRemove(ctx, sb.home, client.VolumeRemoveOptions{Force: true})
		_, _ = d.cli.VolumeRemove(ctx, d.cacheVolume(id), client.VolumeRemoveOptions{Force: true})
	})
	if _, err := d.Create(ctx, driver.Box{ID: id, Name: string(id), ToolboxImage: image}); err != nil {
		t.Fatal(err)
	}
	if err := d.MountHome(ctx, id, driver.HomeStorage{Ref: sb.home, Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	return sb
}

func (sb storageBox) sh(ctx context.Context, t *testing.T, user, script string) (string, int) {
	t.Helper()
	res, err := sb.d.probe(ctx, sb.id, drivertest.ProbeRequest{Argv: []string{"sh", "-c", script}, User: user, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(res.Stdout) + string(res.Stderr)), res.ExitCode
}

// readiness waits for the helper's start sequence to end and returns its
// last line: READY, or FAILED with why.
func (sb storageBox) readiness(ctx context.Context, t *testing.T) string {
	t.Helper()
	var out string
	for range 100 {
		out, _ = sb.sh(ctx, t, "", "portenv-agent ready")
		if strings.Contains(out, "READINESS_STATE_READY") || strings.Contains(out, "READINESS_STATE_FAILED") {
			return out
		}
		time.Sleep(300 * time.Millisecond)
	}
	return out
}

// TestABoxSeesOnlyItsOwnRepository (R-0201): two boxes whose repositories
// share one folder. Root in each sees its own repository and nothing of
// the other's: it can't list, read, create or delete there.
func TestABoxSeesOnlyItsOwnRepository(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	parent := t.TempDir()
	a := newStorageBox(ctx, t, image, parent, "", "")
	b := newStorageBox(ctx, t, image, parent, "", "")
	if err := os.WriteFile(filepath.Join(parent, string(b.id), "b-marker"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := a.readiness(ctx, t); !strings.Contains(r, "READINESS_STATE_READY") {
		t.Fatalf("box a didn't start: %s", r)
	}
	if out, _ := a.sh(ctx, t, "", "ls -A "+StorageMount); out != string(a.id) {
		t.Fatalf("%s in box a lists %q, want only its own repository %s", StorageMount, out, a.id)
	}
	for _, script := range []string{
		"ls " + StorageMount + "/" + string(b.id),
		"cat " + StorageMount + "/" + string(b.id) + "/b-marker",
		"touch " + StorageMount + "/" + string(b.id) + "/x",
	} {
		if out, code := a.sh(ctx, t, "", script); code == 0 {
			t.Errorf("root in box a: %q worked: %s", script, out)
		}
	}
	// Deleting needs no listing: try it, then check from the host.
	a.sh(ctx, t, "", "rm -rf "+StorageMount+"/"+string(b.id)+" "+StorageMount+"/../storage/"+string(b.id))
	if out, _ := a.sh(ctx, t, "", "find / -xdev -name b-marker 2>/dev/null; find "+StorageMount+" -name b-marker 2>/dev/null"); out != "" {
		t.Errorf("box a finds box b's repository: %s", out)
	}
	if _, err := os.Stat(filepath.Join(parent, string(b.id), "b-marker")); err != nil {
		t.Fatalf("box b's repository was touched from box a: %v", err)
	}
	if _, code := a.sh(ctx, t, "", "touch "+StorageMount+"/"+string(a.id)+"/own && rm "+StorageMount+"/"+string(a.id)+"/own"); code != 0 {
		t.Error("box a can't write its own repository")
	}
}

// TestTakeOverRefusesAnOldMountBeforeReKeying (R-0201): a box left running
// with the old mount of the whole Repositories folder (a portenvd from
// before this fix started it). The take-over compares mounts first: it
// refuses without giving the box any new key material, so the old
// channel still works, and no new one exists. Stopped and started again,
// the box gets only its own repository, and its home is as it was.
func TestTakeOverRefusesAnOldMountBeforeReKeying(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	parent := t.TempDir()
	old := newStorageBox(ctx, t, image, parent, parent, StorageMount)
	// The helper refuses the whole folder (ruling 2), and says so.
	if r := old.readiness(ctx, t); !strings.Contains(r, "READINESS_STATE_FAILED") || !strings.Contains(r, "other workspaces") {
		t.Fatalf("the helper started with every workspace's repositories mounted: %s", r)
	}
	// A box left running across the update has its home: the helper here
	// refused before making one, so make it as it would be, work's own.
	if _, code := old.sh(ctx, t, "", "mkdir -p /home/work && echo kept > /home/work/kept.txt && chown -R 1000:1000 /home/work"); code != 0 {
		t.Fatal("write to the home")
	}
	oldCh, err := old.d.AgentChannel(ctx, old.id)
	if err != nil {
		t.Fatal(err)
	}

	// A new portenvd: the same box, its own repository only.
	now, err := New(Config{StateDir: old.d.cfg.StateDir, StorageDir: filepath.Join(parent, string(old.id)), StorageRepo: StorageMount + "/" + string(old.id)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(now.cfg.StorageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := now.Rekey(ctx, old.id); !errors.Is(err, driver.ErrMountsChanged) {
		t.Fatalf("take-over of a box with the old mount: %v, want ErrMountsChanged", err)
	}
	if _, err := now.AgentChannel(ctx, old.id); !errors.Is(err, driver.ErrNoChannel) {
		t.Fatalf("the refused take-over left a channel: %v", err)
	}
	// No new secrets reached the box: the old channel still answers.
	conn, err := agent.DialChannel(oldCh.Dial, oldCh.CertPEM, oldCh.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentv1.NewAgentServiceClient(conn).GetVersion(ctx, &agentv1.GetVersionRequest{}); err != nil {
		t.Fatalf("the old channel stopped answering, so the box was re-keyed: %v", err)
	}
	_ = conn.Close()

	// What portenvd does next: stop, then start (which recreates).
	if _, err := now.Stop(ctx, old.id, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := now.Start(ctx, old.id); err != nil {
		t.Fatal(err)
	}
	restarted := storageBox{d: now, id: old.id}
	if r := restarted.readiness(ctx, t); !strings.Contains(r, "READINESS_STATE_READY") {
		t.Fatalf("after the restart: %s", r)
	}
	if out, _ := restarted.sh(ctx, t, "", "ls -A "+StorageMount); out != string(old.id) {
		t.Fatalf("after the restart, %s lists %q", StorageMount, out)
	}
	if out, _ := restarted.sh(ctx, t, "", "cat /home/work/kept.txt"); out != "kept" {
		t.Fatalf("the home after the restart: %q, want it as it was", out)
	}
	if err := now.Rekey(ctx, old.id); err != nil {
		t.Fatalf("take-over of the restarted box: %v", err)
	}
}

// TestTakeOverOfAnUnchangedBoxReKeysIt (R-0242): portenvd restarts (an
// update, launchctl kickstart) while a box runs, and nothing about its
// mounts changed. The new portenvd re-keys it in place: no restart, so its
// programs (the tabs) keep running. On Docker Desktop this failed: the
// engine reports bind sources as /host_mnt/<real path>.
func TestTakeOverOfAnUnchangedBoxReKeysIt(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	first := newStorageBox(ctx, t, image, t.TempDir(), "", "")
	if r := first.readiness(ctx, t); !strings.Contains(r, "READINESS_STATE_READY") {
		t.Fatalf("the box didn't start: %s", r)
	}
	// Something running, as a tab's program would be, and a mark in the
	// root file system, which a restart (a recreated container) would lose.
	if _, code := first.sh(ctx, t, "", "echo here > /run/portenv-test-mark; nohup sleep 600 >/dev/null 2>&1 &"); code != 0 {
		t.Fatal("start a program")
	}
	pid, _ := first.sh(ctx, t, "", "pgrep -o -x sleep")

	// A new portenvd: a new driver, the same configuration.
	again, err := New(Config{StateDir: first.d.cfg.StateDir, StorageDir: first.d.cfg.StorageDir, StorageRepo: first.d.cfg.StorageRepo})
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Rekey(ctx, first.id); err != nil {
		t.Fatalf("take-over of an unchanged box: %v, want it re-keyed in place", err)
	}
	if _, err := again.AgentChannel(ctx, first.id); err != nil {
		t.Fatalf("no channel after the take-over: %v", err)
	}
	same := storageBox{d: again, id: first.id}
	if out, _ := same.sh(ctx, t, "", "cat /run/portenv-test-mark"); out != "here" {
		t.Fatalf("the box was restarted: the mark is %q", out)
	}
	if now, _ := same.sh(ctx, t, "", "pgrep -o -x sleep"); pid == "" || now != pid {
		t.Fatalf("the program running before the take-over: %q, now %q", pid, now)
	}
}
