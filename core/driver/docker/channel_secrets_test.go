// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/drivertest"
)

// tokenDirs are the places in a box where the channel's secrets would land
// if anything ever wrote them (ADR 0010, condition 2). The secrets arrive
// on stdin and live only in the API process's memory (ADR 0014), so none
// of these may hold them:
//   - /run, /run/portenv and /run/portenv-sftp: the agent's sockets and
//     per-run state;
//   - /tmp and /var/tmp: anything's scratch files;
//   - /home: the saved unit, which moves to other machines;
//   - /root and /etc: root's home and configuration;
//   - /var/log: logs written inside the box (apt, dpkg, anything else);
//   - /var/cache/portenv-sync: restic's cache;
//   - /usr/local/libexec/portenv and /usr/share/portenv: the agent's own
//     binaries and skeleton home.
//
// The agent and init log to the container's stdout and stderr, which the
// engine keeps on the machine's disk; checkTokenNotWritten reads that log
// too. Everything else on the root file system is covered by the nightly
// full-disk scan (TestChannelTokenNotOnDisk).
var tokenDirs = []string{
	"/run", "/tmp", "/var/tmp", "/home", "/root", "/etc", "/var/log",
	"/var/cache/portenv-sync", "/usr/local/libexec/portenv", "/usr/share/portenv",
}

// tokenSearchScript reads a pattern on stdin and searches for it, so the
// pattern never appears in any process's command line or environment, the
// search's own included. Mode "files" lists the files under its arguments
// that contain the pattern; mode "procs" lists the processes (pid and
// name, never their command line) whose environment or command line holds
// it; mode "names" lists files under its arguments named like ADR 0010's
// channel files (token, cert.pem, key.pem, channel*, secrets*). It ends with SEARCH-DONE so a search that was cut short never
// passes for a clean one.
const tokenSearchScript = `IFS= read -r tok || { echo NO-PATTERN; exit 3; }
mode=$1; shift
case $mode in
files)
	printf '%s\n' "$tok" | grep -rlsF -D skip -f - -- "$@"
	;;
procs)
	for p in /proc/[0-9]*; do
		[ "$p" = "/proc/$$" ] && continue
		if printf '%s\n' "$tok" | grep -qsF -f - "$p/environ" "$p/cmdline"; then
			echo "$p $(cat "$p/comm" 2>/dev/null)"
		fi
	done
	;;
names)
	find "$@" \( -name token -o -name cert.pem -o -name key.pem -o -name 'channel*' -o -name 'secrets*' \) -print 2>/dev/null
	;;
*)
	echo "unknown mode $mode"; exit 3
	;;
esac
echo SEARCH-DONE`

// searchToken runs tokenSearchScript in the box as root with the pattern
// on stdin and returns what it found, one entry per line. A search that
// errors or doesn't finish fails the test.
func searchToken(ctx context.Context, t *testing.T, d *Driver, id driver.BoxID, pattern string, timeout time.Duration, mode string, args ...string) []string {
	t.Helper()
	argv := append([]string{"sh", "-c", tokenSearchScript, "sh", mode}, args...)
	res, err := d.probe(ctx, id, drivertest.ProbeRequest{Argv: argv, Stdin: []byte(pattern + "\n"), Timeout: timeout})
	if err != nil {
		t.Fatalf("the %s search for the token didn't finish within %s: %v (this counts as a failure)", mode, timeout, err)
	}
	out := strings.TrimSpace(string(res.Stdout))
	lines := strings.Split(out, "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "SEARCH-DONE" {
		t.Fatalf("the %s search for the token was cut short (no SEARCH-DONE; exit %d)", mode, res.ExitCode)
	}
	return lines[:len(lines)-1]
}

// boxLogHolds reports whether the box's stdout and stderr, as the engine
// keeps them, contain pattern.
func boxLogHolds(ctx context.Context, t *testing.T, d *Driver, id driver.BoxID, pattern string) bool {
	t.Helper()
	var all bytes.Buffer
	for chunk, err := range d.Logs(ctx, id, driver.LogOptions{}) {
		if err != nil {
			t.Fatalf("reading the box's log: %v", err)
		}
		all.Write(chunk.Data)
	}
	return bytes.Contains(all.Bytes(), []byte(pattern))
}

// checkTokenNotWritten checks that the channel token is in none of the
// places it could land: no file under tokenDirs holds it, no file there has
// a channel file's name, no process's environment or command line holds
// it, and the box's log doesn't. It reports paths and processes, never the
// token. Each search is bounded; one that doesn't finish fails the test.
func checkTokenNotWritten(ctx context.Context, t *testing.T, d *Driver, id driver.BoxID, token string) {
	t.Helper()
	const bound = 30 * time.Second
	if found := searchToken(ctx, t, d, id, token, bound, "files", tokenDirs...); len(found) > 0 {
		t.Errorf("the token is in a file in the box: %s", strings.Join(found, ", "))
	}
	if found := searchToken(ctx, t, d, id, token, bound, "names", tokenDirs...); len(found) > 0 {
		t.Errorf("channel files are in the box: %s", strings.Join(found, ", "))
	}
	if found := searchToken(ctx, t, d, id, token, bound, "procs"); len(found) > 0 {
		t.Errorf("the token is in a process's environment or command line: %s", strings.Join(found, ", "))
	}
	if boxLogHolds(ctx, t, d, id, token) {
		t.Error("the token is in the box's log (its stdout and stderr, which the engine keeps on the machine's disk)")
	}
}

// checkTheSearchFindsIt is the control for checkTokenNotWritten: it plants a
// decoy (a fresh random token, never the channel's) in each place in turn
// and requires the search to report exactly where. Without it, a search
// that silently found nothing would prove nothing.
func checkTheSearchFindsIt(ctx context.Context, t *testing.T, d *Driver, id driver.BoxID, sh func(user, script string) string) {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	decoy := hex.EncodeToString(b)
	const bound = 30 * time.Second
	for _, dir := range tokenDirs {
		path := strings.TrimSuffix(dir, "/") + "/portenv-decoy-file"
		sh("", "mkdir -p "+dir+" && printf 'x %s x\\n' "+decoy+" > "+path)
		found := searchToken(ctx, t, d, id, decoy, bound, "files", tokenDirs...)
		sh("", "rm -f "+path)
		if !containsLine(found, path) {
			t.Errorf("control: a decoy planted at %s wasn't found (found %q): the check there proves nothing", path, found)
		}
	}
	for _, name := range []string{"token", "cert.pem", "key.pem", "channel-secrets", "secrets.line"} {
		path := "/run/portenv/" + name
		sh("", "touch "+path)
		found := searchToken(ctx, t, d, id, decoy, bound, "names", tokenDirs...)
		sh("", "rm -f "+path)
		if !containsLine(found, path) {
			t.Errorf("control: a channel file named %s wasn't found (found %q)", path, found)
		}
	}
	// A process holding the decoy in its environment, then one holding it
	// in its command line.
	for what, start := range map[string]string{
		"environment":  "DECOY=" + decoy + " setsid sleep 120 >/dev/null 2>&1 < /dev/null &",
		"command line": "setsid sh -c 'sleep 120' " + decoy + " >/dev/null 2>&1 < /dev/null &",
	} {
		sh("", start+" sleep 0.3")
		found := searchToken(ctx, t, d, id, decoy, bound, "procs")
		sh("", "pkill -f 'sleep 120' || true")
		if len(found) == 0 {
			t.Errorf("control: a process with the decoy in its %s wasn't found", what)
		}
	}
	// The box's log: init and the API process write their logs to the
	// container's stderr (slog's text handler), which is what boxLogHolds
	// reads. Root in the box can't write to init's descriptors itself (init
	// is non-dumpable), so the control is a line the agent always writes
	// there once the box is up.
	ok := false
	for range 50 {
		if boxLogHolds(ctx, t, d, id, `msg="box ready"`) {
			ok = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ok {
		t.Error(`control: the box's log doesn't show the agent's own msg="box ready" line: the log check reads the wrong stream`)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// startChannelBox creates and starts a box from PORTENV_TEST_IMAGE with a
// fresh home, waits for the agent, and returns it with its channel and a
// root shell probe (the test inspecting the box as root would, never an
// access path). It skips without an image.
func startChannelBox(t *testing.T, ctx context.Context, prefix string) (*Driver, driver.BoxID, driver.AgentChannel, func(user, script string) string) {
	t.Helper()
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make driver-test")
	}
	d, err := New(Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := driver.BoxID(prefix + "-" + hex.EncodeToString(b))
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
	sh := func(user, script string) string {
		t.Helper()
		res, err := d.probe(ctx, id, drivertest.ProbeRequest{Argv: []string{"sh", "-c", script}, User: user, Timeout: time.Minute})
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
	return d, id, ch, sh
}
