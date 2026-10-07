// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/portenv/portenv/core/driver"
)

// Executor runs restic for a Box and inspects the home: locally (tests, and
// machines where the home is a plain directory) or inside the box through
// portenv-agent (ADR 0005).
type Executor interface {
	// Restic runs restic with args and the credentials, the password
	// through a pipe.
	Restic(ctx context.Context, args []string, cred Credentials) (ExecResult, error)
	// PathInfo describes a path where restic runs (the home, the excludes
	// file).
	PathInfo(ctx context.Context, path string) (PathInfo, error)
}

// PathInfo describes one path.
type PathInfo struct {
	Exists bool `json:"exists"`
	IsDir  bool `json:"is_dir"`
	Empty  bool `json:"empty"` // a directory with no entries
}

// Credentials are what one restic run needs besides its arguments.
type Credentials struct {
	Password []byte
	Env      []string // storage credentials, e.g. AWS_*
	// SFTP storage: a private key (OpenSSH PEM) and the storage server's
	// pinned host key ("ssh-ed25519 AAAA...").
	SSHKey     []byte
	SSHHostKey string
	// NewPassword, for key add only, reaches restic on fd 4.
	NewPassword []byte
}

// ExecResult is the outcome of one restic run.
type ExecResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// ExitError is a restic run that exited non-zero.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// LocalExecutor runs a restic binary on this machine: in tests, and on the
// host for everything that does not need a box's /home.
type LocalExecutor struct {
	Bin      string
	CacheDir string
	// ControlDir is a private directory (0700, this user) for SFTP storage:
	// the in-memory key agent's socket, the pinned host key and the reused
	// SSH connection.
	ControlDir string
}

// Restic implements Executor. The password reaches restic through an
// inherited pipe (RESTIC_PASSWORD_FILE=/dev/fd/3): never on disk, never in
// the environment. Inherited RESTIC_* variables are dropped.
func (l LocalExecutor) Restic(ctx context.Context, args []string, cred Credentials) (ExecResult, error) {
	password, env := cred.Password, cred.Env
	var extraEnv []string
	if len(cred.SSHKey) > 0 {
		h, opts, henv, err := startHostSFTP(l.ControlDir, cred.SSHKey, cred.SSHHostKey)
		if err != nil {
			return ExecResult{}, err
		}
		defer h.stop()
		args = append(opts, args...)
		extraEnv = henv
	}
	pr, err := passwordPipe(password)
	if err != nil {
		return ExecResult{}, err
	}
	defer func() { _ = pr.Close() }()
	files := []*os.File{pr}
	if cred.NewPassword != nil {
		npr, err := passwordPipe(cred.NewPassword)
		if err != nil {
			return ExecResult{}, err
		}
		defer func() { _ = npr.Close() }()
		files = append(files, npr)
	}

	if l.CacheDir != "" {
		args = append([]string{"--cache-dir", l.CacheDir}, args...)
	}
	cmd := exec.CommandContext(ctx, l.Bin, args...) // #nosec G204 -- the restic binary comes from configuration
	var procEnv []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "RESTIC_") {
			procEnv = append(procEnv, kv)
		}
	}
	cmd.Env = append(append(append(procEnv, env...), extraEnv...), "RESTIC_PASSWORD_FILE=/dev/fd/3")
	cmd.ExtraFiles = files
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return ExecResult{Stdout: out.Bytes(), Stderr: errb.Bytes(), ExitCode: exit.ExitCode()}, nil
	}
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{Stdout: out.Bytes(), Stderr: errb.Bytes()}, nil
}

// passwordPipe returns the read end of a pipe already holding secret.
func passwordPipe(secret []byte) (*os.File, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	if _, err := pw.Write(secret); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, err
	}
	_ = pw.Close()
	return pr, nil
}

// PathInfo implements Executor.
func (LocalExecutor) PathInfo(_ context.Context, path string) (PathInfo, error) {
	return LocalPathInfo(path)
}

// LocalPathInfo describes a path on this machine.
func LocalPathInfo(path string) (PathInfo, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return PathInfo{}, nil
	}
	if err != nil {
		return PathInfo{}, err
	}
	info := PathInfo{Exists: true, IsDir: fi.IsDir()}
	if info.IsDir {
		entries, err := os.ReadDir(path)
		if err != nil {
			return PathInfo{}, err
		}
		info.Empty = len(entries) == 0
	}
	return info, nil
}

// AgentExecutor runs restic inside a box through portenv-agent. In Phase 0
// the agent is reached with the driver's Exec; Phase 1 moves this onto the
// agent's own channel. The password travels on the command's stdin.
type AgentExecutor struct {
	Driver driver.Driver
	Box    driver.BoxID
}

// agentRefused is the agent's exit code when it refuses a run.
const agentRefused = 125

// Restic implements Executor.
func (a AgentExecutor) Restic(ctx context.Context, args []string, cred Credentials) (ExecResult, error) {
	// The secrets are meant to be in this JSON: it goes to the agent on
	// stdin and is never written anywhere.
	input, err := json.Marshal(struct { // #nosec G117 -- deliberate, see above
		Password   string   `json:"password"`
		Env        []string `json:"env,omitempty"`
		SSHKey     string   `json:"ssh_key,omitempty"`
		SSHHostKey string   `json:"ssh_host_key,omitempty"`
	}{string(cred.Password), cred.Env, string(cred.SSHKey), cred.SSHHostKey})
	if err != nil {
		return ExecResult{}, err
	}
	res, err := a.Driver.Exec(ctx, a.Box, driver.ExecRequest{
		Argv:  append([]string{"portenv-agent", "restic"}, args...),
		Stdin: input,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("run restic in box: %w", err)
	}
	if res.ExitCode == agentRefused {
		return ExecResult{}, fmt.Errorf("agent refused the restic run: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return ExecResult{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode}, nil
}

// PathInfo implements Executor.
func (a AgentExecutor) PathInfo(ctx context.Context, path string) (PathInfo, error) {
	res, err := a.Driver.Exec(ctx, a.Box, driver.ExecRequest{Argv: []string{"portenv-agent", "path-info", path}, Timeout: 30 * time.Second})
	if err != nil {
		return PathInfo{}, fmt.Errorf("inspect %s in box: %w", path, err)
	}
	var info PathInfo
	if res.ExitCode != 0 || json.Unmarshal(res.Stdout, &info) != nil {
		return PathInfo{}, fmt.Errorf("inspect %s in box: exit %d: %s", path, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return info, nil
}
