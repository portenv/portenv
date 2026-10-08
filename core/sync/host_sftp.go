// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
	sshagent "golang.org/x/crypto/ssh/agent"
)

// hostSFTP lets restic on the host reach SFTP storage: the storage key is
// served from memory by an SSH agent for one restic run, the server's host
// key is pinned, and one SSH connection is reused across runs
// (ControlMaster). All of it lives in a private directory on the host, which
// nothing inside a box can reach (ADR 0005).
type hostSFTP struct {
	lis  net.Listener
	sock string
}

// privateDir checks that dir is a real directory owned by this user with
// mode 0700, creating it if needed.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o700 || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s must be a directory owned by you with mode 0700", dir)
	}
	return nil
}

// startHostSFTP starts the agent and returns restic options and environment.
func startHostSFTP(controlDir string, privateKey []byte, hostKey string) (*hostSFTP, []string, []string, error) {
	if controlDir == "" {
		return nil, nil, nil, errors.New("SFTP storage on the host needs a control directory")
	}
	if err := privateDir(controlDir); err != nil {
		return nil, nil, nil, err
	}
	raw, err := ssh.ParseRawPrivateKey(privateKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse SFTP key: %w", err)
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse storage host key: %w", err)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, nil, nil, errors.New("storage host key: want exactly one key")
	}
	keyring := sshagent.NewKeyring()
	if err := keyring.Add(sshagent.AddedKey{PrivateKey: raw}); err != nil {
		return nil, nil, nil, err
	}
	known := filepath.Join(controlDir, "known_hosts")
	if err := os.WriteFile(known, []byte("portenv-storage "+string(ssh.MarshalAuthorizedKey(pub))), 0o600); err != nil {
		return nil, nil, nil, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	h := &hostSFTP{sock: filepath.Join(controlDir, "agent-"+hex.EncodeToString(b))}
	if h.lis, err = net.Listen("unix", h.sock); err != nil {
		return nil, nil, nil, err
	}
	go func() {
		for {
			c, err := h.lis.Accept()
			if err != nil {
				return
			}
			go func() { _ = sshagent.ServeAgent(keyring, c); _ = c.Close() }()
		}
	}()
	opts := []string{"-o", "sftp.args=-F /dev/null -o BatchMode=yes -o IdentitiesOnly=no -o HostKeyAlias=portenv-storage" +
		" -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + known +
		" -o ControlMaster=auto -o ControlPath=" + filepath.Join(controlDir, "cm-%C") + " -o ControlPersist=600" +
		" -o ForwardAgent=no -o ClearAllForwardings=yes"}
	return h, opts, []string{"SSH_AUTH_SOCK=" + h.sock}, nil
}

func (h *hostSFTP) stop() {
	_ = h.lis.Close()
	_ = os.Remove(h.sock)
}
