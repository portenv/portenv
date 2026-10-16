// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// Add a Server's Check (ADR 0021 §3, R-0207): a fixed read-only probe, run
// over the pinned SSH connection, read into plain lines. Each failure says
// what to do; none carries the server's own words. Nothing installs until
// every check passes.

//go:embed check.sh
var checkSH string

// CheckState is one line's outcome.
type CheckState int

const (
	CheckPassed CheckState = iota + 1
	CheckFailed
	CheckInfo // recorded, never a failure (KVM, an existing install)
)

// CheckLine answers one question.
type CheckLine struct {
	ID     string
	Title  string
	State  CheckState
	Detail string // what Portenv found, in its own words
	Fix    string // for a failure: what to do, one plain line
}

// The minimums setup.sh holds a fresh install to.
const (
	minMemKB      = 1800 * 1024
	minDiskFreeKB = 15 * 1024 * 1024
	maxClockSkew  = 2 * time.Minute
	// maxCheckLine caps each line of the probe's output; longer ones are
	// ignored. maxCheckOutput caps all of it.
	maxCheckLine   = 256
	maxCheckOutput = 64 << 10
)

// checkArgv is the remote command: the C locale and no profile or rc
// file, so a server's dotfiles can't change the probe's output.
func checkArgv() []string { return []string{"LC_ALL=C", "bash", "--noprofile", "--norc", "-s"} }

// checkScript is the probe with this run's markers.
func checkScript(nonce string) string { return strings.ReplaceAll(checkSH, "__NONCE__", nonce) }

// errNoAnswer: the probe's markers weren't both there.
var errNoAnswer = errors.New("the server didn't answer the check")

// CheckServer runs the probe on target and reads its answers.
func CheckServer(ctx context.Context, target string) ([]CheckLine, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(b)
	cmd, err := Remote(ctx, QuickRemote, target, checkArgv()...)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = strings.NewReader(checkScript(nonce))
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &capped{w: &out, left: maxCheckOutput}, &capped{w: &errb, left: maxCheckOutput}
	runErr := cmd.Run()
	if kerr := noteHostKeyFailure(target, errb.String()); kerr != nil {
		return nil, kerr
	}
	lines, err := parseCheck(out.String(), nonce, time.Now())
	if err != nil {
		if runErr != nil {
			// ssh's own failure; never the probe's output.
			return nil, errors.New("Portenv couldn't run its check on " + ServerDisplayName(target) + ". Check you can sign in to it, then choose Check Again.")
		}
		return nil, err
	}
	return lines, nil
}

// capped keeps at most left bytes.
type capped struct {
	w    io.Writer
	left int
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if c.left <= 0 {
		return n, nil
	}
	if len(p) > c.left {
		p = p[:c.left]
	}
	c.left -= len(p)
	_, err := c.w.Write(p)
	return n, err
}

// parseCheck reads the answers between this run's markers into lines.
func parseCheck(out, nonce string, now time.Time) ([]CheckLine, error) {
	kv := map[string]string{}
	in, done := false, false
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 4096), maxCheckOutput)
	for sc.Scan() {
		l := sc.Text()
		switch l {
		case "PORTENV-CHECK-BEGIN " + nonce:
			in = true
			continue
		case "PORTENV-CHECK-END " + nonce:
			done = in
			in = false
			continue
		}
		if !in || len(l) > maxCheckLine {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && knownCheckKey[k] {
			kv[k] = v
		}
	}
	if !done {
		return nil, errNoAnswer
	}
	return checkLines(kv, now), nil
}

var knownCheckKey = map[string]bool{
	"os_id": true, "os_version": true, "arch": true, "mem_kb": true, "disk_free_kb": true, "user_root": true, "sudo": true,
	"clock": true, "https_ghcr": true, "https_docker": true, "docker": true, "apt": true, "portenv": true, "kvm": true,
}

func checkLines(kv map[string]string, now time.Time) []CheckLine {
	var lines []CheckLine
	add := func(id, title string, ok bool, detail, fix string) {
		st := CheckPassed
		if !ok {
			st = CheckFailed
		}
		lines = append(lines, CheckLine{ID: id, Title: title, State: st, Detail: detail, Fix: fix})
	}

	// The system: what setup.sh installs on (ADR 0021 §3).
	osOK := kv["os_id"] == "ubuntu" && (kv["os_version"] == "22.04" || kv["os_version"] == "24.04")
	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[kv["arch"]]
	switch {
	case !osOK:
		add("system", "System", false, "", "Portenv sets up servers running Ubuntu 22.04 or 24.04 for now. Choose a server with one of those, then choose Check Again.")
	case arch == "":
		add("system", "System", false, "", "Portenv needs an arm64 or amd64 (x86-64) server. Choose a server with one of those, then choose Check Again.")
	default:
		add("system", "System", true, "Ubuntu "+kv["os_version"]+", "+arch, "")
	}

	mem, memErr := strconv.ParseInt(kv["mem_kb"], 10, 64)
	add("memory", "Memory", memErr == nil && mem >= minMemKB, sizeKB(mem, memErr),
		"Portenv needs at least 2 GB of memory. Choose a bigger server, then choose Check Again.")
	disk, diskErr := strconv.ParseInt(kv["disk_free_kb"], 10, 64)
	add("disk", "Free disk", diskErr == nil && disk >= minDiskFreeKB, sizeKB(disk, diskErr)+" free",
		"Portenv needs at least 15 GB free on the server's main disk. Free some space or choose a bigger disk, then choose Check Again.")

	root := kv["user_root"] == "yes"
	switch {
	case root:
		add("sudo", "Administrator rights", true, "signed in as root", "")
	default:
		add("sudo", "Administrator rights", kv["sudo"] == "yes", "sudo without a password",
			"Portenv needs root, or sudo without a password, to set the server up; it never asks for or keeps a password. Add this user to sudoers with NOPASSWD, or sign in as root, then choose Check Again.")
	}

	clock, clockErr := strconv.ParseInt(kv["clock"], 10, 64)
	skew := now.Sub(time.Unix(clock, 0))
	if skew < 0 {
		skew = -skew
	}
	add("clock", "Clock", clockErr == nil && skew <= maxClockSkew, "within 2 minutes of this Mac's",
		"The server's clock is more than 2 minutes off this Mac's. Turn on time sync on the server (timedatectl set-ntp true), then choose Check Again.")

	add("https-ghcr", "Reaching ghcr.io", kv["https_ghcr"] == "yes", "outgoing HTTPS",
		"Portenv downloads the workspace toolbox from ghcr.io over HTTPS. Allow outgoing HTTPS (port 443) in your provider's firewall, then choose Check Again.")
	docker := kv["docker"]
	if docker != "" {
		lines = append(lines, CheckLine{ID: "https-docker", Title: "Reaching download.docker.com", State: CheckInfo, Detail: "not needed: Docker is already here"})
	} else {
		add("https-docker", "Reaching download.docker.com", kv["https_docker"] == "yes", "outgoing HTTPS",
			"Portenv installs Docker Engine from download.docker.com over HTTPS. Allow outgoing HTTPS (port 443) in your provider's firewall, then choose Check Again.")
	}
	switch {
	case docker != "" && plainVersion(docker):
		add("docker", "Docker Engine", true, "Docker "+docker+" is here", "")
	case docker != "":
		add("docker", "Docker Engine", true, "Docker is here", "")
	default:
		add("docker", "Docker Engine", kv["apt"] == "yes", "Portenv installs it",
			"Portenv installs Docker Engine with apt, which this server doesn't have. Choose an Ubuntu server, then choose Check Again.")
	}

	if v := kv["portenv"]; v != "" {
		lines = append(lines, CheckLine{ID: "portenv", Title: "Portenv", State: CheckInfo, Detail: "already installed; Set Up updates it"})
	} else {
		lines = append(lines, CheckLine{ID: "portenv", Title: "Portenv", State: CheckInfo, Detail: "not installed yet"})
	}
	kvm := "not available (fine for now)"
	if kv["kvm"] == "yes" {
		kvm = "available"
	}
	lines = append(lines, CheckLine{ID: "kvm", Title: "KVM", State: CheckInfo, Detail: kvm})
	return lines
}

// AllChecksPassed: nothing failed. Only then does Set Up install anything.
func AllChecksPassed(lines []CheckLine) bool {
	for _, l := range lines {
		if l.State == CheckFailed {
			return false
		}
	}
	return len(lines) > 0
}

func sizeKB(kb int64, err error) string {
	if err != nil {
		return "unknown"
	}
	return strconv.FormatFloat(float64(kb)/(1024*1024), 'f', 1, 64) + " GB"
}

// plainVersion: digits and dots only, safe to show.
func plainVersion(v string) bool {
	if v == "" || len(v) > 32 {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}
