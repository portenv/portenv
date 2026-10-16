// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// agentAs reaches an agent's own tmux server as the agent: each call
// re-runs this binary as "portenv-agent as-agent", which becomes the
// agent's user (RunAsAgent) and then runs tmux on the agent's socket. Go
// can't set no_new_privs or clear ambient capabilities for a child, so the
// switch happens in that process, before tmux runs.
func agentAs(spec AgentServerSpec) (tmuxFunc, tmuxCmdFunc, error) {
	if _, err := checkAsAgent(spec.UID, spec.GID, spec.Home); err != nil {
		return nil, nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	base := []string{"as-agent", fmt.Sprint(spec.UID), fmt.Sprint(spec.GID), spec.Home, "--",
		"tmux", "-S", agentSocket(spec)}
	command := func(ctx context.Context, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, self, append(append([]string{}, base...), args...)...) // #nosec G204 -- this binary; argv escaped for tmux
		cmd.Dir = "/"
		cmd.Env = withUTF8(spec.Env)
		return cmd
	}
	run := func(ctx context.Context, args ...string) (string, error) {
		cmd := command(ctx, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}
	return run, command, nil
}

// RunAsAgent is "portenv-agent as-agent UID GID HOME -- tmux ARGS…": it
// becomes the agent (groups: its own and the team's; no capabilities;
// no_new_privs) and runs tmux. It returns only on failure.
func RunAsAgent(args []string) error {
	uid, gid, home, argv, err := parseAsAgent(args)
	if err != nil {
		return err
	}
	// Capabilities are per thread: the thread that drops them runs exec.
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	if err := syscall.Setgroups([]int{gid, TeamGID}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	// Nothing is left to keep: the permitted, effective and inheritable
	// sets go too (exec would drop them for tmux, which has no file
	// capabilities; this doesn't rely on it).
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var none [2]unix.CapUserData
	if err := unix.Capset(&hdr, &none[0]); err != nil {
		return fmt.Errorf("drop capabilities: %w", err)
	}
	if os.Getuid() != uid || os.Geteuid() != uid || os.Getgid() != gid || os.Getegid() != gid {
		return fmt.Errorf("still uid %d/%d gid %d/%d", os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid())
	}
	if err := os.Chdir(home); err != nil {
		return err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ()) // #nosec G204 -- tmux, checked in parseAsAgent
}
