// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/docker"
	"github.com/portenv/portenv/core/keys"
	boxsync "github.com/portenv/portenv/core/sync"
)

// defaultImage is the toolbox published by CI; the first resume pins it to
// the digest it resolved to, so a box never changes image silently.
const defaultImage = "ghcr.io/portenv/toolbox-node:main"

// env is this machine's Portenv directory:
//
//	boxes/<name>.json     box configuration (no secrets)
//	machine-id            this machine's ID
//	state/<box-id>/       sync state
//	driver/               docker driver specs
//	keys/                 repository keys (servers; Macs use the Keychain)
//	Repositories/         default "This Mac only" storage
type env struct {
	dir     string
	machine string
}

func newEnv() (*env, error) {
	dir := os.Getenv("PORTENV_HOME")
	if dir == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(cfg, "Portenv")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- the user's own Portenv directory
		return nil, err
	}
	e := &env{dir: dir}
	id, err := e.machineID()
	if err != nil {
		return nil, err
	}
	e.machine = id
	return e, nil
}

var unsafeRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (e *env) machineID() (string, error) {
	p := filepath.Join(e.dir, "machine-id")
	if b, err := os.ReadFile(p); err == nil { // #nosec G304 -- fixed name in the Portenv directory
		return strings.TrimSpace(string(b)), nil
	}
	host, _ := os.Hostname()
	host = strings.Trim(unsafeRE.ReplaceAllString(strings.Split(host, ".")[0], "-"), "-")
	if host == "" {
		host = "machine"
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	id := host + "-" + hex.EncodeToString(b)
	return id, os.WriteFile(p, []byte(id+"\n"), 0o600)
}

// boxConfig is a box's configuration on this machine. It holds no secrets.
type boxConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Storage string `json:"storage"` // "" (this Mac only), an absolute directory, sftp:USER@HOST:/PATH or s3:URL
	// StorageHostKey pins the SFTP server's host key ("ssh-ed25519 AAAA...").
	StorageHostKey string `json:"storage_host_key,omitempty"`
	// StorageREST is restic's REST server on the SFTP server's loopback
	// (127.0.0.1:PORT, set up by server/setup.sh). restic on this machine
	// reaches it through the storage account's SSH connection; backup and
	// restore in the box keep using SFTP.
	StorageREST string `json:"storage_rest,omitempty"`
}

// machineConfig holds this machine's settings (machine.json).
type machineConfig struct {
	// HomesDir is where box home volumes live: the encrypted volume on a
	// server (written by the server setup script).
	HomesDir string `json:"homes_dir,omitempty"`
}

func (e *env) machineConfig() (machineConfig, error) {
	var m machineConfig
	b, err := os.ReadFile(filepath.Join(e.dir, "machine.json")) // #nosec G304 -- fixed name in the Portenv directory
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// storageKeyID names a box's SFTP storage key in the key store.
func storageKeyID(boxID string) string { return boxID + "-storage" }

// restKeyID names the box's REST server password in the key store; the
// REST user is the box ID.
func restKeyID(boxID string) string { return boxID + "-rest" }

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

func (e *env) configPath(name string) (string, error) {
	if !nameRE.MatchString(name) {
		return "", usageError{fmt.Sprintf("invalid box name %q (letters, digits, '.', '_', '-')", name)}
	}
	return filepath.Join(e.dir, "boxes", name+".json"), nil
}

func (e *env) loadBox(name string) (boxConfig, error) {
	p, err := e.configPath(name)
	if err != nil {
		return boxConfig{}, err
	}
	b, err := os.ReadFile(p) // #nosec G304 -- validated name in the Portenv directory
	if errors.Is(err, os.ErrNotExist) {
		return boxConfig{}, fmt.Errorf("no box named %s here; create it with portenv init %s", name, name)
	}
	if err != nil {
		return boxConfig{}, err
	}
	var c boxConfig
	return c, json.Unmarshal(b, &c)
}

func (e *env) keyStore() keys.Store {
	if os.Getenv("PORTENV_KEYS") == "file" { // tests: keep keys out of the Keychain
		return keys.FileStore{Dir: filepath.Join(e.dir, "keys")}
	}
	return keys.Default(filepath.Join(e.dir, "keys"))
}

// storage resolves where the box's repository is, as restic inside the box
// sees it, and which host directory the driver must mount for it.
func (e *env) storage(c boxConfig) (repo, hostDir string, resticEnv []string, err error) {
	switch {
	case c.Storage == "":
		return docker.StorageMount + "/" + c.ID, filepath.Join(e.dir, "Repositories"), nil, nil
	case strings.HasPrefix(c.Storage, "sftp:"):
		if c.StorageHostKey == "" {
			return "", "", nil, errors.New("SFTP storage needs the server's host key (--storage-host-key)")
		}
		return strings.TrimSuffix(c.Storage, "/") + "/boxes/" + c.ID, "", nil, nil
	case strings.HasPrefix(c.Storage, "s3:"):
		for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_DEFAULT_REGION"} {
			if v := os.Getenv(k); v != "" {
				resticEnv = append(resticEnv, k+"="+v)
			}
		}
		return strings.TrimSuffix(c.Storage, "/") + "/boxes/" + c.ID, "", resticEnv, nil
	case filepath.IsAbs(c.Storage):
		return docker.StorageMount + "/boxes/" + c.ID, c.Storage, nil, nil
	}
	return "", "", nil, fmt.Errorf("unsupported storage %q: use an absolute directory, sftp:USER@HOST:/PATH or s3:URL", c.Storage)
}

// session is everything needed to work on one box.
type session struct {
	e         *env
	cfg       boxConfig
	drv       *docker.Driver
	repo      string
	resticEnv []string
	key       []byte
	sshKey    []byte
	// restPassword is the box's REST server password (StorageREST).
	restPassword []byte
}

func (e *env) open(name string) (*session, error) {
	c, err := e.loadBox(name)
	if err != nil {
		return nil, err
	}
	repo, hostDir, resticEnv, err := e.storage(c)
	if err != nil {
		return nil, err
	}
	if hostDir != "" {
		if err := os.MkdirAll(hostDir, 0o700); err != nil { // #nosec G703 -- storage the user configured
			return nil, err
		}
	}
	mc, err := e.machineConfig()
	if err != nil {
		return nil, err
	}
	drv, err := docker.New(docker.Config{
		StateDir: filepath.Join(e.dir, "driver"), StorageDir: hostDir, HomesDir: mc.HomesDir,
		Namespace: os.Getenv("PORTENV_DOCKER_NAMESPACE"),
	})
	if err != nil {
		return nil, err
	}
	key, err := e.keyStore().Get(c.ID)
	if err != nil {
		return nil, fmt.Errorf("repository key for %s: %w", name, err)
	}
	s := &session{e: e, cfg: c, drv: drv, repo: repo, resticEnv: resticEnv, key: key}
	if strings.HasPrefix(c.Storage, "sftp:") {
		if s.sshKey, err = e.keyStore().Get(storageKeyID(c.ID)); err != nil {
			return nil, fmt.Errorf("SFTP storage key for %s: %w", name, err)
		}
	}
	if c.StorageREST != "" {
		if s.restPassword, err = e.keyStore().Get(restKeyID(c.ID)); err != nil {
			return nil, fmt.Errorf("REST storage password for %s: %w", name, err)
		}
	}
	return s, nil
}

func (s *session) id() driver.BoxID { return driver.BoxID(s.cfg.ID) }

// hostRestic finds restic on this machine for the commands that do not need
// a box's /home: PORTENV_RESTIC, then next to the portenv binary, then PATH.
func hostRestic() string {
	if p := os.Getenv("PORTENV_RESTIC"); p != "" {
		return p
	}
	if self, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(self), "restic"); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("restic"); err == nil {
		return p
	}
	return ""
}

// fileExists is used only with paths from the user's own configuration or
// next to the portenv binary.
func fileExists(p string) bool {
	_, err := os.Stat(p) // #nosec G703 -- see above
	return err == nil
}

// hostRepo is the repository's address from this machine (not from inside
// the box): a real path for local storage, and the loopback address for
// SFTP storage on this very server.
func (s *session) hostRepo() string {
	c := s.cfg
	switch {
	case c.Storage == "":
		return filepath.Join(s.e.dir, "Repositories", c.ID)
	case filepath.IsAbs(c.Storage):
		return filepath.Join(c.Storage, "boxes", c.ID)
	}
	if f := s.restForward(); f != nil {
		return "rest:http+unix://" + f.Socket(controlDir()) + ":/" + s.cfg.ID + "/"
	}
	return strings.Replace(s.repo, "host.portenv.internal", "127.0.0.1", 1)
}

// sftpAddr returns host:port of an SFTP repository address, in either
// restic form: sftp:user@host:/path or sftp://user@host:port//path.
func sftpAddr(repo string) string {
	if strings.HasPrefix(repo, "sftp://") {
		if u, err := url.Parse(repo); err == nil && u.Host != "" {
			port := u.Port()
			if port == "" {
				port = "22"
			}
			return net.JoinHostPort(u.Hostname(), port)
		}
		return ""
	}
	rest := strings.TrimPrefix(repo, "sftp:")
	hostPart, _, _ := strings.Cut(rest, ":")
	if i := strings.LastIndex(hostPart, "@"); i >= 0 {
		hostPart = hostPart[i+1:]
	}
	return net.JoinHostPort(hostPart, "22")
}

// hostExecutor runs restic on this machine, or is nil when there is none
// (then everything runs in the box).
func (s *session) hostExecutor() boxsync.Executor {
	bin := hostRestic()
	if bin == "" {
		return nil
	}
	// On Linux, a local storage directory written by restic in the box
	// belongs to portenv-sync (uid 990), which this user cannot read; keep
	// everything in the box there. (Servers use SFTP storage, which one
	// account owns.)
	if runtime.GOOS == "linux" && (s.cfg.Storage == "" || filepath.IsAbs(s.cfg.Storage)) {
		return nil
	}
	return boxsync.LocalExecutor{
		Bin:      bin,
		CacheDir: filepath.Join(s.e.dir, "state", s.cfg.ID, "host-cache"),
		// Short on purpose: SSH control sockets must fit in 104 bytes, which
		// macOS's per-user temporary directory does not leave room for.
		ControlDir: controlDir(),
		REST:       s.restForward(),
	}
}

func controlDir() string { return fmt.Sprintf("/tmp/portenv-%d", os.Getuid()) }

// restForward reaches the box's REST server (StorageREST) through the
// storage account's SSH connection, on this server too (as 127.0.0.1), so
// there is one path.
func (s *session) restForward() *boxsync.RESTForward {
	if s.cfg.StorageREST == "" || len(s.restPassword) == 0 || !strings.HasPrefix(s.cfg.Storage, "sftp:") {
		return nil
	}
	user, host, port := sftpTarget(strings.Replace(s.repo, "host.portenv.internal", "127.0.0.1", 1))
	if host == "" {
		return nil
	}
	return &boxsync.RESTForward{Via: user + "@" + host, Port: port, Remote: s.cfg.StorageREST, User: s.cfg.ID, Password: string(s.restPassword)}
}

// sftpTarget splits an SFTP repository address, in either restic form
// (sftp:user@host:/path or sftp://user@host:port//path), into user, host
// and port (empty for 22).
func sftpTarget(repo string) (user, host, port string) {
	if strings.HasPrefix(repo, "sftp://") {
		u, err := url.Parse(repo)
		if err != nil || u.Host == "" {
			return "", "", ""
		}
		return u.User.Username(), u.Hostname(), u.Port()
	}
	userHost, _, _ := strings.Cut(strings.TrimPrefix(repo, "sftp:"), ":")
	user, host, ok := strings.Cut(userHost, "@")
	if !ok {
		return "", userHost, ""
	}
	return user, host, ""
}

// storageReachable reports whether the storage can be reached within 2 s.
func (s *session) storageReachable() bool {
	c := s.cfg
	var addr string
	switch {
	case strings.HasPrefix(c.Storage, "sftp:"):
		// The SFTP server's address, even when restic on this machine goes
		// through the REST forward (spike).
		if addr = sftpAddr(strings.Replace(s.repo, "host.portenv.internal", "127.0.0.1", 1)); addr == "" {
			return true // unparsable: let restic report the real error
		}
	case strings.HasPrefix(c.Storage, "s3:"):
		u, err := url.Parse(strings.TrimPrefix(c.Storage, "s3:"))
		if err != nil || u.Host == "" {
			return true
		}
		addr = net.JoinHostPort(u.Hostname(), "443")
	case c.Storage == "":
		return fileExists(filepath.Join(s.e.dir, "Repositories"))
	default:
		// A local directory is reachable when its root exists (an
		// unmounted external drive is not); the boxes folder comes later.
		return fileExists(c.Storage) // #nosec G703 -- the user's own configured storage directory
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second) // #nosec G704 -- the user's own configured storage server
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// sync opens the sync engine: backup and restore run inside the box,
// everything else on this machine when it has restic.
func (s *session) sync() (*boxsync.Box, error) {
	return boxsync.Open(boxsync.Config{
		MetaExecutor:   s.hostExecutor(),
		MetaRepository: s.hostRepo(),
		Executor:       boxsync.AgentExecutor{Driver: s.drv, Box: s.id()},
		Repository:     s.repo,
		Password:       s.key,
		Env:            s.resticEnv,
		SSHKey:         s.sshKey,
		SSHHostKey:     s.cfg.StorageHostKey,
		BoxID:          s.cfg.ID,
		MachineID:      s.e.machine,
		HomeDir:        "/home",
		ExcludeFile:    "/home/work/.portenv/excludes",
		StateDir:       filepath.Join(s.e.dir, "state", s.cfg.ID),
		Trace:          trace,
	})
}

// trace prints a phase's duration when PORTENV_TRACE=1.
func trace(phase string, d time.Duration) {
	if os.Getenv("PORTENV_TRACE") == "1" {
		fmt.Fprintf(os.Stderr, "trace  %-28s %6.2f s\n", phase, d.Seconds())
	}
}

// timed runs fn and traces its duration.
func timed(phase string, fn func() error) error {
	start := time.Now()
	err := fn()
	trace(phase, time.Since(start))
	return err
}

func homeVolume(boxID string) string {
	if ns := os.Getenv("PORTENV_DOCKER_NAMESPACE"); ns != "" {
		return "portenv-home-" + ns + "-" + boxID
	}
	return "portenv-home-" + boxID
}

// waitAgent waits until the agent reports one of the wanted states and
// returns the state and detail. It polls quickly at first (a box is usually
// ready within a second), then backs off to once a second.
func waitAgent(ctx context.Context, d driver.Driver, id driver.BoxID, want ...string) (string, string, error) {
	deadline := time.Now().Add(15 * time.Minute)
	last := ""
	wait := 50 * time.Millisecond
	for time.Now().Before(deadline) {
		res, err := d.Exec(ctx, id, driver.ExecRequest{Argv: []string{"portenv-agent", "ready"}, Timeout: 10 * time.Second})
		if err == nil {
			out := strings.TrimSpace(string(res.Stdout))
			state, detail, _ := strings.Cut(out, ": ")
			state = strings.TrimPrefix(state, "READINESS_STATE_")
			for _, w := range want {
				if state == w {
					return state, detail, nil
				}
			}
			if out != last && out != "" {
				fmt.Fprintf(os.Stderr, "  %s\n", strings.ToLower(detail))
				last = out
			}
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Second)
	}
	return "", "", errors.New("the box did not finish starting within 15 minutes")
}

func (e *env) saveBox(c boxConfig) error {
	p, err := e.configPath(c.Name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
