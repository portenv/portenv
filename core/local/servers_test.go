// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

var (
	fakeSSHOnce sync.Once
	fakeSSHPath string
	fakeSSHErr  error
)

// fakeSSHBinary builds testdata/fakessh once per test run.
func fakeSSHBinary(t *testing.T) string {
	t.Helper()
	fakeSSHOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fakessh")
		if err != nil {
			fakeSSHErr = err
			return
		}
		fakeSSHPath = filepath.Join(dir, "ssh")
		out, err := exec.Command("go", "build", "-o", fakeSSHPath, "./testdata/fakessh").CombinedOutput() // #nosec G204 -- the test's own helper
		if err != nil {
			fakeSSHErr = err
			t.Log(string(out))
		}
	})
	if fakeSSHErr != nil {
		t.Fatal(fakeSSHErr)
	}
	return fakeSSHPath
}

// hostKey is a fresh Ed25519 host key as known_hosts writes it.
func hostKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// trustWorld is a Mac with servers in machine.json, a fake ssh that
// reaches hosts (alias → host:port) with the given keys, and a HOME of the
// test's own: the person's real ~/.ssh is never read.
type trustWorld struct {
	e     *Env
	keys  string
	home  string
	hosts map[string]string
}

func newTrustWorld(t *testing.T, servers ...string) *trustWorld {
	t.Helper()
	w := &trustWorld{e: &Env{Dir: t.TempDir(), Machine: "mac-1"}, keys: t.TempDir(), home: t.TempDir(), hosts: map[string]string{}}
	t.Setenv("PORTENV_SSH", fakeSSHBinary(t))
	t.Setenv("FAKESSH_KEYS", w.keys)
	t.Setenv("HOME", w.home)
	quoted := make([]string, len(servers))
	for i, s := range servers {
		quoted[i] = `"` + s + `"`
	}
	if err := os.WriteFile(filepath.Join(w.e.Dir, "machine.json"), []byte(`{"servers":[`+strings.Join(quoted, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w.e.UseForServers()
	t.Cleanup(func() { useForServers(nil) })
	return w
}

// serve puts a host with key at addr (host:port), reached as alias.
func (w *trustWorld) serve(t *testing.T, alias, addr, key string) {
	t.Helper()
	w.hosts[alias] = addr
	var m []string
	for a, v := range w.hosts {
		m = append(m, a+"="+v)
	}
	t.Setenv("FAKESSH_HOSTS", strings.Join(m, ","))
	if err := os.WriteFile(filepath.Join(w.keys, addr), []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// knownHosts writes the person's ~/.ssh/known_hosts (in the test's HOME).
func (w *trustWorld) knownHosts(t *testing.T, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(w.home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.home, ".ssh", "known_hosts"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// confirmedServers makes targets this Mac's confirmed servers, for tests
// whose fake ssh doesn't check keys.
func confirmedServers(t *testing.T, targets ...string) *Env {
	t.Helper()
	e := &Env{Dir: t.TempDir(), Machine: "mac-1"}
	key := hostKey(t)
	for _, h := range targets {
		if err := e.PinServerKey(h, key); err != nil {
			t.Fatal(err)
		}
	}
	e.UseForServers()
	t.Cleanup(func() { useForServers(nil); forgetMadeKeys() })
	return e
}

// TestServersMigrateOnce: machine.json's servers become records, not yet
// confirmed; machine.json is left as it was, for an older build (R-0180).
func TestServersMigrateOnce(t *testing.T) {
	w := newTrustWorld(t, "me@one", "me@two")
	before, _ := os.ReadFile(filepath.Join(w.e.Dir, "machine.json"))
	rs, err := w.e.ServerRecords()
	if err != nil || len(rs) != 2 || rs[0].Target != "me@one" || rs[0].ID != ServerID("me@one") || rs[0].State != ServerUnconfirmed {
		t.Fatalf("records %+v, %v", rs, err)
	}
	after, _ := os.ReadFile(filepath.Join(w.e.Dir, "machine.json"))
	if string(before) != string(after) {
		t.Fatal("machine.json changed")
	}
	// Once: a server removed from machine.json later isn't brought back.
	if err := os.WriteFile(filepath.Join(w.e.Dir, "machine.json"), []byte(`{"servers":["me@three"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if rs, _ := w.e.ServerRecords(); len(rs) != 2 {
		t.Fatalf("migrated twice: %+v", rs)
	}
}

// TestAnUnconfirmedServerIsNeverContacted: no command runs on it; the
// error names it, plainly.
func TestAnUnconfirmedServerIsNeverContacted(t *testing.T) {
	w := newTrustWorld(t, "me@test-server")
	w.serve(t, "test-server", "test-server:22", hostKey(t))
	log := filepath.Join(t.TempDir(), "ssh.log")
	t.Setenv("FAKESSH_LOG", log)
	ctx := context.Background()
	_, err := AppOn(ctx, "me@test-server", "state", "acme-api")
	if err == nil || err.Error() != "Confirm test-server to open this." {
		t.Fatalf("got %v", err)
	}
	if _, err := ForwardLocal(ctx, "me@test-server", "127.0.0.1:8000"); err == nil {
		t.Fatal("forwarded to it")
	}
	if err := RunRemote(ctx, QuickRemote, "me@test-server", io.Discard, "true"); err == nil {
		t.Fatal("ran on it")
	}
	if _, err := AppOn(ctx, "me@not-mine", "state", "acme-api"); err == nil {
		t.Fatal("a server that isn't this Mac's ran a command")
	}
	if b, err := os.ReadFile(log); err == nil {
		t.Fatalf("ssh ran: %s", b)
	}
}

// TestPORTENV_SSHCantLoosenThePin: options in PORTENV_SSH come after the
// pins, and ssh keeps the first value, so they can't turn checking off.
func TestPORTENV_SSHCantLoosenThePin(t *testing.T) {
	w := newTrustWorld(t, "me@test-server")
	w.serve(t, "test-server", "test-server:22", hostKey(t))
	ctx := context.Background()
	scan, _ := w.e.ScanServerKey(ctx, ServerID("me@test-server"))
	if err := w.e.TrustServerKey(ServerID("me@test-server"), scan.Fingerprint); err != nil {
		t.Fatal(err)
	}
	w.serve(t, "test-server", "test-server:22", hostKey(t))
	t.Setenv("PORTENV_SSH", fakeSSHBinary(t)+" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o HostKeyAlias=test-server")
	if out, err := AppOn(ctx, "me@test-server", "state", "acme-api"); err == nil {
		t.Fatalf("ran with a changed key: %q", out)
	}
}

// TestScanThenTrust: the key is found through ssh itself (never
// ssh-keyscan), pinned by its alias with its type, and from then on every
// run carries the pin; Trust needs the fingerprint the person saw.
func TestScanThenTrust(t *testing.T) {
	w := newTrustWorld(t, "me@test-server")
	key := hostKey(t)
	w.serve(t, "test-server", "203.0.113.24:2222", key)
	ctx := context.Background()
	scan, err := w.e.ScanServerKey(ctx, ServerID("me@test-server"))
	if err != nil {
		t.Fatal(err)
	}
	if scan.KeyType != "ssh-ed25519" || !strings.HasPrefix(scan.Fingerprint, "SHA256:") || scan.KnownHosts != KnownHostsNone {
		t.Fatalf("scan %+v", scan)
	}
	if err := w.e.TrustServerKey(ServerID("me@test-server"), "SHA256:wrong"); err == nil {
		t.Fatal("trusted a fingerprint the person didn't see")
	}
	if err := w.e.TrustServerKey(ServerID("me@test-server"), scan.Fingerprint); err != nil {
		t.Fatal(err)
	}
	out, err := AppOn(ctx, "me@test-server", "state", "acme-api")
	if err != nil || !strings.Contains(out, "ran on 203.0.113.24:2222") {
		t.Fatalf("after trust: %q, %v", out, err)
	}
	args, err := sshArgs("me@test-server")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"StrictHostKeyChecking=yes", "UpdateHostKeys=no", "GlobalKnownHostsFile=/dev/null", "CheckHostIP=no",
		"HostKeyAlias=portenv-" + ServerID("me@test-server"), "HostKeyAlgorithms=ssh-ed25519", "UserKnownHostsFile=" + filepath.Join(w.e.Dir, "known_hosts")} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %q", want, joined)
		}
	}
	// No temporary scan file is left in the socket folder.
	dir, _ := SocketDir()
	if ents, _ := filepath.Glob(filepath.Join(dir, "scan-*")); len(ents) != 0 {
		t.Fatalf("left: %v", ents)
	}
}

// TestAChangedKeyIsAHardStop: nothing runs, the record says so, and only
// Forget (then the wizard) gets past it.
func TestAChangedKeyIsAHardStop(t *testing.T) {
	w := newTrustWorld(t, "me@test-server")
	w.serve(t, "test-server", "test-server:22", hostKey(t))
	ctx := context.Background()
	scan, _ := w.e.ScanServerKey(ctx, ServerID("me@test-server"))
	if err := w.e.TrustServerKey(ServerID("me@test-server"), scan.Fingerprint); err != nil {
		t.Fatal(err)
	}
	w.serve(t, "test-server", "test-server:22", hostKey(t)) // reinstalled, or someone in between
	_, err := AppOn(ctx, "me@test-server", "state", "acme-api")
	if err == nil || !strings.Contains(err.Error(), "test-server") || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("got %v", err)
	}
	if r, _ := w.e.ServerRecord("me@test-server"); r.State != ServerKeyChanged {
		t.Fatalf("record %+v", r)
	}
	if err := w.e.TrustServerKey(ServerID("me@test-server"), scan.Fingerprint); err == nil {
		t.Fatal("trust anyway")
	}
	if _, err := AppOn(ctx, "me@test-server", "state", "acme-api"); err == nil {
		t.Fatal("ran after a changed key")
	}
	if err := w.e.ForgetServerKey(ServerID("me@test-server")); err != nil {
		t.Fatal(err)
	}
	if r, _ := w.e.ServerRecord("me@test-server"); r.State != ServerUnconfirmed {
		t.Fatalf("after Forget %+v", r)
	}
}

// TestKeyTypesInOrder: Ed25519, then ECDSA, then RSA as rsa-sha2-512 and
// rsa-sha2-256, never ssh-rsa (SHA-1); a host offering only a certificate
// is told so and refused (R-0180).
func TestKeyTypesInOrder(t *testing.T) {
	if hostKeyAlgorithms != "ssh-ed25519,ecdsa-sha2-nistp256,ecdsa-sha2-nistp384,ecdsa-sha2-nistp521,rsa-sha2-512,rsa-sha2-256" {
		t.Fatalf("order %q", hostKeyAlgorithms)
	}
	w := newTrustWorld(t, "me@cert-only")
	w.serve(t, "cert-only", "cert-only:22", "ssh-ed25519-cert-v01@openssh.com AAAAIHNzaC1lZDI1NTE5LWNlcnQtdjAxQG9wZW5zc2guY29t")
	_, err := w.e.ScanServerKey(context.Background(), ServerID("me@cert-only"))
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("got %v", err)
	}
}

// TestKnownHostsPrefill (R-0180): the person's ~/.ssh/known_hosts is read,
// never written. An exact match (plain or hashed) confirms; a different
// key of the same type is a changed key, with no Trust; no entry offers
// Trust.
func TestKnownHostsPrefill(t *testing.T) {
	key := hostKey(t)
	for _, tc := range []struct {
		name  string
		lines func(w *trustWorld) []string
		want  string
		state ServerState
	}{
		{"plain match", func(*trustWorld) []string { return []string{"[203.0.113.24]:2222 " + key} }, KnownHostsMatch, ServerConfirmed},
		{"hashed match", func(*trustWorld) []string { return []string{hashedLine(t, "[203.0.113.24]:2222", key)} }, KnownHostsMatch, ServerConfirmed},
		{"different key", func(*trustWorld) []string { return []string{"[203.0.113.24]:2222 " + hostKey(t)} }, KnownHostsMismatch, ServerKeyChanged},
		{"no entry", func(*trustWorld) []string { return []string{"other.example " + hostKey(t)} }, KnownHostsNone, ServerUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTrustWorld(t, "me@test-server")
			w.serve(t, "test-server", "203.0.113.24:2222", key)
			w.knownHosts(t, tc.lines(w)...)
			before, _ := os.ReadFile(filepath.Join(w.home, ".ssh", "known_hosts"))
			scan, err := w.e.ScanServerKey(context.Background(), ServerID("me@test-server"))
			if err != nil {
				t.Fatal(err)
			}
			if scan.KnownHosts != tc.want {
				t.Fatalf("known_hosts %q, want %q", scan.KnownHosts, tc.want)
			}
			r, _ := w.e.ServerRecord("me@test-server")
			if r.State != tc.state {
				t.Fatalf("state %q, want %q", r.State, tc.state)
			}
			if tc.state == ServerConfirmed && r.ConfirmedFrom != ConfirmedFromKnownHosts {
				t.Fatalf("confirmed from %q", r.ConfirmedFrom)
			}
			after, _ := os.ReadFile(filepath.Join(w.home, ".ssh", "known_hosts"))
			if string(before) != string(after) {
				t.Fatal("~/.ssh/known_hosts was written")
			}
		})
	}
}

// hashedLine is a hashed known_hosts line for name, made by ssh-keygen -H
// on a file of the test's own (-f): never the person's.
func hashedLine(t *testing.T, name, key string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "kh")
	if err := os.WriteFile(f, []byte(name+" "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ssh-keygen", "-H", "-f", f).CombinedOutput(); err != nil { // #nosec G204 -- the test's own file
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// TestServerFilesArePrivate (R-0183): servers.json and Portenv's
// known_hosts are 0600, in a 0700 folder.
func TestServerFilesArePrivate(t *testing.T) {
	t.Setenv("PORTENV_HOME", filepath.Join(t.TempDir(), "Portenv"))
	e, err := NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { useForServers(nil) })
	if err := e.PinServerKey("me@test-server", hostKey(t)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"servers.json", "known_hosts"} {
		fi, err := os.Stat(filepath.Join(e.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s is %o", name, fi.Mode().Perm())
		}
	}
	fi, _ := os.Stat(e.Dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("the folder is %o", fi.Mode().Perm())
	}
}
