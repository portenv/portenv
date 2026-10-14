// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshdPath is OpenSSH's server, run unprivileged on 127.0.0.1 for these
// tests. On CI it must be there (the Go job installs it); elsewhere a Mac
// without it skips.
func sshdPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("sshd is missing on CI: the Go job installs openssh-server")
	}
	t.Skip("no sshd on this machine")
	return ""
}

// sshdWorld is two real sshds on 127.0.0.1 run as this user, a jump host
// and a target reached only through it, and an ssh config (-F, never
// ~/.ssh) that names the target by an alias with ProxyJump. Nothing reads
// or writes the person's ~/.ssh: HOME is the test's own and every
// known_hosts file is named.
type sshdWorld struct {
	dir        string
	sshd       string
	clientKey  string
	jumpPort   int
	targetPort int
	target     *exec.Cmd
	config     string
}

func keygen(t *testing.T, path string) string {
	t.Helper()
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", path).CombinedOutput(); err != nil { // #nosec G204 -- a new file of the test's own
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	b, err := os.ReadFile(path + ".pub") // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	return f[0] + " " + f[1]
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startSSHD runs sshd on port with hostKey (a private key file) until the
// test ends.
func (w *sshdWorld) startSSHD(t *testing.T, name string, port int, hostKey string) *exec.Cmd {
	t.Helper()
	conf := filepath.Join(w.dir, name+".sshd_config")
	body := fmt.Sprintf("ListenAddress 127.0.0.1:%d\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nAllowTcpForwarding yes\nLogLevel ERROR\n",
		port, hostKey, filepath.Join(w.dir, name+".pid"), filepath.Join(w.dir, "authorized_keys"))
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(w.sshd, "-D", "-e", "-f", conf) // #nosec G204 -- the test's own sshd
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
			_ = c.Close()
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sshd %s didn't start", name)
	return nil
}

func newSSHDWorld(t *testing.T) (*sshdWorld, string, string) {
	t.Helper()
	w := &sshdWorld{dir: t.TempDir(), sshd: sshdPath(t)}
	t.Setenv("HOME", w.dir)
	t.Setenv("SSH_AUTH_SOCK", "") // the person's agent isn't offered
	w.clientKey = filepath.Join(w.dir, "client")
	pub := keygen(t, w.clientKey)
	if err := os.WriteFile(filepath.Join(w.dir, "authorized_keys"), []byte(pub+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jumpKey := keygen(t, filepath.Join(w.dir, "jump_host_key"))
	targetKey := keygen(t, filepath.Join(w.dir, "target_host_key"))
	w.jumpPort, w.targetPort = freePort(t), freePort(t)
	w.startSSHD(t, "jump", w.jumpPort, filepath.Join(w.dir, "jump_host_key"))
	w.target = w.startSSHD(t, "target", w.targetPort, filepath.Join(w.dir, "target_host_key"))
	// The jump host's key is the person's to have checked, in their own
	// known hosts (here, the test's file); the target's is Portenv's.
	jumpKH := filepath.Join(w.dir, "jump_known_hosts")
	if err := os.WriteFile(jumpKH, []byte(fmt.Sprintf("[127.0.0.1]:%d %s\n", w.jumpPort, jumpKey)), 0o600); err != nil {
		t.Fatal(err)
	}
	w.config = filepath.Join(w.dir, "ssh_config")
	cfg := fmt.Sprintf(`Host jump
  HostName 127.0.0.1
  Port %d
  UserKnownHostsFile %s
Host target-alias
  HostName 127.0.0.1
  Port %d
  ProxyJump jump
  UserKnownHostsFile %s
Host *
  IdentityFile %s
  IdentitiesOnly yes
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  BatchMode yes
`, w.jumpPort, jumpKH, w.targetPort, filepath.Join(w.dir, "user_known_hosts"), w.clientKey)
	if err := os.WriteFile(w.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTENV_SSH", "ssh -F "+w.config)
	return w, jumpKey, targetKey
}

// TestScanThroughProxyJump (R-0180): a server reached through a ProxyJump
// alias is scanned as ssh itself reaches it: the target's key is pinned,
// never the jump host's; then real OpenSSH holds the pin, and a changed
// key stops it.
func TestScanThroughProxyJump(t *testing.T) {
	w, jumpKey, targetKey := newSSHDWorld(t)
	me, err := user.Current() // never skipped for want of $USER: on CI it must run
	if err != nil {
		t.Fatal(err)
	}
	target := me.Username + "@target-alias"
	e := &Env{Dir: t.TempDir(), Machine: "mac-1"}
	if err := os.WriteFile(filepath.Join(e.Dir, "machine.json"), []byte(`{"servers":["`+target+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.UseForServers()
	t.Cleanup(func() { useForServers(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id := ServerID(target)
	scan, err := e.ScanServerKey(ctx, id)
	t.Logf("real sshd: jump on %d, target on %d", w.jumpPort, w.targetPort)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Fingerprint != Fingerprint(targetKey) || scan.Fingerprint == Fingerprint(jumpKey) || scan.KnownHosts != KnownHostsNone {
		t.Fatalf("scan %+v; target %s, jump %s", scan, Fingerprint(targetKey), Fingerprint(jumpKey))
	}
	if err := e.TrustServerKey(id, scan.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := RunRemote(ctx, QuickRemote, target, io.Discard, "true"); err != nil {
		t.Fatalf("after trust: %v", err)
	}
	// The person's (here, the test's) files are untouched: no ~/.ssh, and
	// the target isn't in their known hosts.
	if _, err := os.Stat(filepath.Join(w.dir, ".ssh")); err == nil {
		t.Fatal("~/.ssh was made")
	}
	if _, err := os.Stat(filepath.Join(w.dir, "user_known_hosts")); err == nil {
		t.Fatal("the person's known hosts were written")
	}

	// The target is reinstalled with a new host key: real ssh refuses, and
	// the record says so. The shared connection is closed first, as it
	// would be after 10 minutes.
	closeShared(t, target)
	_ = w.target.Process.Kill()
	_ = w.target.Wait()
	_ = os.Remove(filepath.Join(w.dir, "target_host_key"))
	_ = os.Remove(filepath.Join(w.dir, "target_host_key.pub"))
	keygen(t, filepath.Join(w.dir, "target_host_key"))
	w.target = w.startSSHD(t, "target2", w.targetPort, filepath.Join(w.dir, "target_host_key"))
	err = RunRemote(ctx, QuickRemote, target, io.Discard, "true")
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("with a changed key: %v", err)
	}
	if r, _ := e.ServerRecord(target); r.State != ServerKeyChanged {
		t.Fatalf("record %+v", r)
	}
}

// closeShared stops the shared connection to target, if one is up.
func closeShared(t *testing.T, target string) {
	t.Helper()
	// Its socket only: never a new sign-in.
	opts, err := controlOptions(ServerID(target))
	if err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("ssh", append(append([]string{"-F", "none"}, opts...), "-O", "exit", "--", target)...).Run() // #nosec G204 -- the test's own ssh
}

// TestAMadeKeySignsInOnceThroughItsAgent (ADR 0021, R-0180): a made-key
// server runs with -F none, so a planted config (a Match exec, a
// ProxyCommand) runs nothing; the key signs in once, through a run agent
// that's gone as soon as the shared connection is up; later runs go
// through that connection with no key at all.
func TestAMadeKeySignsInOnceThroughItsAgent(t *testing.T) {
	sshd := sshdPath(t)
	w := &sshdWorld{dir: t.TempDir(), sshd: sshd}
	t.Setenv("HOME", w.dir)
	t.Setenv("SSH_AUTH_SOCK", "")
	signer, priv := testSigner(t)
	if err := os.WriteFile(filepath.Join(w.dir, "authorized_keys"), ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	keygen(t, filepath.Join(w.dir, "host_key"))
	port := freePort(t)
	w.startSSHD(t, "made", port, filepath.Join(w.dir, "host_key"))
	marker := filepath.Join(w.dir, "match-exec-ran")
	planted := filepath.Join(w.dir, "planted_config")
	if err := os.WriteFile(planted, []byte(fmt.Sprintf("Match exec \"touch %s\"\n  ProxyCommand /usr/bin/false\nHost *\n  PermitLocalCommand yes\n  LocalCommand touch %s\n", marker, marker)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTENV_SSH", "ssh -F "+planted)
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("ssh://%s@127.0.0.1:%d", me.Username, port)
	e := &Env{Dir: t.TempDir(), Machine: "mac-1"}
	if err := os.WriteFile(filepath.Join(e.Dir, "machine.json"), []byte(`{"servers":["`+target+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.UseForServers()
	t.Cleanup(func() { closeShared(t, target); useForServers(nil); forgetMadeKeys() })
	id := ServerID(target)
	if err := e.UseMadeKey(id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scan, err := e.ScanServerKey(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.TrustServerKey(id, scan.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := RunRemote(ctx, QuickRemote, target, io.Discard, "true"); err == nil || !strings.Contains(err.Error(), "Open Portenv on this Mac") {
		t.Fatalf("with no key handed over: %v", err)
	}
	if err := e.ProvideServerKey(id, priv); err != nil {
		t.Fatal(err)
	}
	dir, _ := SocketDir()
	before, _ := filepath.Glob(filepath.Join(dir, "a-*"))
	var out strings.Builder
	if err := RunRemote(ctx, QuickRemote, target, &out, "echo", "signed-in"); err != nil || !strings.Contains(out.String(), "signed-in") {
		t.Fatalf("run: %v: %s", err, out.String())
	}
	if after, _ := filepath.Glob(filepath.Join(dir, "a-*")); len(after) != len(before) {
		t.Fatalf("an agent socket outlived the sign-in: %v (before: %v)", after, before)
	}
	forgetMadeKeys() // the key is gone; the shared connection carries on
	if err := RunRemote(ctx, QuickRemote, target, io.Discard, "true"); err != nil {
		t.Fatalf("a later run through the shared connection: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the planted config ran a command")
	}
}
