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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// TestChannelConditions checks ADR 0010's conditions against a real box:
// the channel's files never reach a save and are gone once the API process
// has loaded them; root in the box cannot read the key or token; and an
// impostor on the agent's port never receives the token or a password.
func TestChannelConditions(t *testing.T) {
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
	id := driver.BoxID("conditions-" + hex.EncodeToString(b))
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
	// Exec here is the test inspecting the box as root would, never an
	// access path.
	sh := func(user, script string) string {
		t.Helper()
		res, err := d.Exec(ctx, id, driver.ExecRequest{Argv: []string{"sh", "-c", script}, User: user, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	for range 100 {
		if strings.HasPrefix(sh("", "portenv-agent ready"), "READINESS_STATE_READY") {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	ch, err := d.AgentChannel(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Condition 2: the files are gone, and the API process runs as
	// portenv-agent.
	if got := sh("", "test -e "+agent.ChannelDir+" && echo present || echo absent"); got != "absent" {
		t.Errorf("channel directory after start: %s, want absent", got)
	}
	servePid := sh("", "pgrep -f '^portenv-agent serve'")
	if uid := sh("", "ps -o uid= -p "+servePid); uid != "991" {
		t.Errorf("API process uid %q, want 991 (portenv-agent)", uid)
	}

	// Condition 1: a save right after start contains none of them.
	conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	c := agentv1.NewAgentServiceClient(conn)
	password := "portenv-conditions-" + hex.EncodeToString(b)
	input, _ := json.Marshal(map[string]string{"password": password})
	sh("", "install -d -o 990 -g 990 -m 0700 /var/tmp/repo; id -u lane >/dev/null 2>&1 || useradd --uid 2000 --no-create-home lane")
	for _, args := range [][]string{{"init"}, {"backup", "/home"}} {
		if res, err := c.RunRestic(ctx, &agentv1.RunResticRequest{Args: append([]string{"--repo", "/var/tmp/repo"}, args...), Input: input}); err != nil || res.GetExitCode() != 0 {
			t.Fatalf("restic %v: %v %s", args, err, res.GetStderr())
		}
	}
	// Restore the save into a scratch directory and look for the files and
	// their contents there (restic's allow-list has no ls).
	sh("", "install -d -o 990 -g 990 -m 0700 /var/tmp/restored")
	if res, err := c.RunRestic(ctx, &agentv1.RunResticRequest{Args: []string{"--repo", "/var/tmp/repo", "restore", "latest", "--target", "/var/tmp/restored"}, Input: input}); err != nil || res.GetExitCode() != 0 {
		t.Fatalf("restic restore: %v %s", err, res.GetStderr())
	}
	if got := sh("", "find /var/tmp/restored \\( -name token -o -name cert.pem -o -name key.pem -o -path '*portenv/agent*' \\) | head -3"); got != "" {
		t.Errorf("the save contains channel files: %s", got)
	}
	if got := sh("", "grep -rlF "+ch.Token+" /var/tmp/restored 2>/dev/null | head -1"); got != "" {
		t.Errorf("the save contains the token: %s", got)
	}
	if got := sh("", "ls /var/tmp/restored/home | tr '\\n' ' '"); !strings.Contains(got, "work") {
		t.Fatalf("the restored save does not hold the home (%q): the check would prove nothing", got)
	}
	if got := sh("", "grep -rlF "+ch.Token+" /home 2>/dev/null | head -1"); got != "" {
		t.Errorf("the token is in a file under /home: %s", got)
	}

	// Condition 3: root in the box, and the API process's own uid, cannot
	// read the token from files, /proc or the environment.
	sh("", "cat > /tmp/memprobe.py <<'PY'\n"+memProbe+"\nPY")
	for _, attacker := range []string{"root", "root-as-agent", "lane"} {
		for _, what := range []string{"environ", "fd3", "cmdline", "mem", "ptrace"} {
			cmd := "python3 /tmp/memprobe.py " + servePid + " " + ch.Token + " " + what
			switch attacker {
			case "root-as-agent":
				cmd = "setpriv --reuid=991 --regid=991 --clear-groups " + cmd
			case "lane":
				cmd = "setpriv --reuid=2000 --regid=2000 --clear-groups " + cmd
			}
			if got := sh("", cmd); got != "DENIED" {
				t.Errorf("%s reading the token through %s: %s", attacker, what, got)
			}
		}
	}
	if got := sh("", "ls -l /proc/"+servePid+"/fd 2>/dev/null | grep -c 'portenv/agent' || true"); got != "0" {
		t.Errorf("the API process still has channel files open (%s)", got)
	}

	// Condition 3: root kills the agent and starts an impostor with a
	// self-made certificate on the same port. The client refuses it: the
	// impostor never sees the token or a password.
	sh("", "pkill -f '^portenv-agent serve'; sleep 1")
	if got := sh("", "pgrep -f '^portenv-agent serve' || echo none"); got != "none" {
		t.Fatalf("the agent was restarted without its secrets: %s", got)
	}
	sh("", "openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=portenv-agent -days 1 -keyout /tmp/i.key -out /tmp/i.crt 2>/dev/null; "+
		"setsid sh -c 'openssl s_server -accept 7700 -cert /tmp/i.crt -key /tmp/i.key -quiet > /tmp/impostor.log 2>&1' >/dev/null 2>&1 & sleep 1")
	if raw, err := ch.Dial(ctx); err != nil {
		t.Fatalf("the impostor is not listening: %v", err)
	} else {
		_ = raw.Close()
	}
	conn2, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn2.Close() }()
	_, err = agentv1.NewAgentServiceClient(conn2).RunRestic(ctx, &agentv1.RunResticRequest{Args: []string{"--repo", "/var/tmp/repo", "snapshots"}, Input: input})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "the certificate is not this box's") {
		t.Fatalf("against the impostor: %v, want the pinned certificate to refuse it", err)
	}
	log := sh("", "cat /tmp/impostor.log")
	if strings.Contains(log, ch.Token) || strings.Contains(log, password) {
		t.Fatal("the impostor received the token or the password")
	}
}
