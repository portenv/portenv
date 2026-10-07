// SPDX-License-Identifier: Apache-2.0

// Package drivertest is the conformance suite every box driver must pass.
// It needs a toolbox image (portenv-agent as init) and a real engine.
package drivertest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/portenv/portenv/core/driver"
)

// Env describes the driver under test.
type Env struct {
	Driver driver.Driver
	Image  string
	// HomeRef returns a fresh, unused home storage reference for a box.
	HomeRef func(id driver.BoxID) string
	// Cleanup removes home storage after the test (drivers never do).
	Cleanup func(ref string)
}

// Run runs the conformance suite.
func Run(t *testing.T, env Env) {
	t.Run("Capabilities", func(t *testing.T) { capabilities(t, env) })
	t.Run("Lifecycle", func(t *testing.T) { lifecycle(t, env) })
	t.Run("HomeSurvivesRestartAndDestroy", func(t *testing.T) { homeSurvives(t, env) })
	t.Run("ExecAndStdin", func(t *testing.T) { execStdin(t, env) })
	t.Run("MissingHomeIsReported", func(t *testing.T) { missingHome(t, env) })
}

type box struct {
	t   *testing.T
	env Env
	id  driver.BoxID
	ref string
}

func newBox(t *testing.T, env Env) *box {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := driver.BoxID("conformance-" + hex.EncodeToString(b))
	bx := &box{t: t, env: env, id: id, ref: env.HomeRef(id)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.Driver.Stop(ctx, id, time.Second)
		_ = env.Driver.Destroy(ctx, id)
		if env.Cleanup != nil {
			env.Cleanup(bx.ref)
		}
	})
	return bx
}

func (b *box) create(fresh bool) {
	b.t.Helper()
	ctx := context.Background()
	st, err := b.env.Driver.Create(ctx, driver.Box{ID: b.id, Name: string(b.id), ToolboxImage: b.env.Image})
	if err != nil {
		b.t.Fatalf("Create: %v", err)
	}
	if st != driver.StateCreated {
		b.t.Fatalf("Create returned %v, want created", st)
	}
	if err := b.env.Driver.MountHome(ctx, b.id, driver.HomeStorage{Ref: b.ref, Fresh: fresh}); err != nil {
		b.t.Fatalf("MountHome: %v", err)
	}
}

func (b *box) start() {
	b.t.Helper()
	if _, err := b.env.Driver.Start(context.Background(), b.id); err != nil {
		b.t.Fatalf("Start: %v", err)
	}
}

// exec runs argv in the box as root and returns stdout.
func (b *box) exec(argv ...string) (string, int) {
	b.t.Helper()
	res, err := b.env.Driver.Exec(context.Background(), b.id, driver.ExecRequest{Argv: argv, Timeout: time.Minute})
	if err != nil {
		b.t.Fatalf("Exec %v: %v", argv, err)
	}
	return string(res.Stdout), res.ExitCode
}

// waitReady waits for the agent to report want ("READY" or "FAILED").
func (b *box) waitReady(want string) string {
	b.t.Helper()
	var out string
	for range 240 {
		res, err := b.env.Driver.Exec(context.Background(), b.id, driver.ExecRequest{Argv: []string{"portenv-agent", "ready"}, Timeout: 10 * time.Second})
		if err == nil {
			out = string(res.Stdout)
			if strings.Contains(out, want) {
				return out
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	b.t.Fatalf("box never became %s; last: %q", want, out)
	return ""
}

func capabilities(t *testing.T, env Env) {
	c, err := env.Driver.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Driver == "" || c.EngineVersion == "" || c.Isolation == driver.IsolationUnspecified || len(c.Architectures) == 0 {
		t.Fatalf("incomplete capabilities: %+v", c)
	}
}

func lifecycle(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(true)
	b.start()
	b.waitReady("READY")

	st, err := env.Driver.Stats(context.Background(), b.id)
	if err != nil || st.State != driver.StateRunning || st.ProcessCount == 0 {
		t.Fatalf("Stats while running: %+v, %v", st, err)
	}

	var logs strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for chunk, err := range env.Driver.Logs(ctx, b.id, driver.LogOptions{}) {
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		logs.Write(chunk.Data)
	}
	if !strings.Contains(logs.String(), "box ready") {
		t.Fatalf("logs lack the agent's ready line:\n%s", logs.String())
	}

	if st, err := env.Driver.Stop(context.Background(), b.id, 5*time.Second); err != nil || st != driver.StateStopped {
		t.Fatalf("Stop: %v %v", st, err)
	}
	if st, err := env.Driver.Stats(context.Background(), b.id); err != nil || st.State == driver.StateRunning {
		t.Fatalf("Stats after stop: %+v, %v", st, err)
	}
}

func homeSurvives(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(true)
	b.start()
	b.waitReady("READY")
	if _, code := b.exec("sh", "-c", "echo kept > /home/work/marker && echo lost > /var/tmp/marker"); code != 0 {
		t.Fatal("write markers")
	}
	if _, err := env.Driver.Stop(context.Background(), b.id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	b.start()
	b.waitReady("READY")
	if out, _ := b.exec("cat", "/home/work/marker"); strings.TrimSpace(out) != "kept" {
		t.Fatalf("/home lost across restart: %q", out)
	}
	if _, code := b.exec("test", "-e", "/var/tmp/marker"); code == 0 {
		t.Fatal("the root filesystem survived a restart; it must be fresh each start")
	}

	// Destroy keeps home storage: a new box on the same storage finds it.
	if _, err := env.Driver.Stop(context.Background(), b.id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := env.Driver.Destroy(context.Background(), b.id); err != nil {
		t.Fatal(err)
	}
	b.create(false)
	b.start()
	b.waitReady("READY")
	if out, _ := b.exec("cat", "/home/work/marker"); strings.TrimSpace(out) != "kept" {
		t.Fatalf("Destroy lost the home: %q", out)
	}
}

func execStdin(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(true)
	b.start()
	b.waitReady("READY")
	res, err := env.Driver.Exec(context.Background(), b.id, driver.ExecRequest{
		Argv: []string{"sh", "-c", "cat; echo err >&2; exit 3"}, Stdin: []byte("hello from stdin"), Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "hello from stdin" || strings.TrimSpace(string(res.Stderr)) != "err" || res.ExitCode != 3 {
		t.Fatalf("got stdout %q stderr %q exit %d", res.Stdout, res.Stderr, res.ExitCode)
	}
	res, err = env.Driver.Exec(context.Background(), b.id, driver.ExecRequest{Argv: []string{"id", "-un"}, User: "work", Timeout: time.Minute})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "work" {
		t.Fatalf("exec as work: %q %v", res.Stdout, err)
	}
}

func missingHome(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(false) // new, empty storage without Fresh: the agent must refuse
	b.start()
	out := b.waitReady("FAILED")
	if !strings.Contains(out, "home storage is not attached") {
		t.Fatalf("got %q", out)
	}
}
