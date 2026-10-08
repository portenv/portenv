// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/internal/bounded"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
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
	// REST, when set, reaches restic's REST server on the storage server
	// through a forward over the reused SSH connection, instead of SFTP
	// (far fewer round trips for listing and lease tags).
	REST *RESTForward
}

// BinaryID identifies the restic binary this executor runs without running
// it (path, size and modification time): a version check holds until it
// changes.
func (l LocalExecutor) BinaryID() (string, error) {
	p, err := exec.LookPath(l.Bin)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %d %d", p, fi.Size(), fi.ModTime().UnixNano()), nil
}

// RESTForward is restic's REST server on the storage server's loopback,
// forwarded to a unix socket in ControlDir over the reused SSH connection.
// No inbound port: the server listens on 127.0.0.1 only.
type RESTForward struct {
	Via      string // user@host of the SSH connection
	Port     string // its SSH port; empty means 22
	Remote   string // the server's REST address, 127.0.0.1:PORT
	User     string
	Password string
}

// Socket is the local end of the forward.
func (f *RESTForward) Socket(controlDir string) string {
	sum := sha256.Sum256([]byte(f.Via + " " + f.Port + " " + f.Remote))
	return filepath.Join(controlDir, "rest-"+hex.EncodeToString(sum[:6])+".sock")
}

// Restic implements Executor. The password reaches restic through an
// inherited pipe (RESTIC_PASSWORD_FILE=/dev/fd/3): never on disk, never in
// the environment. Inherited RESTIC_* variables are dropped.
func (l LocalExecutor) Restic(ctx context.Context, args []string, cred Credentials) (ExecResult, error) {
	res, err := l.restic(ctx, args, cred, false)
	// A run that failed on the REST forward's socket (its SSH connection
	// went away) gets a fresh forward and one more try.
	if err == nil && res.ExitCode != 0 && l.REST != nil && bytes.Contains(res.Stderr, []byte(l.REST.Socket(l.ControlDir))) {
		res, err = l.restic(ctx, args, cred, true)
	}
	return res, err
}

func (l LocalExecutor) restic(ctx context.Context, args []string, cred Credentials, resetForward bool) (ExecResult, error) {
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
		if l.REST != nil {
			if err := l.REST.ensureFresh(ctx, l.ControlDir, opts, henv, resetForward); err != nil {
				return ExecResult{}, fmt.Errorf("REST forward: %w", err)
			}
			extraEnv = append(extraEnv, "RESTIC_REST_USERNAME="+l.REST.User, "RESTIC_REST_PASSWORD="+l.REST.Password)
		}
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
	// The real deadline comes with ctx (restic.runWithin); a caller without
	// one gets the short deadline. Stopped with SIGINT first, so restic
	// releases its lock, then SIGKILL after the grace period.
	cmd, err := bounded.Command(ctx, bounded.Limit(ctx, DefaultDeadlines.Short), l.Bin, args...)
	if err != nil {
		return ExecResult{}, err
	}
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
	input, err := agentInput(cred)
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

// agentInput is the JSON portenv-agent restic reads: the password and
// storage credentials. The secrets are meant to be in it: it goes to the
// agent on a pipe or the agent channel and is never written anywhere.
func agentInput(cred Credentials) ([]byte, error) {
	return json.Marshal(struct { // #nosec G117 -- deliberate, see above
		Password   string   `json:"password"`
		Env        []string `json:"env,omitempty"`
		SSHKey     string   `json:"ssh_key,omitempty"`
		SSHHostKey string   `json:"ssh_host_key,omitempty"`
	}{string(cred.Password), cred.Env, string(cred.SSHKey), cred.SSHHostKey})
}

// ChannelExecutor runs restic and path checks inside a box over the agent
// channel (ADR 0010): what portenvd uses from Phase 1, never docker exec.
type ChannelExecutor struct {
	Agent agentv1.AgentServiceClient
	// Client, when set, returns the client to use instead of Agent: the
	// box's current channel, which changes when the box restarts.
	Client func() agentv1.AgentServiceClient
}

// errNoChannel: the box has no agent connection (it was just closed or
// is restarting).
var errNoChannel = status.Error(codes.Unavailable, "the box agent is unavailable (no channel)")

func (c ChannelExecutor) agent() (agentv1.AgentServiceClient, error) {
	a := c.Agent
	if c.Client != nil {
		a = c.Client()
	}
	if a == nil {
		return nil, errNoChannel
	}
	return a, nil
}

// Restic implements Executor.
func (c ChannelExecutor) Restic(ctx context.Context, args []string, cred Credentials) (ExecResult, error) {
	input, err := agentInput(cred)
	if err != nil {
		return ExecResult{}, err
	}
	a, err := c.agent()
	if err != nil {
		return ExecResult{}, err
	}
	res, err := a.RunRestic(ctx, &agentv1.RunResticRequest{Args: args, Input: input})
	if err != nil {
		return ExecResult{}, fmt.Errorf("run restic in box: %w", err)
	}
	return ExecResult{Stdout: res.GetStdout(), Stderr: res.GetStderr(), ExitCode: int(res.GetExitCode())}, nil
}

// PathInfo implements Executor.
func (c ChannelExecutor) PathInfo(ctx context.Context, path string) (PathInfo, error) {
	a, err := c.agent()
	if err != nil {
		return PathInfo{}, err
	}
	res, err := a.GetPathInfo(ctx, &agentv1.GetPathInfoRequest{Path: path})
	if err != nil {
		return PathInfo{}, fmt.Errorf("inspect %s in box: %w", path, err)
	}
	return PathInfo{Exists: res.GetExists(), IsDir: res.GetIsDir(), Empty: res.GetEmpty()}, nil
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

// ensureFresh makes sure the forward's SSH connection (the master restic's
// SFTP runs also reuse, same ControlPath) and its socket are up; with reset
// it first drops them (after a run failed on them).
func (f *RESTForward) ensureFresh(ctx context.Context, controlDir string, opts, env []string, reset bool) error {
	sock := f.Socket(controlDir)
	// opts is ["-o", "sftp.args=<ssh options>"]; reuse those options.
	var base []string
	for _, a := range strings.Fields(strings.TrimPrefix(opts[1], "sftp.args=")) {
		if a == "ClearAllForwardings=yes" {
			base = base[:len(base)-1] // drop its "-o" too
			continue
		}
		base = append(base, a)
	}
	if f.Port != "" {
		base = append(base, "-p", f.Port)
	}
	ssh := func(extra ...string) error {
		cmd, err := bounded.Command(ctx, sshSetupLimit, "ssh", append(append(slices.Clone(base), extra...), f.Via)...)
		if err != nil {
			return err
		}
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ssh %s: %w: %s", extra[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if reset {
		_ = ssh("-O", "exit")
	} else if ssh("-O", "check") == nil {
		// The connection is up; the forward too if the socket answers.
		if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
			_ = c.Close()
			return nil
		}
	}
	// Nothing answers: the socket is missing or left by a connection that
	// has gone. The master binds the socket, so it needs the unlink option.
	// A forward is not a client of the master, so the master's idle limit
	// would end it under a socket in use: it stays up for hours, and
	// keepalives end it promptly if the network drops it instead.
	_ = os.Remove(sock)
	if ssh("-O", "check") != nil {
		if err := ssh("-M", "-N", "-f", "-o", "StreamLocalBindUnlink=yes", "-o", "ControlPersist=4h",
			"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4"); err != nil {
			return err
		}
	}
	return ssh("-O", "forward", "-L", sock+":"+f.Remote)
}
