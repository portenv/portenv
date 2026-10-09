// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// fakeRunner records commands and answers them from a table keyed by the
// program name and first argument. cp -a is performed for real so home
// initialisation can be checked on disk.
type fakeRunner struct {
	calls     []string
	installed map[string]bool // dpkg-query answers
	uid       string          // "" means the user does not exist
	fail      string          // a command prefix that fails
	badPkg    string          // apt-get install fails whenever this package is asked for
}

func (f *fakeRunner) Run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	if f.fail != "" && strings.HasPrefix(call, f.fail) {
		return []byte("E: something broke"), errors.New("exit status 100")
	}
	switch name {
	case "id":
		if f.uid == "" {
			return []byte("id: 'work': no such user"), errors.New("exit status 1")
		}
		return []byte(f.uid + "\n"), nil
	case "dpkg-query":
		if f.installed[args[len(args)-1]] {
			return []byte("installed"), nil
		}
		return []byte("dpkg-query: no packages found"), errors.New("exit status 1")
	case "cp":
		src := strings.TrimSuffix(args[1], "/.")
		return nil, os.CopyFS(args[2], os.DirFS(src))
	case "apt-get":
		if args[0] == "install" && !slices.Contains(env, "DEBIAN_FRONTEND=noninteractive") {
			return nil, errors.New("apt-get install without DEBIAN_FRONTEND")
		}
		if args[0] == "install" && f.badPkg != "" && slices.Contains(args, f.badPkg) {
			return []byte("E: Unable to locate package " + f.badPkg), errors.New("exit status 100")
		}
		if args[0] == "install" {
			for _, a := range args[1:] {
				if f.installed != nil && !strings.HasPrefix(a, "-") {
					f.installed[packageName(a)] = true
				}
			}
		}
	}
	return nil, nil
}

func (f *fakeRunner) ran(prefix string) bool {
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	skel := filepath.Join(root, "skel")
	write(t, filepath.Join(skel, ".portenv", "apt-packages.txt"), "# none yet\n")
	write(t, filepath.Join(skel, ".portenv", "excludes"), "/home/*/.cache\n")
	return Config{User: "work", UID: 1000, HomeRoot: filepath.Join(root, "home"), Skeleton: skel}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBootRefusesMissingHome(t *testing.T) {
	cfg := testConfig(t)
	run := &fakeRunner{uid: "1000"}
	err := Boot(context.Background(), cfg, run, NewReadiness())
	if !errors.Is(err, ErrNoHome) {
		t.Fatalf("got %v, want ErrNoHome", err)
	}
	if _, err := os.Stat(cfg.Home()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Boot created a home without InitHome")
	}
}

func TestBootRefusesEmptyHome(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(cfg.Home(), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Boot(context.Background(), cfg, &fakeRunner{uid: "1000"}, NewReadiness())
	if !errors.Is(err, ErrNoHome) {
		t.Fatalf("got %v, want ErrNoHome", err)
	}
}

func TestBootInitialisesNewHome(t *testing.T) {
	cfg := testConfig(t)
	cfg.InitHome = true
	run := &fakeRunner{uid: "1000"}
	if err := Boot(context.Background(), cfg, run, NewReadiness()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".portenv/apt-packages.txt", ".portenv/excludes"} {
		if _, err := os.Stat(filepath.Join(cfg.Home(), f)); err != nil {
			t.Errorf("skeleton file %s missing: %v", f, err)
		}
	}
	fi, err := os.Stat(cfg.Home())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("home mode %v, want 0700", fi.Mode().Perm())
	}
	if !run.ran("chown -R 1000:1000 " + cfg.Home()) {
		t.Errorf("home not chowned to 1000:1000; calls %q", run.calls)
	}
}

func TestBootKeepsExistingHome(t *testing.T) {
	cfg := testConfig(t)
	cfg.InitHome = true // must not matter when a home exists
	write(t, filepath.Join(cfg.Home(), "project", "main.go"), "package main\n")
	if err := os.Chmod(cfg.Home(), 0o755); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{uid: "1000"}
	if err := Boot(context.Background(), cfg, run, NewReadiness()); err != nil {
		t.Fatal(err)
	}
	if run.ran("cp") || run.ran("chown") {
		t.Fatalf("existing home was overwritten; calls %q", run.calls)
	}
	if _, err := os.Stat(filepath.Join(cfg.Home(), ".portenv")); !errors.Is(err, os.ErrNotExist) {
		t.Error("skeleton copied into an existing home")
	}
	if fi, _ := os.Stat(cfg.Home()); fi.Mode().Perm() != 0o700 {
		t.Errorf("home mode %v, want 0700", fi.Mode().Perm())
	}
}

func TestBootCreatesMissingUser(t *testing.T) {
	cfg := testConfig(t)
	cfg.InitHome = true
	run := &fakeRunner{}
	if err := Boot(context.Background(), cfg, run, NewReadiness()); err != nil {
		t.Fatal(err)
	}
	if !run.ran("useradd --uid 1000 --user-group --no-create-home") {
		t.Fatalf("user not created; calls %q", run.calls)
	}
}

func TestBootRejectsWrongUID(t *testing.T) {
	cfg := testConfig(t)
	err := Boot(context.Background(), cfg, &fakeRunner{uid: "1001"}, NewReadiness())
	if err == nil || !strings.Contains(err.Error(), "uid 1001") {
		t.Fatalf("got %v, want a uid error", err)
	}
}

func TestBootReplaysOnlyMissingPackages(t *testing.T) {
	cfg := testConfig(t)
	write(t, filepath.Join(cfg.Home(), ".portenv", "apt-packages.txt"), "jq\nredis-tools\nlibpq-dev=16.1\n")
	run := &fakeRunner{uid: "1000", installed: map[string]bool{"jq": true}}
	r := NewReadiness()
	if err := Boot(context.Background(), cfg, run, r); err != nil {
		t.Fatal(err)
	}
	if !run.ran("apt-get update") {
		t.Error("package lists not updated")
	}
	want := "apt-get install -y --no-install-recommends -- redis-tools libpq-dev=16.1"
	if !slices.Contains(run.calls, want) {
		t.Fatalf("want %q; calls %q", want, run.calls)
	}
	if state, _ := r.Get(); state != agentv1.ReadinessState_READINESS_STATE_STARTING {
		t.Errorf("Boot changed readiness to %v; the caller marks ready", state)
	}
}

func TestBootSkipsAptWhenNothingMissing(t *testing.T) {
	cfg := testConfig(t)
	write(t, filepath.Join(cfg.Home(), ".portenv", "apt-packages.txt"), "jq\n")
	run := &fakeRunner{uid: "1000", installed: map[string]bool{"jq": true}}
	if err := Boot(context.Background(), cfg, run, NewReadiness()); err != nil {
		t.Fatal(err)
	}
	if run.ran("apt-get") {
		t.Fatalf("apt-get ran with nothing missing; calls %q", run.calls)
	}
}

// TestAFailedPackageNeverStopsTheBox: a package that can't be installed is
// found (the others still install), recorded with the error, and the box
// starts all the same (PLAN.md 1.4).
func TestAFailedPackageNeverStopsTheBox(t *testing.T) {
	cfg := testConfig(t)
	write(t, filepath.Join(cfg.Home(), ".portenv", "apt-packages.txt"), "jq\nno-such-package\ntree\n")
	run := &fakeRunner{uid: "1000", installed: map[string]bool{}, badPkg: "no-such-package"}
	r := NewReadiness()
	if err := Boot(context.Background(), cfg, run, r); err != nil {
		t.Fatalf("Boot: %v, want the box to start", err)
	}
	failed, msg := r.Packages()
	if !slices.Equal(failed, []string{"no-such-package"}) {
		t.Fatalf("failed packages %q, want only no-such-package", failed)
	}
	if !strings.Contains(msg, "Unable to locate package no-such-package") {
		t.Fatalf("error %q, want apt-get's message", msg)
	}
	if !run.installed["jq"] || !run.installed["tree"] {
		t.Fatalf("the other packages were not installed; calls %q", run.calls)
	}
}

// TestNoPackageListsStillStarts: apt-get update failing (no network) marks
// every missing package as failed; the box still starts.
func TestNoPackageListsStillStarts(t *testing.T) {
	cfg := testConfig(t)
	write(t, filepath.Join(cfg.Home(), ".portenv", "apt-packages.txt"), "jq\ntree\n")
	run := &fakeRunner{uid: "1000", installed: map[string]bool{}, fail: "apt-get update"}
	r := NewReadiness()
	if err := Boot(context.Background(), cfg, run, r); err != nil {
		t.Fatalf("Boot: %v, want the box to start", err)
	}
	if failed, _ := r.Packages(); !slices.Equal(failed, []string{"jq", "tree"}) {
		t.Fatalf("failed packages %q, want both", failed)
	}
}

// TestRetryInstallsWhatFailed: once the package can be installed (the list
// fixed, the network back), a retry installs it and clears the record.
func TestRetryInstallsWhatFailed(t *testing.T) {
	cfg := testConfig(t)
	write(t, filepath.Join(cfg.Home(), ".portenv", "apt-packages.txt"), "jq\n")
	run := &fakeRunner{uid: "1000", installed: map[string]bool{}, fail: "apt-get update"}
	r := NewReadiness()
	if err := Boot(context.Background(), cfg, run, r); err != nil {
		t.Fatal(err)
	}
	if failed, _ := r.Packages(); len(failed) != 1 {
		t.Fatalf("failed %q, want jq", failed)
	}
	run.fail = ""
	RetryPackages(context.Background(), cfg, run, r)
	if failed, msg := r.Packages(); len(failed) != 0 || msg != "" {
		t.Fatalf("after the retry: %q %q, want nothing failed", failed, msg)
	}
	if !run.installed["jq"] {
		t.Fatal("jq was not installed by the retry")
	}
}

// TestRetryDelaysBackOff: background retries slow down, to at most an hour.
func TestRetryDelaysBackOff(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour}
	for i, w := range want {
		if got := retryDelay(i); got != w {
			t.Errorf("retry %d: %v, want %v", i, got, w)
		}
	}
}

func TestBootRejectsInvalidPackageList(t *testing.T) {
	cfg := testConfig(t)
	write(t, filepath.Join(cfg.Home(), ".portenv", "apt-packages.txt"), "--allow-unauthenticated\n")
	run := &fakeRunner{uid: "1000"}
	if err := Boot(context.Background(), cfg, run, NewReadiness()); err == nil {
		t.Fatal("want an error for an invalid package list")
	}
	if run.ran("apt-get") {
		t.Fatalf("apt-get ran despite an invalid list; calls %q", run.calls)
	}
}
