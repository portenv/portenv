// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	sshagent "golang.org/x/crypto/ssh/agent"
)

// sftpAgent serves an SFTP storage key to restic's ssh from memory for one
// restic run: an SSH agent on a socket in a private directory owned by
// portenv-sync. The key never touches disk; root in the box could use the
// socket during the run but cannot copy the key (this process is
// non-dumpable). The storage server's host key is pinned.
type sftpAgent struct {
	dir string
	lis net.Listener
}

// startSFTPAgent starts the agent and returns restic options and
// environment that make restic's ssh use it.
func startSFTPAgent(privateKey, hostKey string) (*sftpAgent, []string, []string, error) {
	raw, err := ssh.ParseRawPrivateKey([]byte(privateKey))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse SFTP key: %w", err)
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, nil, nil, fmt.Errorf("parse storage host key: %v", err)
	}
	keyring := sshagent.NewKeyring()
	if err := keyring.Add(sshagent.AddedKey{PrivateKey: raw}); err != nil {
		return nil, nil, nil, err
	}

	if err := os.MkdirAll("/run/portenv", 0o700); err != nil {
		return nil, nil, nil, err
	}
	dir, err := os.MkdirTemp("/run/portenv", "sftp-")
	if err != nil {
		return nil, nil, nil, err
	}
	a := &sftpAgent{dir: dir}
	fail := func(err error) (*sftpAgent, []string, []string, error) { a.stop(); return nil, nil, nil, err }
	if err := os.Chown(dir, syncUID, syncGID); err != nil {
		return fail(err)
	}
	// The host key is public; pinning it under a fixed alias keeps the
	// check independent of the server's current address.
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte("portenv-storage "+string(ssh.MarshalAuthorizedKey(pub))), 0o644); err != nil { // #nosec G306 -- a public host key
		return fail(err)
	}
	sock := filepath.Join(dir, "agent.sock")
	a.lis, err = net.Listen("unix", sock)
	if err != nil {
		return fail(err)
	}
	if err := os.Chown(sock, syncUID, syncGID); err != nil {
		return fail(err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return fail(err)
	}
	go func() {
		for {
			c, err := a.lis.Accept()
			if err != nil {
				return
			}
			go func() { _ = sshagent.ServeAgent(keyring, c); _ = c.Close() }()
		}
	}()
	opts := []string{"-o", "sftp.args=-F /dev/null -o BatchMode=yes -o IdentitiesOnly=no -o HostKeyAlias=portenv-storage" +
		" -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + known +
		" -o ForwardAgent=no -o ClearAllForwardings=yes"}
	return a, opts, []string{"SSH_AUTH_SOCK=" + sock}, nil
}

func (a *sftpAgent) stop() {
	if a.lis != nil {
		_ = a.lis.Close()
	}
	_ = os.RemoveAll(a.dir)
}
