// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func testSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, priv
}

func dialAgent(t *testing.T, sock string) agent.ExtendedAgent {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return agent.NewClient(c)
}

// TestTheRunAgentServesOnlyItsKey (ADR 0021): one key, on one socket in
// the checked folder (a-<8 hex>), signing for that key only; adding,
// removing and locking are refused; the socket is gone once it's closed.
func TestTheRunAgentServesOnlyItsKey(t *testing.T) {
	signer, _ := testSigner(t)
	other, _ := testSigner(t)
	a, err := startRunAgent(signer)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := SocketDir()
	if filepath.Dir(a.sock) != dir || !regexp.MustCompile(`^a-[0-9a-f]{8}$`).MatchString(filepath.Base(a.sock)) {
		t.Fatalf("socket %s", a.sock)
	}
	if fi, err := os.Lstat(a.sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket: %v", err)
	}
	c := dialAgent(t, a.sock)
	keys, err := c.List()
	if err != nil || len(keys) != 1 || !bytes.Equal(keys[0].Marshal(), signer.PublicKey().Marshal()) {
		t.Fatalf("list %v, %v", keys, err)
	}
	sig, err := c.Sign(signer.PublicKey(), []byte("session"))
	if err != nil || signer.PublicKey().Verify([]byte("session"), sig) != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := c.Sign(other.PublicKey(), []byte("session")); err == nil {
		t.Fatal("signed for another key")
	}
	_, priv := testSigner(t)
	if err := c.Add(agent.AddedKey{PrivateKey: priv}); err == nil {
		t.Fatal("added a key")
	}
	if err := c.Remove(signer.PublicKey()); err == nil {
		t.Fatal("removed its key")
	}
	if err := c.RemoveAll(); err == nil {
		t.Fatal("removed all")
	}
	if err := c.Lock([]byte("x")); err == nil {
		t.Fatal("locked")
	}
	if keys, _ := c.List(); len(keys) != 1 {
		t.Fatal("its key went")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{a.sock, a.sock + ".pub"} {
		if _, err := os.Lstat(f); !os.IsNotExist(err) {
			t.Fatalf("%s left: %v", f, err)
		}
	}
}

// TestAMadeKeyServerIgnoresEveryConfig (ADR 0021, R-0180): -F none and
// Portenv's own options only; PORTENV_SSH gives the ssh binary and nothing
// else (a later -F would win over -F none); the agent options are explicit.
func TestAMadeKeyServerIgnoresEveryConfig(t *testing.T) {
	e := confirmedServers(t, "me@test-server")
	if err := e.UseMadeKey(ServerID("me@test-server")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTENV_SSH", "/usr/bin/ssh -F /tmp/planted -o StrictHostKeyChecking=no")
	if _, err := sshArgs("me@test-server"); err == nil || !strings.Contains(err.Error(), "Open Portenv on this Mac") {
		t.Fatalf("no key provided: %v", err)
	}
	_, priv := testSigner(t)
	if err := e.ProvideServerKey(ServerID("me@test-server"), priv); err != nil {
		t.Fatal(err)
	}
	args, err := madeKeyArgs(e, "me@test-server", "/tmp/a-01234567")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if args[0] != "/usr/bin/ssh" || args[1] != "-F" || args[2] != "none" {
		t.Fatalf("args %q", joined)
	}
	if strings.Contains(joined, "/tmp/planted") || strings.Contains(joined, "StrictHostKeyChecking=no") {
		t.Fatalf("PORTENV_SSH's options reached a made-key server: %q", joined)
	}
	for _, want := range []string{"ProxyCommand=none", "KnownHostsCommand=none", "PermitLocalCommand=no", "IdentitiesOnly=yes", "IdentityFile=/tmp/a-01234567.pub",
		"IdentityAgent=/tmp/a-01234567", "ForwardAgent=no", "ClearAllForwardings=yes", "ControlMaster=auto", "ControlPath=", "StrictHostKeyChecking=yes", "HostKeyAlias=portenv-"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %q", want, joined)
		}
	}
}

// TestTheSocketFolderIsNoWorkspaces (ADR 0021): the folder of Portenv's
// sockets is outside the person's home and Application Support, where
// Portenv's folder and a box's storage are (the driver's own test checks a
// box mounts nothing else).
func TestTheSocketFolderIsNoWorkspaces(t *testing.T) {
	dir, err := SocketDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.UserConfigDir() // Application Support, where every Portenv folder is
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, mounted := range []string{cfg, home} {
		if rel, err := filepath.Rel(mounted, dir); err == nil && !strings.HasPrefix(rel, "..") {
			t.Fatalf("%s is inside %s", dir, mounted)
		}
	}
}
