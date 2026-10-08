// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ResticBinary is restic inside the box: owned by root:portenv-sync, mode
// 0750, with file capabilities (ADR 0005).
const ResticBinary = "/usr/local/libexec/portenv/restic"

// ResticSHA256 is the expected SHA-256 of ResticBinary, set at image build
// with -ldflags. Empty skips the check (development builds only).
var ResticSHA256 = ""

const (
	syncUID        = 990
	syncGID        = 990
	resticCacheDir = "/var/cache/portenv-sync"
	maxSecretInput = 64 << 10
)

// ResticInput is what the caller sends on stdin for one restic run. It never
// appears in argv or on disk.
type ResticInput struct {
	Password string   `json:"password"`
	Env      []string `json:"env,omitempty"` // storage credentials
	// SFTP storage: the private key (OpenSSH PEM) the agent serves to
	// restic's ssh from memory for this run, and the storage server's host
	// key ("ssh-ed25519 AAAA..."), which is pinned.
	SSHKey     string `json:"ssh_key,omitempty"`
	SSHHostKey string `json:"ssh_host_key,omitempty"`
}

// resticCommands are the restic subcommands the agent runs. Key management
// and anything that could reveal or change the password are excluded.
var resticCommands = []string{
	"backup", "cat", "check", "forget", "init", "prune", "restore",
	"snapshots", "tag", "unlock", "version",
}

// forbiddenFlags would let a caller replace the password source or skip
// encryption.
var forbiddenFlags = []string{
	"--password-command", "--password-file", "-p", "--insecure-no-password",
	"--cache-dir", "--no-cache", "--option", "-o",
}

// envPrefixes are the storage credentials a caller may pass.
var envPrefixes = []string{"AWS_", "B2_", "AZURE_", "GOOGLE_", "OS_", "RESTIC_REST_USERNAME=", "RESTIC_REST_PASSWORD="}

// parseResticRun validates a restic run: argv after "portenv-agent restic"
// and the JSON input from stdin.
func parseResticRun(args []string, stdin io.Reader) ([]string, ResticInput, error) {
	var in ResticInput
	b, err := io.ReadAll(io.LimitReader(stdin, maxSecretInput+1))
	if err != nil {
		return nil, in, fmt.Errorf("read input: %w", err)
	}
	if len(b) > maxSecretInput {
		return nil, in, errors.New("input too large")
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &in); err != nil {
			return nil, in, fmt.Errorf("parse input: %w", err)
		}
	}
	cmd := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, _ := strings.Cut(a, "=")
		if slices.Contains(forbiddenFlags, name) {
			return nil, in, fmt.Errorf("flag %s is not allowed", name)
		}
		if a == "--repo" || a == "-r" {
			i++ // its value
			continue
		}
		if cmd == "" && !strings.HasPrefix(a, "-") {
			cmd = a
		}
	}
	if !slices.Contains(resticCommands, cmd) {
		return nil, in, fmt.Errorf("restic command %q is not allowed", cmd)
	}
	if cmd != "version" && in.Password == "" {
		return nil, in, errors.New("no repository password given")
	}
	if (in.SSHKey == "") != (in.SSHHostKey == "") {
		return nil, in, errors.New("an SFTP key needs the storage server's host key, and the other way round")
	}
	for _, kv := range in.Env {
		if !slices.ContainsFunc(envPrefixes, func(p string) bool { return strings.HasPrefix(kv, p) }) {
			k, _, _ := strings.Cut(kv, "=")
			return nil, in, fmt.Errorf("environment variable %s is not allowed", k)
		}
	}
	return args, in, nil
}

// PathInfo describes a path under /home for the sync engine: whether it
// exists, is a directory, and is an empty one. Paths outside /home are
// refused.
func PathInfo(path string) (map[string]bool, error) {
	clean := filepath.Clean(path)
	if clean != "/home" && !strings.HasPrefix(clean, "/home/") {
		return nil, fmt.Errorf("path-info only answers for /home, not %s", path)
	}
	info := map[string]bool{"exists": false, "is_dir": false, "empty": false}
	fi, err := os.Stat(clean)
	if errors.Is(err, os.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return nil, err
	}
	info["exists"], info["is_dir"] = true, fi.IsDir()
	if fi.IsDir() {
		entries, err := os.ReadDir(clean)
		if err != nil {
			return nil, err
		}
		info["empty"] = len(entries) == 0
	}
	return info, nil
}
