// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// runAgent is an SSH agent of portenvd's own for one ssh run (ADR 0021): it
// serves one server's made key on one socket in the checked folder
// (<T>pv/a-<8 hex>, random per run), signs for that key only, refuses to
// add, remove or lock keys, and is closed as soon as the shared connection
// it signed in is up. Nothing a workspace mounts or sees is there.
type runAgent struct {
	sock   string
	l      net.Listener
	signer ssh.Signer
}

var errRefused = errors.New("refused: this agent serves one key for one run")

func startRunAgent(signer ssh.Signer) (*runAgent, error) {
	dir, err := SocketDir()
	if err != nil {
		return nil, err
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "a-"+hex.EncodeToString(b))
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	// The key's public half beside it, for IdentityFile (madeKeyOptions).
	if err := os.WriteFile(sock+".pub", ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		_ = l.Close()
		_ = os.Remove(sock)
		return nil, err
	}
	a := &runAgent{sock: sock, l: l, signer: signer}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = agent.ServeAgent(a, c)
			}()
		}
	}()
	return a, nil
}

// Close stops serving and removes the socket.
func (a *runAgent) Close() error {
	err := a.l.Close()
	for _, f := range []string{a.sock, a.sock + ".pub"} {
		if rerr := os.Remove(f); rerr != nil && !os.IsNotExist(rerr) && err == nil {
			err = rerr
		}
	}
	return err
}

func (a *runAgent) mine(key ssh.PublicKey) bool {
	return bytes.Equal(key.Marshal(), a.signer.PublicKey().Marshal())
}

func (a *runAgent) List() ([]*agent.Key, error) {
	pub := a.signer.PublicKey()
	return []*agent.Key{{Format: pub.Type(), Blob: pub.Marshal(), Comment: ""}}, nil
}

func (a *runAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return a.SignWithFlags(key, data, 0)
}

func (a *runAgent) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if !a.mine(key) {
		return nil, errRefused
	}
	if flags != 0 { // RSA's SHA-2 flags; a made key is Ed25519
		if as, ok := a.signer.(ssh.AlgorithmSigner); ok {
			alg := ssh.KeyAlgoRSASHA256
			if flags&agent.SignatureFlagRsaSha512 != 0 {
				alg = ssh.KeyAlgoRSASHA512
			}
			return as.SignWithAlgorithm(rand.Reader, data, alg)
		}
	}
	return a.signer.Sign(rand.Reader, data)
}

func (a *runAgent) Signers() ([]ssh.Signer, error) { return []ssh.Signer{a.signer}, nil }

func (a *runAgent) Add(agent.AddedKey) error   { return errRefused }
func (a *runAgent) Remove(ssh.PublicKey) error { return errRefused }
func (a *runAgent) RemoveAll() error           { return errRefused }
func (a *runAgent) Lock([]byte) error          { return errRefused }
func (a *runAgent) Unlock([]byte) error        { return errRefused }
func (a *runAgent) Extension(string, []byte) ([]byte, error) {
	return nil, agent.ErrExtensionUnsupported
}
