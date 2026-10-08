// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/portenv/portenv/core/internal/bounded"
)

// PortenvPath is portenv on servers: the only command the SSH user may run
// with sudo (server/setup.sh), so it is always called by this full path.
const PortenvPath = "/usr/local/bin/portenv"

// sshBase is the ssh command (PORTENV_SSH when set) with options that reuse
// one connection per server (ControlMaster in the private control
// directory).
func sshBase() []string {
	sshCmd := []string{"ssh"}
	if v := os.Getenv("PORTENV_SSH"); v != "" {
		sshCmd = strings.Fields(v)
	}
	if err := os.MkdirAll(ControlDir(), 0o700); err == nil {
		sshCmd = append(sshCmd, "-o", "ControlMaster=auto", "-o", "ControlPath="+filepath.Join(ControlDir(), "cm-%C"), "-o", "ControlPersist=600")
	}
	return sshCmd
}

// Limits for commands run on a server over SSH (ADR 0012). The server runs
// restic under its own deadlines; these are the Mac's backstop.
const (
	// QuickRemote: queries (state, channel, check, ping, status).
	QuickRemote = 60 * time.Second
	// LongRemote: open, close, save point, revert, restart and enrolment,
	// which save or restore the home on the server.
	LongRemote = 30 * time.Minute
	// sshSetup: connection and forward setup.
	sshSetup = 30 * time.Second
)

// Remote is a command on an SSH host, with PORTENV_SSH as the ssh command
// when set, stopped after limit. The host comes after "--", so it can never
// be read as an option.
func Remote(ctx context.Context, limit time.Duration, host string, argv ...string) (*bounded.Cmd, error) {
	sshCmd := sshBase()
	full := append(append(sshCmd[1:], "--", host), argv...)
	return bounded.Command(ctx, limit, sshCmd[0], full...)
}

// appLimit is the limit for `portenv app ACTION` on a server.
func appLimit(action string) time.Duration {
	switch action {
	case "state", "channel", "check", "ping", "servers":
		return QuickRemote
	}
	return LongRemote
}

// AppOn runs `portenv app ARGS` on host through its runner (sudo, by full
// path) and returns its standard output, or its last error line.
func AppOn(ctx context.Context, host string, args ...string) (string, error) {
	action := ""
	if len(args) > 0 {
		action = args[0]
	}
	cmd, err := Remote(ctx, appLimit(action), host, append([]string{"sudo", PortenvPath, "app"}, args...)...)
	if err != nil {
		return "", err
	}
	cmd.Stdin = nil
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(errb.String()), "\n")
		msg := strings.TrimPrefix(lines[len(lines)-1], "portenv: ")
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("on %s: %s", host, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// ForwardLocal makes a unix socket in the private control directory that
// reaches remoteAddr (127.0.0.1:PORT on host) through the reused SSH
// connection, and returns its path. No port is opened on either side.
func ForwardLocal(ctx context.Context, host, remoteAddr string) (string, error) {
	sum := sha256.Sum256([]byte(host + " " + remoteAddr))
	sock := filepath.Join(ControlDir(), "fwd-"+hex.EncodeToString(sum[:6])+".sock")
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		_ = c.Close()
		return sock, nil
	}
	_ = os.Remove(sock)
	base := sshBase()
	run := func(extra ...string) error {
		cmd, err := bounded.Command(ctx, sshSetup, base[0], append(append(slices.Clone(base[1:]), extra...), "--", host)...)
		if err != nil {
			return err
		}
		cmd.Stdin = nil
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ssh %s: %w: %s", extra[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if run("-O", "check") != nil {
		if err := run("-M", "-N", "-f", "-o", "StreamLocalBindUnlink=yes"); err != nil {
			return "", err
		}
	}
	if err := run("-O", "forward", "-L", sock+":"+remoteAddr); err != nil {
		return "", err
	}
	return sock, nil
}

// RunRemote runs a non-interactive command on host, writing its output to
// out. It never forwards stdin: an ssh that inherits an open stdin can wait
// forever after the remote command has finished.
func RunRemote(ctx context.Context, limit time.Duration, host string, out io.Writer, argv ...string) error {
	cmd, err := Remote(ctx, limit, host, argv...)
	if err != nil {
		return err
	}
	cmd.Stdin = nil // /dev/null
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// JoinKeys is what a move sends to portenv join on the target, on SSH's
// stdin: never in argv, never on disk.
type JoinKeys struct {
	RepositoryKey string `json:"repository_key"`
	StorageKey    string `json:"storage_key,omitempty"`   // SFTP storage, OpenSSH PEM
	RESTPassword  string `json:"rest_password,omitempty"` // REST storage (StorageREST)
}

// EnrolOn enrols the box on host, which reaches its storage at
// joinStorage: the host adds its own repository key (portenv join).
func EnrolOn(ctx context.Context, e *Env, c BoxConfig, host, joinStorage string, out io.Writer) error {
	key, err := e.KeyStore().Get(c.ID)
	if err != nil {
		return err
	}
	keys := JoinKeys{RepositoryKey: string(key)}
	joinArgs := []string{"sudo", PortenvPath, "join", c.Name, "--id", c.ID, "--image", c.Image, "--storage", joinStorage}
	if strings.HasPrefix(joinStorage, "sftp:") {
		sk, err := e.KeyStore().Get(StorageKeyID(c.ID))
		if err != nil {
			return fmt.Errorf("storage key: %w", err)
		}
		keys.StorageKey = string(sk)
		joinArgs = append(joinArgs, "--storage-host-key", "'"+c.StorageHostKey+"'")
		if c.StorageREST != "" {
			rp, err := e.KeyStore().Get(RESTKeyID(c.ID))
			if err != nil {
				return fmt.Errorf("REST storage password: %w", err)
			}
			keys.RESTPassword = string(rp)
			joinArgs = append(joinArgs, "--storage-rest", c.StorageREST)
		}
	}
	payload, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	join, err := Remote(ctx, LongRemote, host, joinArgs...)
	if err != nil {
		return err
	}
	join.Stdin = bytes.NewReader(payload)
	join.Stdout, join.Stderr = out, out
	if err := join.Run(); err != nil {
		return fmt.Errorf("enrol on %s: %w (the box is saved and released)", host, err)
	}
	return nil
}

// ResumeOn opens the box on host with the Phase 0 CLI (portenv resume
// there; the gate's path).
func ResumeOn(ctx context.Context, host, name string, out io.Writer) error {
	if err := RunRemote(ctx, LongRemote, host, out, "sudo", PortenvPath, "resume", name); err != nil {
		return fmt.Errorf("resume on %s: %w (the box is saved and released; resume it anywhere)", host, err)
	}
	return nil
}

// CloseOn saves, releases and stops the box on host (portenv close there).
func CloseOn(ctx context.Context, host, name string, out io.Writer) error {
	if err := RunRemote(ctx, LongRemote, host, out, "sudo", PortenvPath, "close", name); err != nil {
		return fmt.Errorf("close on %s: %w", host, err)
	}
	return nil
}

// KnownOn reports whether host has the box enrolled.
func KnownOn(ctx context.Context, host, name string) bool {
	return RunRemote(ctx, QuickRemote, host, io.Discard, "sudo", PortenvPath, "status", name) == nil
}
