// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/portenv/portenv/core/internal/bounded"
)

// A server's host key is pinned in Portenv's own known_hosts (ADR 0021,
// R-0180): found through ssh itself, confirmed once by the person (or by
// this Mac's own SSH known hosts), and checked on every connection. A
// server that isn't confirmed is never contacted, and a changed key is a
// hard stop that only Forget gets past.

// ServerState is where a server's host key stands.
type ServerState string

const (
	// ServerUnconfirmed: no key pinned yet; nothing runs on it.
	ServerUnconfirmed ServerState = "unconfirmed"
	// ServerConfirmed: its key is pinned; every connection checks it.
	ServerConfirmed ServerState = "confirmed"
	// ServerKeyChanged: it answered with a key other than the pinned one,
	// or this Mac's SSH recorded a different one. Nothing runs on it.
	ServerKeyChanged ServerState = "key-changed"
)

// How a key was confirmed.
const (
	ConfirmedFromKnownHosts = "known_hosts" // this Mac's SSH had the same key
	ConfirmedByPerson       = "person"      // Trust, after seeing its fingerprint
	ConfirmedGiven          = "given"       // a script gave the key (portenv app trust-server)
)

// What this Mac's SSH known hosts say about a scanned key.
const (
	KnownHostsNone     = "none"
	KnownHostsMatch    = "match"
	KnownHostsMismatch = "mismatch"
)

// hostKeyAlgorithms is the order a scan asks for host keys in: Ed25519,
// then ECDSA, then RSA signed with SHA-2. Never ssh-rsa (SHA-1).
const hostKeyAlgorithms = "ssh-ed25519,ecdsa-sha2-nistp256,ecdsa-sha2-nistp384,ecdsa-sha2-nistp521,rsa-sha2-512,rsa-sha2-256"

// pinnedAlgorithms is HostKeyAlgorithms for a pinned key of type t: that
// type only, so the server can't offer another.
func pinnedAlgorithms(t string) (string, bool) {
	switch t {
	case "ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		return t, true
	case "ssh-rsa": // the key's type; it signs with SHA-2 only
		return "rsa-sha2-512,rsa-sha2-256", true
	}
	return "", false
}

// ServerRecord is one of this Mac's servers (servers.json).
type ServerRecord struct {
	ID     string      `json:"id"`
	Target string      `json:"target"` // user@host, as ssh is given it
	State  ServerState `json:"state"`
	// Key is the pinned host key, "type base64", when confirmed (and kept
	// when it changed, so ssh reports the change).
	Key           string `json:"key,omitempty"`
	ConfirmedFrom string `json:"confirmed_from,omitempty"`
	// Scanned is the key the last scan found, waiting for Trust.
	Scanned string `json:"scanned,omitempty"`
}

// ServerScan is what ScanServerKey found, for the person to confirm.
type ServerScan struct {
	ID          string
	Name        string
	KeyType     string
	Fingerprint string
	KnownHosts  string
	State       ServerState
}

type serversFile struct {
	Servers []ServerRecord `json:"servers"`
}

// ErrNoSuchServer: the ID isn't one of this Mac's servers.
var ErrNoSuchServer = errors.New("that server isn't one of this Mac's servers")

// serversMu serialises reading and rewriting servers.json.
var serversMu sync.Mutex

func (e *Env) serversPath() string    { return filepath.Join(e.Dir, "servers.json") }
func (e *Env) knownHostsPath() string { return filepath.Join(e.Dir, "known_hosts") }

// loadServers reads servers.json, made once from machine.json's servers
// list. machine.json is left as it was (an older build still reads it) and
// isn't read for servers again.
func (e *Env) loadServers() ([]ServerRecord, error) {
	b, err := os.ReadFile(e.serversPath()) // #nosec G304 -- fixed name in the Portenv directory
	if err == nil {
		var f serversFile
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("servers.json: %w", err)
		}
		return f.Servers, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	mc, err := e.MachineConfig()
	if err != nil {
		return nil, err
	}
	rs := []ServerRecord{}
	for _, t := range mc.Servers {
		if !slices.ContainsFunc(rs, func(r ServerRecord) bool { return r.Target == t }) {
			rs = append(rs, ServerRecord{ID: ServerID(t), Target: t, State: ServerUnconfirmed})
		}
	}
	return rs, e.saveServers(rs)
}

// saveServers writes servers.json and Portenv's known_hosts from it.
func (e *Env) saveServers(rs []ServerRecord) error {
	b, err := json.MarshalIndent(serversFile{Servers: rs}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(e.serversPath(), append(b, '\n')); err != nil {
		return err
	}
	return e.writeKnownHosts(rs)
}

// writeKnownHosts writes Portenv's known_hosts (only its own servers'
// pinned keys, by alias), if it isn't already exactly that.
func (e *Env) writeKnownHosts(rs []ServerRecord) error {
	var b strings.Builder
	for _, r := range rs {
		if r.Key != "" && r.State != ServerUnconfirmed {
			b.WriteString(hostKeyAlias(r.ID) + " " + r.Key + "\n")
		}
	}
	if cur, err := os.ReadFile(e.knownHostsPath()); err == nil && string(cur) == b.String() { // #nosec G304 -- fixed name in the Portenv directory
		return nil
	}
	return writeAtomic(e.knownHostsPath(), []byte(b.String()))
}

func hostKeyAlias(id string) string { return "portenv-" + id }

// ServerRecords is this Mac's servers.
func (e *Env) ServerRecords() ([]ServerRecord, error) {
	serversMu.Lock()
	defer serversMu.Unlock()
	return e.loadServers()
}

// ServerTargets is this Mac's servers (user@host), the Move To targets.
func (e *Env) ServerTargets() ([]string, error) {
	rs, err := e.ServerRecords()
	if err != nil {
		return nil, err
	}
	ts := make([]string, len(rs))
	for i, r := range rs {
		ts[i] = r.Target
	}
	return ts, nil
}

// ServerRecord is the record for target.
func (e *Env) ServerRecord(target string) (ServerRecord, error) {
	rs, err := e.ServerRecords()
	if err != nil {
		return ServerRecord{}, err
	}
	for _, r := range rs {
		if r.Target == target {
			return r, nil
		}
	}
	return ServerRecord{}, fmt.Errorf("%s isn't one of this Mac's servers", ServerDisplayName(target))
}

// updateServer changes the record with this ID under the lock, and saves.
func (e *Env) updateServer(id string, change func(*ServerRecord) error) (ServerRecord, error) {
	serversMu.Lock()
	defer serversMu.Unlock()
	rs, err := e.loadServers()
	if err != nil {
		return ServerRecord{}, err
	}
	i := slices.IndexFunc(rs, func(r ServerRecord) bool { return r.ID == id })
	if i < 0 {
		return ServerRecord{}, ErrNoSuchServer
	}
	if err := change(&rs[i]); err != nil {
		return rs[i], err
	}
	return rs[i], e.saveServers(rs)
}

// Fingerprint is a key's SHA-256 fingerprint as OpenSSH shows it.
func Fingerprint(key string) string {
	_, b64, _ := strings.Cut(strings.TrimSpace(key), " ")
	b64, _, _ = strings.Cut(b64, " ")
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// checkServerKey refuses a server whose key isn't confirmed.
func checkServerKey(r ServerRecord) error {
	name := ServerDisplayName(r.Target)
	switch r.State {
	case ServerConfirmed:
		if _, ok := pinnedAlgorithms(keyType(r.Key)); ok {
			return nil
		}
		return fmt.Errorf("Portenv can't use %s's recorded key. Forget it and add it again.", name) //nolint:staticcheck // shown as is
	case ServerKeyChanged:
		return keyChangedError(name)
	}
	return fmt.Errorf("Confirm %s to open this.", name) //nolint:staticcheck // shown as is (R-0180)
}

func keyChangedError(name string) error {
	return fmt.Errorf("%s's key has changed since it was confirmed, so Portenv won't connect to it. Check with whoever runs it.", name) //nolint:staticcheck // shown as is
}

func keyType(key string) string { t, _, _ := strings.Cut(key, " "); return t }

// CheckServer says whether Portenv may connect to target now, before work
// that would be left half done if it can't.
func (e *Env) CheckServer(target string) error {
	r, err := e.ServerRecord(target)
	if err != nil {
		return err
	}
	return checkServerKey(r)
}

// pinOptions are the ssh options that hold a connection to r's pinned key.
// They come before any other options: ssh keeps the first value it's given.
func (e *Env) pinOptions(r ServerRecord) []string {
	algs, _ := pinnedAlgorithms(keyType(r.Key))
	return []string{
		"-o", "UserKnownHostsFile=" + e.knownHostsPath(),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "CheckHostIP=no",
		"-o", "HostKeyAlias=" + hostKeyAlias(r.ID),
		"-o", "HostKeyAlgorithms=" + algs,
	}
}

// The Env servers are checked against: the daemon's and the CLI's, set by
// NewEnv (tests set their own).
var serversEnv struct {
	sync.Mutex
	e *Env
}

// UseForServers makes e's servers the ones every SSH connection is checked
// against.
func (e *Env) UseForServers() { useForServers(e) }

func useForServers(e *Env) {
	serversEnv.Lock()
	defer serversEnv.Unlock()
	serversEnv.e = e
}

func currentServersEnv() *Env {
	serversEnv.Lock()
	defer serversEnv.Unlock()
	return serversEnv.e
}

// sshArgs is the ssh command for host: the pins, then PORTENV_SSH's own
// options, then the shared connection's. It refuses a server that isn't
// this Mac's or isn't confirmed, so nothing is ever sent to it.
func sshArgs(host string) ([]string, error) {
	e := currentServersEnv()
	if e == nil {
		return nil, errors.New("Portenv hasn't loaded this Mac's servers") //nolint:staticcheck // shown as is
	}
	r, err := e.ServerRecord(host)
	if err != nil {
		return nil, err
	}
	if err := checkServerKey(r); err != nil {
		return nil, err
	}
	serversMu.Lock()
	rs, err := e.loadServers()
	if err == nil {
		err = e.writeKnownHosts(rs) // put back if something removed it
	}
	serversMu.Unlock()
	if err != nil {
		return nil, err
	}
	base := sshCommand()
	args := append([]string{base[0]}, e.pinOptions(r)...)
	args = append(args, base[1:]...)
	// The shared connection only in the checked folder; without it, each
	// run connects on its own.
	if opts, err := controlOptions(r.ID); err == nil {
		args = append(args, opts...)
	}
	return args, nil
}

// sshCommand is ssh, or PORTENV_SSH when set.
func sshCommand() []string {
	if v := os.Getenv("PORTENV_SSH"); v != "" {
		return strings.Fields(v)
	}
	return []string{"ssh"}
}

// noteHostKeyFailure turns ssh's changed-key failure into a hard stop: the
// record is marked, and the error says so plainly. Any other output is
// returned as nil.
func noteHostKeyFailure(host, stderr string) error {
	if !strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED") && !strings.Contains(stderr, "Host key verification failed") {
		return nil
	}
	name := ServerDisplayName(host)
	e := currentServersEnv()
	if e == nil {
		return keyChangedError(name)
	}
	if r, err := e.ServerRecord(host); err == nil {
		_, _ = e.updateServer(r.ID, func(r *ServerRecord) error {
			if r.State == ServerConfirmed {
				r.State = ServerKeyChanged
			}
			return nil
		})
	}
	return keyChangedError(name)
}

// ScanServerKey finds the server's host key through ssh itself (its own
// config, ProxyJump and all; never ssh-keyscan), and checks it against this
// Mac's SSH known hosts, read only: the same key confirms it; a different
// one marks it changed.
func (e *Env) ScanServerKey(ctx context.Context, id string) (ServerScan, error) {
	rs, err := e.ServerRecords()
	if err != nil {
		return ServerScan{}, err
	}
	i := slices.IndexFunc(rs, func(r ServerRecord) bool { return r.ID == id })
	if i < 0 {
		return ServerScan{}, ErrNoSuchServer
	}
	r := rs[i]
	name := ServerDisplayName(r.Target)
	key, err := scanHostKey(ctx, r, name)
	if err != nil {
		return ServerScan{}, err
	}
	resolved, err := resolveHost(ctx, r.Target)
	if err != nil {
		return ServerScan{}, err
	}
	kh, err := knownHostsSays(ctx, resolved, key)
	if err != nil {
		return ServerScan{}, err
	}
	r, err = e.updateServer(id, func(r *ServerRecord) error {
		switch r.State {
		case ServerConfirmed:
			if r.Key != key {
				r.State = ServerKeyChanged
			}
		case ServerUnconfirmed:
			switch kh {
			case KnownHostsMatch:
				r.State, r.Key, r.ConfirmedFrom, r.Scanned = ServerConfirmed, key, ConfirmedFromKnownHosts, ""
			case KnownHostsMismatch:
				r.State, r.Scanned = ServerKeyChanged, ""
			default:
				r.Scanned = key
			}
		}
		return nil
	})
	if err != nil {
		return ServerScan{}, err
	}
	return ServerScan{ID: id, Name: name, KeyType: keyType(key), Fingerprint: Fingerprint(key), KnownHosts: kh, State: r.State}, nil
}

// scanHostKey connects with no way to log in, accepting a new key into a
// file of its own, and reads the key from it. The connection is never
// shared (ControlPath=none), so the key is really exchanged.
func scanHostKey(ctx context.Context, r ServerRecord, name string) (string, error) {
	dir, err := SocketDir()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "scan-*")
	if err != nil {
		return "", err
	}
	file := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(file) }()
	base := sshCommand()
	args := []string{
		"-o", "UserKnownHostsFile=" + file,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UpdateHostKeys=no",
		"-o", "CheckHostIP=no",
		"-o", "HostKeyAlias=" + hostKeyAlias(r.ID),
		"-o", "HostKeyAlgorithms=" + hostKeyAlgorithms,
		"-o", "BatchMode=yes",
		"-o", "PreferredAuthentications=none",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
	}
	args = append(append(args, base[1:]...), "--", r.Target, "true")
	cmd, err := bounded.Command(ctx, sshSetup, base[0], args...)
	if err != nil {
		return "", err
	}
	cmd.Stdin = nil
	hardStop(cmd)
	out, _ := cmd.CombinedOutput() // no way to log in: it fails once the key is in
	b, err := os.ReadFile(file)    // #nosec G304 -- the scan's own file
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fs := strings.Fields(line)
		if len(fs) >= 3 && fs[0] == hostKeyAlias(r.ID) {
			if _, ok := pinnedAlgorithms(fs[1]); !ok {
				return "", fmt.Errorf("%s offered a %s host key, which Portenv doesn't accept", name, fs[1])
			}
			return fs[1] + " " + fs[2], nil
		}
	}
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "-cert-v01@openssh.com") {
		return "", fmt.Errorf("%s offers only a certificate as its host key. Portenv confirms a server's own key, so ask whoever runs it to offer a plain host key too", name)
	}
	if lines := strings.Split(msg, "\n"); msg != "" {
		msg = lines[len(lines)-1]
	} else {
		msg = "no answer"
	}
	return "", fmt.Errorf("couldn't reach %s to read its key: %s", name, msg)
}

// resolvedHost is how this Mac's SSH names a host in its known hosts, and
// where it keeps them (ssh -G).
type resolvedHost struct {
	names []string
	files []string
}

// resolveHost asks ssh how it reaches target (its config applied).
func resolveHost(ctx context.Context, target string) (resolvedHost, error) {
	base := sshCommand()
	cmd, err := bounded.Command(ctx, sshSetup, base[0], append(append(slices.Clone(base[1:]), "-G"), "--", target)...)
	if err != nil {
		return resolvedHost{}, err
	}
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return resolvedHost{}, fmt.Errorf("ssh -G %s: %w", ServerDisplayName(target), err)
	}
	conf := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), " ")
		if _, seen := conf[k]; !seen {
			conf[k] = v
		}
	}
	var h resolvedHost
	switch {
	case conf["hostkeyalias"] != "" && conf["hostkeyalias"] != "none":
		h.names = []string{conf["hostkeyalias"]}
	case conf["port"] == "" || conf["port"] == "22":
		h.names = []string{conf["hostname"]}
	default:
		h.names = []string{"[" + conf["hostname"] + "]:" + conf["port"]}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return resolvedHost{}, err
	}
	files := strings.Fields(conf["userknownhostsfile"])
	if len(files) == 0 {
		files = []string{"~/.ssh/known_hosts", "~/.ssh/known_hosts2"}
	}
	for _, f := range files {
		if strings.HasPrefix(f, "~/") {
			f = filepath.Join(home, f[2:])
		}
		h.files = append(h.files, f)
	}
	return h, nil
}

// knownHostsSays compares key with what this Mac's SSH recorded for the
// host, through ssh-keygen -F on each file, which only reads (it knows
// hashed names too). Another key of the same type is a mismatch; a key of
// another type alone says nothing.
func knownHostsSays(ctx context.Context, h resolvedHost, key string) (string, error) {
	kt, blob, _ := strings.Cut(key, " ")
	match, mismatch := false, false
	for _, f := range h.files {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		for _, n := range h.names {
			cmd, err := bounded.Command(ctx, 10*time.Second, "ssh-keygen", "-F", n, "-f", f)
			if err != nil {
				return "", err
			}
			cmd.Stdin = nil
			out, err := cmd.Output()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				continue // not there
			}
			if err != nil {
				return "", fmt.Errorf("reading this Mac's SSH known hosts: %w", err)
			}
			for _, line := range strings.Split(string(out), "\n") {
				fs := strings.Fields(line)
				if len(fs) == 0 || strings.HasPrefix(fs[0], "#") {
					continue
				}
				marker := ""
				if strings.HasPrefix(fs[0], "@") {
					marker, fs = fs[0], fs[1:]
				}
				if len(fs) < 3 || marker == "@cert-authority" {
					continue
				}
				switch {
				case marker == "@revoked" && fs[1] == kt && fs[2] == blob:
					mismatch = true
				case marker != "":
				case fs[1] == kt && fs[2] == blob:
					match = true
				case fs[1] == kt:
					mismatch = true
				}
			}
		}
	}
	switch {
	case mismatch:
		return KnownHostsMismatch, nil
	case match:
		return KnownHostsMatch, nil
	}
	return KnownHostsNone, nil
}

// TrustServerKey pins the key the last scan found, if its fingerprint is
// the one the person was shown. A changed key can't be trusted: Forget it
// first, then add it again.
func (e *Env) TrustServerKey(id, fingerprint string) error {
	_, err := e.updateServer(id, func(r *ServerRecord) error {
		name := ServerDisplayName(r.Target)
		switch {
		case r.State == ServerKeyChanged:
			return keyChangedError(name)
		case r.State == ServerConfirmed:
			if Fingerprint(r.Key) == fingerprint {
				return nil
			}
			return fmt.Errorf("%s is already confirmed with another key", name)
		case r.Scanned == "" || Fingerprint(r.Scanned) != fingerprint:
			return fmt.Errorf("%s's key isn't the one shown. Check it again", name)
		}
		r.State, r.Key, r.ConfirmedFrom, r.Scanned = ServerConfirmed, r.Scanned, ConfirmedByPerson, ""
		return nil
	})
	return err
}

// ForgetServerKey drops the server's pinned key: it is unconfirmed again,
// and nothing runs on it until it's confirmed.
func (e *Env) ForgetServerKey(id string) error {
	_, err := e.updateServer(id, func(r *ServerRecord) error {
		r.State, r.Key, r.ConfirmedFrom, r.Scanned = ServerUnconfirmed, "", "", ""
		return nil
	})
	return err
}

// PinServerKey adds target, if it isn't one of this Mac's servers yet, with
// key ("type base64") confirmed: for scripts that know the server's key
// from setting it up. It never replaces a different key.
func (e *Env) PinServerKey(target, key string) error {
	fs := strings.Fields(key)
	if len(fs) < 2 || Fingerprint(key) == "" {
		return errors.New(`the key must be "type base64", as in known_hosts`)
	}
	if _, ok := pinnedAlgorithms(fs[0]); !ok {
		return fmt.Errorf("Portenv doesn't accept %s host keys", fs[0]) //nolint:staticcheck // shown as is
	}
	key = fs[0] + " " + fs[1]
	serversMu.Lock()
	defer serversMu.Unlock()
	rs, err := e.loadServers()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rs, func(r ServerRecord) bool { return r.Target == target })
	if i < 0 {
		rs = append(rs, ServerRecord{ID: ServerID(target), Target: target, State: ServerUnconfirmed})
		i = len(rs) - 1
	}
	r := &rs[i]
	switch {
	case r.State == ServerKeyChanged:
		return keyChangedError(ServerDisplayName(target))
	case r.State == ServerConfirmed && r.Key != key:
		return fmt.Errorf("%s is already confirmed with another key", ServerDisplayName(target))
	case r.State == ServerConfirmed:
		return nil
	}
	r.State, r.Key, r.ConfirmedFrom, r.Scanned = ServerConfirmed, key, ConfirmedGiven, ""
	return e.saveServers(rs)
}

// RemoveServer drops target from this Mac's servers, with its pinned key:
// for scripts whose test server comes back at a new address.
func (e *Env) RemoveServer(target string) error {
	serversMu.Lock()
	defer serversMu.Unlock()
	rs, err := e.loadServers()
	if err != nil {
		return err
	}
	return e.saveServers(slices.DeleteFunc(rs, func(r ServerRecord) bool { return r.Target == target }))
}
