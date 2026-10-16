// SPDX-License-Identifier: Apache-2.0

package local

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

const testNonce = "5f2c9e1a7b3d4c60"

// probeOutput is what check.sh prints for a good server, between its
// markers, with noise around it (a chatty .bashrc) and lines changed.
func probeOutput(now time.Time, change map[string]string) string {
	kv := map[string]string{
		"os_id": "ubuntu", "os_version": "24.04", "arch": "x86_64", "mem_kb": "4026532", "disk_free_kb": "41943040",
		"user_root": "no", "sudo": "yes", "clock": "", "https_ghcr": "yes", "https_docker": "yes",
		"docker": "27.3.1", "apt": "yes", "portenv": "", "kvm": "yes",
	}
	kv["clock"] = itoa(now.Unix())
	for k, v := range change {
		kv[k] = v
	}
	var b strings.Builder
	b.WriteString("Welcome to the server! token=hunter2\n")
	b.WriteString("PORTENV-CHECK-BEGIN " + testNonce + "\n")
	for k, v := range kv {
		if v != "-" {
			b.WriteString(k + "=" + v + "\n")
		}
	}
	b.WriteString("PORTENV-CHECK-END " + testNonce + "\n")
	b.WriteString("logout\n")
	return b.String()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func line(t *testing.T, lines []CheckLine, id string) CheckLine {
	t.Helper()
	for _, l := range lines {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("no %s line in %+v", id, lines)
	return CheckLine{}
}

// TestAGoodServerPasses: every question answered, nothing failing.
func TestAGoodServerPasses(t *testing.T) {
	now := time.Now()
	lines, err := parseCheck(probeOutput(now, nil), testNonce, now)
	if err != nil {
		t.Fatal(err)
	}
	if !AllChecksPassed(lines) {
		t.Fatalf("failed: %+v", lines)
	}
	for _, id := range []string{"system", "memory", "disk", "sudo", "clock", "https-ghcr", "https-docker", "docker", "portenv", "kvm"} {
		line(t, lines, id)
	}
}

// TestEachFailureSaysWhatToDo (ADR 0021, GUIDELINES §10): one plain line,
// ending in what to do, and never the server's own words (R-0207).
func TestEachFailureSaysWhatToDo(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		id     string
		change map[string]string
		want   string
	}{
		// The supported list, by name; Debian when a tester asks (R-0212).
		{"system", map[string]string{"os_id": "debian", "os_version": "12"}, "Portenv sets up servers running Ubuntu 22.04 or 24.04 for now."},
		{"system", map[string]string{"os_id": "centos", "os_version": "7; rm -rf / # PRETTY"}, "Ubuntu 22.04 or 24.04"},
		{"system", map[string]string{"arch": "riscv64"}, "arm64 or amd64"},
		{"memory", map[string]string{"mem_kb": "1000000"}, "2 GB of memory"},
		{"disk", map[string]string{"disk_free_kb": "5242880"}, "15 GB free"},
		{"sudo", map[string]string{"sudo": "no"}, "NOPASSWD"},
		{"clock", map[string]string{"clock": itoa(now.Add(-5 * time.Minute).Unix())}, "time sync"},
		{"https-ghcr", map[string]string{"https_ghcr": "no"}, "ghcr.io"},
		{"https-docker", map[string]string{"https_docker": "no", "docker": ""}, "download.docker.com"},
		{"docker", map[string]string{"docker": "", "apt": "no"}, "Docker Engine"},
	} {
		lines, err := parseCheck(probeOutput(now, tc.change), testNonce, now)
		if err != nil {
			t.Fatal(err)
		}
		l := line(t, lines, tc.id)
		if l.State != CheckFailed || !strings.Contains(l.Fix, tc.want) || !strings.HasSuffix(l.Fix, ".") {
			t.Errorf("%s with %v: %+v", tc.id, tc.change, l)
		}
		if AllChecksPassed(lines) {
			t.Errorf("%s with %v passed", tc.id, tc.change)
		}
		for _, l := range lines {
			for _, raw := range []string{"centos", "rm -rf", "riscv64", "hunter2", "PRETTY"} {
				if strings.Contains(l.Title+l.Detail+l.Fix, raw) {
					t.Errorf("the server's words in %+v", l)
				}
			}
		}
	}
}

// TestRootNeedsNoSudo, and Docker present on the server makes download.
// docker.com's reachability information only.
func TestRootAndDockerPresent(t *testing.T) {
	now := time.Now()
	lines, _ := parseCheck(probeOutput(now, map[string]string{"user_root": "yes", "sudo": "no", "https_docker": "no"}), testNonce, now)
	if l := line(t, lines, "sudo"); l.State != CheckPassed {
		t.Fatalf("root: %+v", l)
	}
	if l := line(t, lines, "https-docker"); l.State == CheckFailed {
		t.Fatalf("Docker's already there: %+v", l)
	}
	if !AllChecksPassed(lines) {
		t.Fatal("failed")
	}
}

// TestKVMIsNeverAFailure: recorded for later.
func TestKVMIsNeverAFailure(t *testing.T) {
	now := time.Now()
	lines, _ := parseCheck(probeOutput(now, map[string]string{"kvm": "no"}), testNonce, now)
	if l := line(t, lines, "kvm"); l.State != CheckInfo {
		t.Fatalf("%+v", l)
	}
	if !AllChecksPassed(lines) {
		t.Fatal("KVM failed the check")
	}
}

// TestTheProbesOutputIsKeptInItsPlace (R-0207): only between this run's
// markers; unknown keys ignored; a line over the cap ignored; a missing
// answer is a failure that says so, not a pass.
func TestTheProbesOutputIsKeptInItsPlace(t *testing.T) {
	now := time.Now()
	out := probeOutput(now, map[string]string{"surprise": "1", "sudo": strings.Repeat("y", maxCheckLine+1)})
	out = "sudo=yes\n" + out // before the marker: a dotfile's, not the probe's
	lines, err := parseCheck(out, testNonce, now)
	if err != nil {
		t.Fatal(err)
	}
	if l := line(t, lines, "sudo"); l.State != CheckFailed {
		t.Fatalf("an overlong or outside answer counted: %+v", l)
	}
	for _, l := range lines {
		if l.ID == "surprise" {
			t.Fatal("an unknown key became a line")
		}
	}
	if _, err := parseCheck("PORTENV-CHECK-BEGIN 0000\nsudo=yes\n", testNonce, now); err == nil {
		t.Fatal("another run's markers were read")
	}
	if _, err := parseCheck(strings.ReplaceAll(probeOutput(now, nil), "PORTENV-CHECK-END "+testNonce+"\n", ""), testNonce, now); err == nil {
		t.Fatal("a probe that didn't finish was read")
	}
}

// TestTheProbeRunsWithoutDotfiles (R-0207): C locale, no profile or rc
// file; the script itself goes on stdin, never in argv.
func TestTheProbeRunsWithoutDotfiles(t *testing.T) {
	argv := checkArgv()
	if strings.Join(argv, " ") != "LC_ALL=C bash --noprofile --norc -s" {
		t.Fatalf("argv %q", argv)
	}
	script := checkScript(testNonce)
	if !strings.Contains(script, "PORTENV-CHECK-BEGIN "+testNonce) || !strings.Contains(script, "PORTENV-CHECK-END "+testNonce) {
		t.Fatal("markers")
	}
	for _, write := range []string{">", "tee ", "mkdir", "touch", "rm ", "apt-get install", "apt install", "dpkg -i", "useradd", "systemctl"} {
		for _, l := range strings.Split(script, "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "#") {
				continue
			}
			if strings.Contains(strings.ReplaceAll(strings.ReplaceAll(l, ">/dev/null", ""), "2>&1", ""), write) && !strings.Contains(l, "check: reads only") {
				t.Errorf("the probe may write (%q): %s", write, l)
			}
		}
	}
}
