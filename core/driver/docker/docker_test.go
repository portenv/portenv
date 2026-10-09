// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
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
	d := &Driver{cfg: Config{StateDir: t.TempDir(), StorageDir: "/srv/storage"}}
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
