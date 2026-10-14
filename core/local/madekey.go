// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/portenv/portenv/core/internal/bounded"
)

// SignInMadeKey: the server signs in with a key Portenv made just for it
// (ADR 0021): its private half is in the app's Keychain and reaches
// portenvd in memory only (ProvideServerKey), never on disk or in a log.
const SignInMadeKey = "made-key"

// madeKeys are the made keys the app handed over, by server ID.
var madeKeys struct {
	sync.Mutex
	m map[string]ssh.Signer
}

// ProvideServerKey keeps a server's made key in memory (an Ed25519 private
// key, or its 32-byte seed).
func (e *Env) ProvideServerKey(id string, key []byte) error {
	var priv ed25519.PrivateKey
	switch len(key) {
	case ed25519.SeedSize:
		priv = ed25519.NewKeyFromSeed(key)
	case ed25519.PrivateKeySize:
		priv = ed25519.PrivateKey(slices.Clone(key))
	default:
		return fmt.Errorf("a server's key is an Ed25519 key, not %d bytes", len(key))
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}
	madeKeys.Lock()
	defer madeKeys.Unlock()
	if madeKeys.m == nil {
		madeKeys.m = map[string]ssh.Signer{}
	}
	madeKeys.m[id] = s
	return nil
}

// forgetMadeKeys drops every made key held (tests).
func forgetMadeKeys() {
	madeKeys.Lock()
	defer madeKeys.Unlock()
	madeKeys.m = nil
}

func madeKeySigner(id string) (ssh.Signer, bool) {
	madeKeys.Lock()
	defer madeKeys.Unlock()
	s, ok := madeKeys.m[id]
	return s, ok
}

// UseMadeKey marks the server as signing in with its made key.
func (e *Env) UseMadeKey(id string) error {
	_, err := e.updateServer(id, func(r *ServerRecord) error {
		r.SignIn = SignInMadeKey
		return nil
	})
	return err
}

// madeKeyOptions keep the person's ssh config (a Match exec, a
// ProxyCommand, another IdentityFile) out of a made-key server's runs, with
// every default that matters said explicitly (R-0180). With
// IdentitiesOnly, ssh uses an agent's key only when an IdentityFile names
// it, so a sign-in names the key's public half (<agent socket>.pub, never
// the private one); a run through the shared connection names none.
func madeKeyOptions(agentSock string) []string {
	identity := "none"
	if agentSock != "none" {
		identity = agentSock + ".pub"
	}
	return []string{
		"-o", "ProxyCommand=none",
		"-o", "KnownHostsCommand=none",
		"-o", "PermitLocalCommand=no",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityFile=" + identity,
		"-o", "IdentityAgent=" + agentSock,
		"-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes",
	}
}

// madeKeyArgs is the ssh command for a made-key server: the binary only
// from PORTENV_SSH (its options, a later -F among them, would win over
// -F none), -F none, the pins, the options above and the shared
// connection's. agentSock is the run's agent, or "none" for a run that
// goes through the shared connection.
func madeKeyArgs(e *Env, host, agentSock string) ([]string, error) {
	r, err := e.ServerRecord(host)
	if err != nil {
		return nil, err
	}
	args := append([]string{sshBase()[0], "-F", "none"}, e.pinOptions(r)...)
	args = append(args, madeKeyOptions(agentSock)...)
	opts, err := controlOptions(r.ID)
	if err != nil {
		return nil, err // a made key signs in once, for the shared connection
	}
	return append(args, opts...), nil
}

// ensureMadeKeyMaster brings up the shared connection to a made-key server
// if it isn't up: a run agent serves the key while the connection signs in,
// and is closed as soon as it's up. Later runs go through it with no key.
func ensureMadeKeyMaster(e *Env, host string) error {
	r, err := e.ServerRecord(host)
	if err != nil {
		return err
	}
	check, err := madeKeyArgs(e, host, "none")
	if err != nil {
		return err
	}
	run := func(args []string, extra ...string) (string, error) {
		cmd, err := bounded.Command(context.Background(), sshSetup, args[0], append(append(slices.Clone(args[1:]), extra...), "--", host)...)
		if err != nil {
			return "", err
		}
		cmd.Stdin = nil
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, err := run(check, "-O", "check"); err == nil {
		return nil
	}
	signer, ok := madeKeySigner(r.ID)
	if !ok {
		return fmt.Errorf("Portenv needs %s's key from the Keychain. Open Portenv on this Mac to allow it.", ServerDisplayName(host)) //nolint:staticcheck // shown as is
	}
	a, err := startRunAgent(signer)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	start, err := madeKeyArgs(e, host, a.sock)
	if err != nil {
		return err
	}
	if out, err := run(start, "-M", "-N", "-f"); err != nil {
		if kerr := noteHostKeyFailure(host, out); kerr != nil {
			return kerr
		}
		return fmt.Errorf("couldn't sign in to %s: %s", ServerDisplayName(host), lastLine(out, err))
	}
	return nil
}

func lastLine(out string, err error) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return err.Error()
	}
	lines := strings.Split(out, "\n")
	return lines[len(lines)-1]
}
