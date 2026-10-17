// SPDX-License-Identifier: Apache-2.0

// Package teamboundary tests the per-agent user boundary (ADR 0022) on a
// real engine: each rule is one row, run as an agent's user, and each row
// has a mutation that must make it fail.
package teamboundary

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/docker"
)

// box is a throwaway workspace on the engine, made by the docker driver
// itself, so it has a real workspace's mounts, capabilities and helper.
type box struct {
	d    *docker.Driver
	cli  *client.Client
	id   driver.BoxID
	name string // the container's name
}

func newBox(ctx context.Context, t *testing.T, image string) *box {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	ns := os.Getenv("PORTENV_DOCKER_NAMESPACE")
	d, err := docker.New(docker.Config{StateDir: t.TempDir(), Client: cli, Namespace: ns})
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := driver.BoxID("team-" + hex.EncodeToString(b))
	home := "portenv-test-home-" + string(id)
	name := "portenv-" + string(id)
	cache := "portenv-cache-" + string(id)
	if ns != "" {
		name = "portenv-" + ns + "-" + string(id)
		cache = "portenv-cache-" + ns + "-" + string(id)
	}
	bx := &box{d: d, cli: cli, id: id, name: name}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = d.Stop(ctx, id, time.Second)
		_ = d.Destroy(ctx, id)
		_, _ = cli.VolumeRemove(ctx, home, client.VolumeRemoveOptions{Force: true})
		_, _ = cli.VolumeRemove(ctx, cache, client.VolumeRemoveOptions{Force: true})
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
	for range 200 {
		out, _, _ := bx.root(ctx, "portenv-agent ready")
		switch {
		case strings.Contains(out, "READINESS_STATE_READY"):
			return bx
		case strings.Contains(out, "READINESS_STATE_FAILED"):
			t.Fatalf("the workspace didn't start: %s", out)
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("the workspace didn't become ready")
	return nil
}

// root runs script as root in the box (test setup only: workspaces
// themselves are never reached this way).
func (b *box) root(ctx context.Context, script string) (string, int, error) {
	return b.exec(ctx, "root", []string{"sh", "-c", script})
}

func (b *box) exec(ctx context.Context, user string, argv []string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	created, err := b.cli.ExecCreate(ctx, b.name, client.ExecCreateOptions{User: user, Cmd: argv, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", -1, fmt.Errorf("exec create: %w", err)
	}
	att, err := b.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", -1, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, att.Reader); err != nil && !errors.Is(err, io.EOF) {
		return out.String(), -1, err
	}
	insp, err := b.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return out.String(), -1, err
	}
	return strings.TrimSpace(out.String()), insp.ExitCode, nil
}
