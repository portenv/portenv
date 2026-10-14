// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/drivertest"
)

// TestConformance runs the driver conformance suite against the local
// Docker engine. It needs a toolbox image: set PORTENV_TEST_IMAGE (make
// driver-test does).
func TestConformance(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	state := t.TempDir()
	d, err := New(Config{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	drivertest.Run(t, drivertest.Env{
		Driver: d,
		Probe:  d.probe,
		Restarted: func() driver.Driver {
			again, err := New(Config{StateDir: state})
			if err != nil {
				t.Fatal(err)
			}
			return again
		},
		Image:   image,
		HomeRef: func(id driver.BoxID) string { return "portenv-test-home-" + string(id) },
		Cleanup: func(ref string) {
			_, _ = d.cli.VolumeRemove(context.Background(), ref, client.VolumeRemoveOptions{Force: true})
			id := driver.BoxID(strings.TrimPrefix(ref, "portenv-test-home-"))
			_, _ = d.cli.VolumeRemove(context.Background(), d.cacheVolume(id), client.VolumeRemoveOptions{Force: true})
		},
	})
}

func TestBoxIDsAreValidated(t *testing.T) {
	d := &Driver{cfg: Config{StateDir: t.TempDir()}}
	for _, id := range []driver.BoxID{"", "../x", "a/b", "-x"} {
		if _, err := d.specPath(id); err == nil {
			t.Errorf("box ID %q accepted", id)
		}
	}
}

func TestHostConfigKeepsIsolation(t *testing.T) {
	d := &Driver{cfg: Config{StateDir: t.TempDir(), StorageDir: "/srv/storage/b", StorageRepo: StorageMount + "/b"}}
	for _, s := range []spec{
		{Box: driver.Box{ID: "b"}},
		{Box: driver.Box{ID: "b", Resources: driver.Resources{CPUMillis: 2000, MemoryBytes: 4 << 30, SharedMemoryBytes: 2 << 30}},
			Home: driver.HomeStorage{Ref: "vol", Fresh: true}},
	} {
		h := d.hostConfig(s)
		switch {
		case h.Privileged:
			t.Error("box is privileged")
		case len(h.CapAdd) > 0:
			t.Errorf("box adds capabilities %v", h.CapAdd)
		case h.PidMode.IsHost(), h.IpcMode.IsHost(), h.NetworkMode.IsHost(), h.UsernsMode.IsHost():
			t.Error("box shares a host namespace")
		case len(h.Devices) > 0:
			t.Errorf("box gets host devices %v", h.Devices)
		}
		// The only published port is the agent channel, on loopback (ADR 0010).
		for port, binds := range h.PortBindings {
			if port != agentPort {
				t.Errorf("box publishes port %v", port)
			}
			for _, b := range binds {
				if !b.HostIP.IsLoopback() {
					t.Errorf("agent port published on %v, want 127.0.0.1 only", b.HostIP)
				}
			}
		}
		if len(h.PortBindings) != 1 {
			t.Errorf("port bindings %v, want just the agent channel", h.PortBindings)
		}
		for _, o := range h.SecurityOpt {
			if strings.Contains(o, "unconfined") {
				t.Errorf("box relaxes security: %s", o)
			}
		}
	}
}

// TestABoxMountsOnlyItsOwn (ADR 0021, R-0201): a box gets exactly its home
// volume, its own repository's folder and restic's cache volume. Never the
// folder of every box's repositories (a sibling could be deleted from
// inside), never an engine socket (root on the machine), never the folder
// of Portenv's sockets, and no Binds. Both local storages: this Mac's
// Repositories folder and a drive's boxes folder.
func TestABoxMountsOnlyItsOwn(t *testing.T) {
	for _, c := range []struct{ name, dir, repo string }{
		{"this Mac", "/Users/x/Library/Application Support/Portenv/Repositories/box-0123456789ab", StorageMount + "/box-0123456789ab"},
		{"a drive", "/Volumes/Backup/portenv/boxes/box-0123456789ab", StorageMount + "/boxes/box-0123456789ab"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &Driver{cfg: Config{StateDir: t.TempDir(), StorageDir: c.dir, StorageRepo: c.repo}}
			h := d.hostConfig(spec{Box: driver.Box{ID: "box-0123456789ab"}, Home: driver.HomeStorage{Ref: "vol"}})
			if len(h.Binds) != 0 || len(h.VolumesFrom) != 0 {
				t.Fatalf("binds %v, volumes from %v", h.Binds, h.VolumesFrom)
			}
			want := map[string]mount.Mount{
				"/home":    {Type: mount.TypeVolume, Source: "vol", Target: "/home"},
				c.repo:     {Type: mount.TypeBind, Source: c.dir, Target: c.repo},
				CacheMount: {Type: mount.TypeVolume, Source: d.cacheVolume("box-0123456789ab"), Target: CacheMount},
			}
			if len(h.Mounts) != len(want) {
				t.Fatalf("mounts %+v, want exactly home, its own repository and the cache", h.Mounts)
			}
			for _, m := range h.Mounts {
				w, ok := want[m.Target]
				if !ok || m.Type != w.Type || m.Source != w.Source || m.ReadOnly {
					t.Errorf("mount %s %s from %s", m.Type, m.Target, m.Source)
				}
				if engineSocket(m.Source) || engineSocket(m.Target) {
					t.Errorf("an engine socket is mounted: %s at %s", m.Source, m.Target)
				}
				if m.Target == StorageMount || m.Target == StorageMount+"/boxes" {
					t.Errorf("the folder of every box's repositories is mounted at %s", m.Target)
				}
			}
		})
	}
}

// TestEngineSocketsAreRecognised: the names the mounts test refuses.
func TestEngineSocketsAreRecognised(t *testing.T) {
	for _, p := range []string{"/var/run/docker.sock", "/run/docker.sock", "/Users/x/.docker/run/docker.sock", "/run/containerd/containerd.sock", "/run/podman/podman.sock", "/run/user/1000/podman/podman.sock"} {
		if !engineSocket(p) {
			t.Errorf("%s not recognised", p)
		}
	}
	for _, p := range []string{"/home", "/run/portenv/storage/box-1", "/var/cache/portenv-sync", "docker.socket.md"} {
		if engineSocket(p) {
			t.Errorf("%s taken for an engine socket", p)
		}
	}
}

// TestNoStorageWithoutItsRepository (R-0201): a driver given a storage
// folder must be told the one repository inside it it mounts, under
// StorageMount; otherwise it refuses, so the whole folder can't come back.
func TestNoStorageWithoutItsRepository(t *testing.T) {
	for _, repo := range []string{"", StorageMount, StorageMount + "/boxes", "/home/work", StorageMount + "/../x", StorageMount + "/a/b/c"} {
		if err := (Config{StateDir: t.TempDir(), StorageDir: "/srv/storage/box-1", StorageRepo: repo}).check(); err == nil {
			t.Errorf("storage repository %q accepted", repo)
		}
	}
	for _, repo := range []string{StorageMount + "/box-1", StorageMount + "/boxes/box-1"} {
		if err := (Config{StateDir: t.TempDir(), StorageDir: "/srv/storage/box-1", StorageRepo: repo}).check(); err != nil {
			t.Errorf("storage repository %q refused: %v", repo, err)
		}
	}
	if err := (Config{StateDir: t.TempDir()}).check(); err != nil {
		t.Errorf("no storage (SFTP, S3): %v", err)
	}
	// New refuses it too, so no driver is ever made that mounts the whole
	// folder.
	if _, err := New(Config{StateDir: t.TempDir(), StorageDir: "/srv/storage", StorageRepo: StorageMount}); err == nil {
		t.Error("New made a driver mounting the whole storage folder")
	}
}

// TestMountsCompare: the take-over's comparison of a running container's
// mounts with what its spec gives now. Order doesn't matter; anything
// else does.
func TestMountsCompare(t *testing.T) {
	home := mount.Mount{Type: mount.TypeVolume, Source: "vol", Target: "/home"}
	own := mount.Mount{Type: mount.TypeBind, Source: "/r/box-1", Target: StorageMount + "/box-1"}
	cache := mount.Mount{Type: mount.TypeVolume, Source: "portenv-cache-box-1", Target: CacheMount}
	old := mount.Mount{Type: mount.TypeBind, Source: "/r", Target: StorageMount}
	now := []mount.Mount{home, own, cache}
	if !sameMounts(now, []mount.Mount{cache, home, own}) {
		t.Error("the same mounts in another order differ")
	}
	for name, ran := range map[string][]mount.Mount{
		"the old whole-folder mount": {home, old, cache},
		"one more":                   {home, own, cache, old},
		"one fewer":                  {home, own},
		"read-only":                  {home, {Type: own.Type, Source: own.Source, Target: own.Target, ReadOnly: true}, cache},
		"none":                       nil,
	} {
		if sameMounts(now, ran) {
			t.Errorf("%s: taken for the same", name)
		}
	}
}

// TestAgentRefusesForbiddenCapabilities starts the toolbox image the way a
// careless engine configuration might (with CAP_SYS_PTRACE, CAP_SYS_ADMIN,
// or privileged) and expects the agent to refuse to report ready.
func TestAgentRefusesForbiddenCapabilities(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cases := map[string]container.HostConfig{
		"CAP_SYS_PTRACE": {CapAdd: []string{"SYS_PTRACE"}},
		"CAP_SYS_ADMIN":  {CapAdd: []string{"SYS_ADMIN"}},
		"privileged":     {Privileged: true},
	}
	for name, hc := range cases {
		t.Run(name, func(t *testing.T) {
			cname := "portenv-captest-" + strings.ToLower(strings.ReplaceAll(name, "_", "-"))
			_, _ = cli.ContainerRemove(ctx, cname, client.ContainerRemoveOptions{Force: true})
			if _, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
				Name: cname, Config: &container.Config{Image: image, Env: []string{"PORTENV_INIT_HOME=1"}}, HostConfig: &hc,
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = cli.ContainerRemove(ctx, cname, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			})
			if _, err := cli.ContainerStart(ctx, cname, client.ContainerStartOptions{}); err != nil {
				t.Fatal(err)
			}
			d := &Driver{cli: cli}
			var out string
			for range 60 {
				res, err := d.execIn(ctx, cname, []string{"portenv-agent", "ready"})
				out = string(res.Stdout)
				if err == nil && strings.Contains(out, "FAILED") {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			want := name
			if name == "privileged" {
				want = "CAP_SYS_PTRACE"
			}
			if !strings.Contains(out, "FAILED") || !strings.Contains(out, want) {
				t.Fatalf("agent did not refuse a box with %s: %q", name, out)
			}
		})
	}
}
