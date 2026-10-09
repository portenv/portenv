// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// memProbe looks for a secret in another process: environment, fd 3,
// command line, writable memory, and ptrace attach. It prints LEAKED or
// DENIED.
const memProbe = `
import ctypes, os, re, sys
pid, secret, what = int(sys.argv[1]), sys.argv[2].encode(), sys.argv[3]
def out(ok): print("LEAKED" if ok else "DENIED")
try:
    if what == "environ": out(secret in open(f"/proc/{pid}/environ", "rb").read())
    elif what == "fd3": os.open(f"/proc/{pid}/fd/3", os.O_RDONLY | os.O_NONBLOCK); out(True)
    elif what == "cmdline": out(secret in open(f"/proc/{pid}/cmdline", "rb").read())
    elif what == "mem":
        found = False
        with open(f"/proc/{pid}/mem", "rb", 0) as mem:
            for line in open(f"/proc/{pid}/maps"):
                m = re.match(r"([0-9a-f]+)-([0-9a-f]+) rw", line)
                if not m: continue
                try:
                    mem.seek(int(m[1], 16)); found |= secret in mem.read(int(m[2], 16) - int(m[1], 16))
                except OSError: pass
        out(found)
    elif what == "ptrace":
        libc = ctypes.CDLL(None, use_errno=True)
        ok = libc.ptrace(16, pid, 0, 0) == 0
        if ok: libc.ptrace(17, pid, 0, 0)
        out(ok)
except OSError:
    out(False)
`

// TestChannelPasswordIsUnreadable extends ADR 0005's probe to the agent
// channel (ADR 0010): while restic runs through RunRestic with a password,
// neither the API process nor restic can be read by root or a lane user.
func TestChannelPasswordIsUnreadable(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	d, err := New(Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := driver.BoxID("isolation-" + hex.EncodeToString(b))
	home := "portenv-test-home-" + string(id)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = d.Stop(ctx, id, time.Second)
		_ = d.Destroy(ctx, id)
		_, _ = d.cli.VolumeRemove(ctx, home, client.VolumeRemoveOptions{Force: true})
		_, _ = d.cli.VolumeRemove(ctx, d.cacheVolume(id), client.VolumeRemoveOptions{Force: true})
	})
	if _, err := d.Create(ctx, driver.Box{ID: id, Name: string(id), ToolboxImage: image}); err != nil {
		t.Fatal(err)
	}
	if err := d.MountHome(ctx, id, driver.HomeStorage{Ref: home, Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	exec := func(user string, argv ...string) string {
		t.Helper()
		res, err := d.Exec(ctx, id, driver.ExecRequest{Argv: argv, User: user, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	for range 100 {
		if strings.HasPrefix(exec("", "portenv-agent", "ready"), "READINESS_STATE_READY") {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	ch, err := d.AgentChannel(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	c := agentv1.NewAgentServiceClient(conn)

	secret := "portenv-probe-" + hex.EncodeToString(b) + "-channel"
	input, _ := json.Marshal(map[string]string{"password": secret})
	exec("", "sh", "-c", "install -d -o 990 -g 990 -m 0700 /var/tmp/repo && mkdir -p /home/work/probe && head -c 40M /dev/urandom > /home/work/probe/data.bin && (id -u lane >/dev/null 2>&1 || useradd --uid 2000 --no-create-home lane)")
	if res, err := c.RunRestic(ctx, &agentv1.RunResticRequest{Args: []string{"--repo", "/var/tmp/repo", "init"}, Input: input}); err != nil || res.GetExitCode() != 0 {
		t.Fatalf("restic init over the channel: %v %s", err, res.GetStderr())
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.RunRestic(ctx, &agentv1.RunResticRequest{Args: []string{"--repo", "/var/tmp/repo", "backup", "--limit-upload", "1024", "/home/work/probe"}, Input: input})
		done <- err
	}()
	var servePid, resticPid string
	for range 50 {
		servePid = exec("", "pgrep", "-f", "^portenv-agent serve")
		resticPid = exec("", "pgrep", "-f", "^restic --repo /var/tmp/repo backup")
		if servePid != "" && resticPid != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if servePid == "" || resticPid == "" {
		t.Fatalf("serve %q restic %q not found", servePid, resticPid)
	}
	// restic runs with its own file capabilities but no inheritable or
	// ambient ones, so nothing it starts inherits any.
	for _, set := range []string{"CapInh", "CapAmb"} {
		if got := exec("", "sh", "-c", "grep '^"+set+"' /proc/"+resticPid+"/status | awk '{print $2}'"); got != "0000000000000000" {
			t.Errorf("restic has %s %s", set, got)
		}
	}
	exec("", "sh", "-c", "cat > /tmp/memprobe.py <<'PY'\n"+memProbe+"\nPY")
	// Control: an ordinary root process holding the secret is readable, so
	// the probe really detects a leak.
	if _, err := d.Exec(ctx, id, driver.ExecRequest{Argv: []string{"sh", "-c", "PORTENV_CANARY=" + secret + " setsid sleep 120 >/dev/null 2>&1 &"}, Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	control := exec("", "pgrep", "-n", "-x", "sleep")
	if got := exec("", "python3", "/tmp/memprobe.py", control, secret, "environ"); got != "LEAKED" {
		t.Fatalf("control: the probe did not find the secret in a plain root process (%s)", got)
	}
	for _, target := range []struct{ name, pid string }{{"agent API process", servePid}, {"restic", resticPid}} {
		for _, attacker := range []string{"root", "root-as-sync", "root-as-agent", "lane"} {
			for _, what := range []string{"environ", "fd3", "cmdline", "mem", "ptrace"} {
				argv := []string{"python3", "/tmp/memprobe.py", target.pid, secret, what}
				switch attacker {
				case "root-as-sync":
					argv = append([]string{"setpriv", "--reuid=990", "--regid=990", "--clear-groups"}, argv...)
				case "root-as-agent":
					argv = append([]string{"setpriv", "--reuid=991", "--regid=991", "--clear-groups"}, argv...)
				case "lane":
					argv = append([]string{"setpriv", "--reuid=2000", "--regid=2000", "--clear-groups"}, argv...)
				}
				if got := exec("", argv...); got != "DENIED" {
					t.Errorf("%s: %s reading %s: %s", target.name, attacker, what, got)
				}
			}
		}
	}
	exec("", "sh", "-c", "kill -KILL "+resticPid)
	<-done
}
