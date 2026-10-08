// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// RunRestic runs restic as portenv-sync with the password on an inherited
// pipe, passing restic's output through, and returns restic's exit code.
// The calling process makes itself non-dumpable before reading the password,
// so other processes in the box cannot read it from this process either.
func RunRestic(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return 0, fmt.Errorf("make agent non-dumpable: %w", err)
	}
	args, in, err := parseResticRun(args, stdin)
	if err != nil {
		return 0, err
	}
	if err := CheckSwitch(syncUID, ForRestic); err != nil {
		return 0, err
	}

	bin, err := os.Open(ResticBinary)
	if err != nil {
		return 0, fmt.Errorf("open restic: %w", err)
	}
	defer func() { _ = bin.Close() }()
	if ResticSHA256 != "" {
		h := sha256.New()
		if _, err := io.Copy(h, bin); err != nil {
			return 0, fmt.Errorf("hash restic: %w", err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != ResticSHA256 {
			return 0, fmt.Errorf("restic binary has SHA-256 %s, want %s: refusing to run it", got, ResticSHA256)
		}
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer func() { _ = pr.Close() }()
	if _, err := pw.WriteString(in.Password); err != nil {
		_ = pw.Close()
		return 0, err
	}
	_ = pw.Close()

	var extraEnv []string
	if in.SSHKey != "" {
		sa, opts, env, err := startSFTPAgent(in.SSHKey, in.SSHHostKey)
		if err != nil {
			return 0, err
		}
		defer sa.stop()
		args = append(opts, args...)
		extraEnv = env
	}

	// Execute the file just hashed (fd 4 in the child), not whatever the
	// path names by the time exec happens.
	cmd := exec.CommandContext(ctx, "/proc/self/fd/4", args...) // #nosec G204 -- validated subcommand and flags
	cmd.Args[0] = "restic"
	// Stopped at the caller's deadline (the gRPC call's): SIGINT first, so
	// restic releases its lock and cleans up; SIGKILL only if it hasn't
	// exited after the grace period (cap_kill: restic runs as portenv-sync).
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = resticKillGrace
	cmd.ExtraFiles = []*os.File{pr, bin}
	cmd.Env = append([]string{
		"PATH=/usr/bin:/bin", "HOME=/nonexistent",
		"RESTIC_CACHE_DIR=" + resticCacheDir,
		"RESTIC_PASSWORD_FILE=/dev/fd/3",
	}, append(in.Env, extraEnv...)...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: syncUID, Gid: syncGID, Groups: []uint32{}},
	}
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 0, fmt.Errorf("run restic: %w", err)
	}
	return 0, nil
}
