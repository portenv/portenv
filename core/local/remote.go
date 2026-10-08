// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Remote is a command on an SSH host, with PORTENV_SSH as the ssh command
// when set. The host comes after "--", so it can never be read as an option.
func Remote(ctx context.Context, host string, argv ...string) *exec.Cmd {
	sshCmd := []string{"ssh"}
	if v := os.Getenv("PORTENV_SSH"); v != "" {
		sshCmd = strings.Fields(v)
	}
	full := append(append(sshCmd[1:], "--", host), argv...)
	return exec.CommandContext(ctx, sshCmd[0], full...) // #nosec G204 G702 -- the user's own SSH host, after -- so it cannot be an option
}

// RunRemote runs a non-interactive command on host, writing its output to
// out. It never forwards stdin: an ssh that inherits an open stdin can wait
// forever after the remote command has finished.
func RunRemote(ctx context.Context, host string, out io.Writer, argv ...string) error {
	cmd := Remote(ctx, host, argv...)
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
	joinArgs := []string{"sudo", "portenv", "join", c.Name, "--id", c.ID, "--image", c.Image, "--storage", joinStorage}
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
	join := Remote(ctx, host, joinArgs...)
	join.Stdin = bytes.NewReader(payload)
	join.Stdout, join.Stderr = out, out
	if err := join.Run(); err != nil {
		return fmt.Errorf("enrol on %s: %w (the box is saved and released)", host, err)
	}
	return nil
}

// ResumeOn opens the box on host (portenv resume there).
func ResumeOn(ctx context.Context, host, name string, out io.Writer) error {
	if err := RunRemote(ctx, host, out, "sudo", "portenv", "resume", name); err != nil {
		return fmt.Errorf("resume on %s: %w (the box is saved and released; resume it anywhere)", host, err)
	}
	return nil
}

// CloseOn saves, releases and stops the box on host (portenv close there).
func CloseOn(ctx context.Context, host, name string, out io.Writer) error {
	if err := RunRemote(ctx, host, out, "sudo", "portenv", "close", name); err != nil {
		return fmt.Errorf("close on %s: %w", host, err)
	}
	return nil
}

// KnownOn reports whether host has the box enrolled.
func KnownOn(ctx context.Context, host, name string) bool {
	return RunRemote(ctx, host, io.Discard, "sudo", "portenv", "status", name) == nil
}
