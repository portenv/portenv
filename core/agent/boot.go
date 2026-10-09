// SPDX-License-Identifier: Apache-2.0

// Package agent implements portenv-agent, the box's init process. In
// milestone 0.2 it runs the start sequence (user, home, package replay),
// reports readiness over portenv.agent.v1, reaps orphaned processes and
// shuts the box down cleanly. Terminals, lanes, port discovery and audit
// arrive in later phases.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config describes the box's fixed layout. DefaultConfig matches the plan.
type Config struct {
	User     string // main user, "work"
	UID      int    // 1000
	HomeRoot string // "/home"; the saved unit
	Skeleton string // fresh-home template inside the image
	// InitHome allows creating a home from the skeleton when none exists.
	// Only the caller knows a box is brand new (resume rule 1), so this is
	// off unless PORTENV_INIT_HOME=1; otherwise a missing home is an error,
	// never silently replaced.
	InitHome bool
	// CapBoundingSet reads the box's capability bounding set; nil skips the
	// isolation check (tests). DefaultConfig reads /proc/self/status.
	CapBoundingSet func() (uint64, error)
}

// DefaultConfig returns the box layout from docs/PLAN.md.
func DefaultConfig() Config {
	return Config{
		User:     "work",
		UID:      1000,
		HomeRoot: "/home",
		Skeleton: "/usr/share/portenv/skel",
		InitHome: os.Getenv("PORTENV_INIT_HOME") == "1",
		// A container's bounding set is the box's. In a VM (Phase 1) the
		// agent drops these itself for every process it starts instead.
		CapBoundingSet: BoundingSet,
	}
}

// Home is the main user's home directory.
func (c Config) Home() string { return filepath.Join(c.HomeRoot, c.User) }

// ErrNoHome means the box has no home and was not asked to create one.
var ErrNoHome = errors.New("home storage is not attached or is empty")

// Runner runs a program to completion and returns its combined output.
type Runner interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

// ExecRunner runs programs with os/exec. Mu, when set, is held for each
// program's whole lifetime so a process-wide reaper (see Init) cannot reap
// the child before Wait does.
type ExecRunner struct {
	Mu *sync.Mutex
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	if r.Mu != nil {
		r.Mu.Lock()
		defer r.Mu.Unlock()
	}
	// #nosec G204 -- callers pass fixed program names; package names are validated by ParsePackages.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// Boot runs the start sequence and reports each step to r. It does not mark
// the box ready; the caller does that when Boot returns nil.
func Boot(ctx context.Context, cfg Config, run Runner, r *Readiness) error {
	if cfg.CapBoundingSet != nil {
		r.Step("checking isolation")
		if err := checkIsolation(cfg.CapBoundingSet); err != nil {
			return err
		}
	}
	r.Step("checking user " + cfg.User)
	if err := ensureUser(ctx, cfg, run); err != nil {
		return err
	}
	r.Step("checking home")
	if err := ensureHome(ctx, cfg, run); err != nil {
		return err
	}
	return replayPackages(ctx, cfg, run, r)
}

func ensureUser(ctx context.Context, cfg Config, run Runner) error {
	out, err := run.Run(ctx, nil, "id", "-u", cfg.User)
	if err != nil {
		uid := strconv.Itoa(cfg.UID)
		if out, err := run.Run(ctx, nil, "useradd", "--uid", uid, "--user-group",
			"--no-create-home", "--home-dir", cfg.Home(), "--shell", "/bin/bash", cfg.User); err != nil {
			return cmdError("create user "+cfg.User, out, err)
		}
		return nil
	}
	if got := strings.TrimSpace(string(out)); got != strconv.Itoa(cfg.UID) {
		return fmt.Errorf("user %s has uid %s, want %d", cfg.User, got, cfg.UID)
	}
	return nil
}

func ensureHome(ctx context.Context, cfg Config, run Runner) error {
	home := cfg.Home()
	entries, err := os.ReadDir(home)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("read home %s: %w", home, err)
	case len(entries) > 0:
		return os.Chmod(home, 0o700) // #nosec G302 -- a directory: owner-only
	}
	// No home, or an empty one.
	if !cfg.InitHome {
		return fmt.Errorf("%w: %s (set PORTENV_INIT_HOME=1 only for a brand-new box)", ErrNoHome, home)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}
	if out, err := run.Run(ctx, nil, "cp", "-a", cfg.Skeleton+"/.", home); err != nil {
		return cmdError("copy home skeleton", out, err)
	}
	owner := fmt.Sprintf("%d:%d", cfg.UID, cfg.UID)
	if out, err := run.Run(ctx, nil, "chown", "-R", owner, home); err != nil {
		return cmdError("set home owner", out, err)
	}
	return os.Chmod(home, 0o700) // #nosec G302 -- a directory: owner-only
}

// replayPackages installs what apt-packages.txt lists and the box lacks. A
// package that can't be installed never stops the box from starting
// (PLAN.md 1.4): it is recorded in r (for the inspector), and the agent
// retries it in the background. Only an unreadable or invalid list is an
// error.
func replayPackages(ctx context.Context, cfg Config, run Runner, r *Readiness) error {
	pkgs, err := listedPackages(cfg)
	if err != nil || len(pkgs) == 0 {
		return err
	}
	installMissing(ctx, run, r, pkgs)
	return nil
}

// RetryPackages installs the listed packages the box still lacks (the
// background retry, or Retry in the inspector) and records what still fails.
func RetryPackages(ctx context.Context, cfg Config, run Runner, r *Readiness) {
	pkgs, err := listedPackages(cfg)
	if err != nil {
		r.SetPackages(nil, "")
		return
	}
	installMissing(ctx, run, r, pkgs)
}

// retryDelay is the wait before background retry n (from 0): 1, 5, 15 min,
// then hourly.
func retryDelay(n int) time.Duration {
	switch n {
	case 0:
		return time.Minute
	case 1:
		return 5 * time.Minute
	case 2:
		return 15 * time.Minute
	}
	return time.Hour
}

func listedPackages(cfg Config) ([]string, error) {
	path := filepath.Join(cfg.Home(), ".portenv", "apt-packages.txt")
	f, err := os.Open(path) // #nosec G304 -- fixed path inside the box's home.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return ParsePackages(f)
}

// installMissing installs the packages not installed yet. When the batch
// fails, each package is tried on its own, so one bad name doesn't keep the
// others out; whatever still fails is recorded in r.
func installMissing(ctx context.Context, run Runner, r *Readiness, pkgs []string) {
	var missing []string
	for _, p := range pkgs {
		out, err := run.Run(ctx, nil, "dpkg-query", "-W", "-f=${db:Status-Status}", packageName(p))
		if err != nil || strings.TrimSpace(string(out)) != "installed" {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		r.SetPackages(nil, "")
		return
	}
	env := []string{"DEBIAN_FRONTEND=noninteractive"}
	r.Step("updating package lists")
	if out, err := run.Run(ctx, env, "apt-get", "update"); err != nil {
		r.SetPackages(missing, cmdError("apt-get update", out, err).Error())
		return
	}
	r.Step(fmt.Sprintf("installing %d packages: %s", len(missing), strings.Join(missing, " ")))
	install := func(p ...string) ([]byte, error) {
		return run.Run(ctx, env, "apt-get", append([]string{"install", "-y", "--no-install-recommends", "--"}, p...)...)
	}
	if _, err := install(missing...); err == nil {
		r.SetPackages(nil, "")
		return
	}
	var failed []string
	var last string
	for _, p := range missing {
		if out, err := install(p); err != nil {
			failed = append(failed, p)
			last = cmdError("install "+p, out, err).Error()
		}
	}
	r.SetPackages(failed, last)
}

// cmdError wraps a failed command with the last lines of its output.
func cmdError(what string, out []byte, err error) error {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	if tail := strings.TrimSpace(strings.Join(lines, "\n")); tail != "" {
		return fmt.Errorf("%s: %w: %s", what, err, tail)
	}
	return fmt.Errorf("%s: %w", what, err)
}
