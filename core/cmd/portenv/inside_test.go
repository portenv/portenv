// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/portenv/portenv/core/agent"
)

// insideBox makes this test run as if inside a workspace, with a fresh
// home and no Portenv folder or agent socket.
func insideBox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PORTENV_HOME", "")
	t.Setenv("PORTENV_SOCKET", "")
	t.Setenv(agent.BoxIDEnv, "box-0123456789ab")
	return home
}

// runCaught runs the CLI with its stderr caught.
func runCaught(t *testing.T, args ...string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	got := make(chan string)
	go func() { b, _ := io.ReadAll(r); got <- string(b) }()
	code := run(context.Background(), args)
	os.Stderr = saved
	_ = w.Close()
	return code, <-got
}

// nothingIn fails if anything was written under home.
func nothingIn(t *testing.T, home, what string) {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s wrote %s in the home", what, e.Name())
	}
}

// Inside a workspace, the owner's commands are refused before they touch
// anything: portenv init would otherwise write a box's repository key in
// the home, which every save carries (CC-0453).
func TestTheOwnersCommandsAreRefusedInsideAWorkspace(t *testing.T) {
	cases := map[string][]string{
		"init":       {"init", "x"},
		"status":     {"status", "x"}, // without the agent's socket
		"join":       {"join", "x", "--id", "box-0123456789ab", "--storage", "/tmp/s"},
		"attach":     {"attach", "x"},
		"app":        {"app", "open", "x"},
		"ssh-config": {"ssh-config", "x", "--host", "example.org"},
		"machine-id": {"machine-id"},
	}
	// Every owner's command is covered, including any added later.
	var names []string
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := cases[name]; !ok {
			t.Errorf("%s has no case here; add one", name)
		}
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			home := insideBox(t)
			code, stderr := runCaught(t, args...)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if want := "This is the owner's command; run it on your Mac.\n"; stderr != want {
				t.Errorf("stderr %q, want %q", stderr, want)
			}
			nothingIn(t, home, strings.Join(args, " "))
		})
	}
}

// The in-box commands still run inside a workspace, and write nothing in
// the home.
func TestTheInBoxCommandsRunInsideAWorkspace(t *testing.T) {
	t.Run("whoami", func(t *testing.T) {
		home := insideBox(t)
		code, stderr := runCaught(t, "whoami")
		if code != 2 || !strings.Contains(stderr, "works only for an agent inside a workspace (PORTENV_SOCKET isn't set)") {
			t.Errorf("exit %d, stderr %q: want the agent command's own line", code, stderr)
		}
		nothingIn(t, home, "whoami")
	})
	t.Run("guide", func(t *testing.T) {
		home := insideBox(t)
		stdout := os.Stdout
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = devnull.Close() }()
		os.Stdout = devnull
		code, stderr := runCaught(t, "guide")
		os.Stdout = stdout
		if code != 0 {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
		nothingIn(t, home, "guide")
	})
	t.Run("team init", func(t *testing.T) {
		home := insideBox(t)
		dir := t.TempDir()
		stdout := os.Stdout
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = devnull.Close() }()
		os.Stdout = devnull
		code, stderr := runCaught(t, "team", "init", "--dir", dir)
		os.Stdout = stdout
		if code != 0 {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, ".portenv", "agents", "lead.md")); err != nil {
			t.Error(err)
		}
		nothingIn(t, home, "team init")
	})
	t.Run("status with the agent's socket", func(t *testing.T) {
		home := insideBox(t)
		t.Setenv("PORTENV_SOCKET", filepath.Join(t.TempDir(), "portenv.sock"))
		code, stderr := runCaught(t, "status", "set", "testing")
		// No helper answers here, so the call fails, but as the agent's.
		if code == 0 || strings.Contains(stderr, "owner's command") {
			t.Errorf("exit %d, stderr %q: want the agent's status call", code, stderr)
		}
		nothingIn(t, home, "status set")
	})
	t.Run("help", func(t *testing.T) {
		insideBox(t)
		if code, stderr := runCaught(t, "help"); code != 0 || strings.Contains(stderr, "owner's command") {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})
}

// Outside a workspace the owner's commands aren't refused.
func TestOutsideAWorkspaceTheOwnersCommandsRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PORTENV_HOME", filepath.Join(home, "portenv"))
	t.Setenv(agent.BoxIDEnv, "")
	if insideWorkspace() {
		t.Skip("this machine has the box agent's folder; it is a workspace")
	}
	stdout := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devnull.Close() }()
	os.Stdout = devnull
	code, stderr := runCaught(t, "machine-id")
	os.Stdout = stdout
	if code != 0 || strings.Contains(stderr, "owner's command") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}
