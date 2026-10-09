// SPDX-License-Identifier: Apache-2.0

package local

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

// DefaultImage is the toolbox published by CI; the first resume pins it to
// the digest it resolved to, so a box never changes image silently.
const DefaultImage = "ghcr.io/portenv/toolbox-node:main"

// Env is this machine's Portenv directory:
//
//	boxes/<name>.json     box configuration (no secrets)
//	machine-id            this machine's ID
//	state/<box-id>/       sync state
//	driver/               docker driver specs
//	keys/                 repository keys (servers; Macs use the Keychain)
//	Repositories/         default "This Mac only" storage
type Env struct {
	Dir     string
	Machine string
	// Provided holds keys the app read from the Keychain and handed to
	// portenvd (in memory only); they are used before the store's own.
	Provided *keys.Memory
}

func NewEnv() (*Env, error) {
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
	e := &Env{Dir: dir}
	id, err := e.machineID()
	if err != nil {
		return nil, err
	}
	e.Machine = id
	return e, nil
}

var unsafeRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (e *Env) machineID() (string, error) {
	p := filepath.Join(e.Dir, "machine-id")
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

// BoxConfig is a box's configuration on this machine. It holds no secrets.
type BoxConfig struct {
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

// MachineConfig holds this machine's settings (machine.json).
type MachineConfig struct {
	// HomesDir is where box home volumes live: the encrypted volume on a
	// server (written by the server setup script).
	HomesDir string `json:"homes_dir,omitempty"`
	// Servers are the SSH hosts (user@host, running portenv) offered in
	// Move To. Phase 2's Add a Server replaces this list.
	Servers []string `json:"servers,omitempty"`
}

func (e *Env) MachineConfig() (MachineConfig, error) {
	var m MachineConfig
	b, err := os.ReadFile(filepath.Join(e.Dir, "machine.json")) // #nosec G304 -- fixed name in the Portenv directory
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// StorageKeyID names a box's SFTP storage key in the key store.
func StorageKeyID(boxID string) string { return boxID + "-storage" }

// RESTKeyID names the box's REST server password in the key store; the
// REST user is the box ID.
func RESTKeyID(boxID string) string { return boxID + "-rest" }

// UsageError is a mistake in how a command was called.
type UsageError struct{ Msg string }

func (u UsageError) Error() string { return u.Msg }

// NameRE is a valid box name.
var NameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

func (e *Env) ConfigPath(name string) (string, error) {
	if !NameRE.MatchString(name) {
		return "", UsageError{Msg: fmt.Sprintf("invalid box name %q (letters, digits, '.', '_', '-')", name)}
	}
	return filepath.Join(e.Dir, "boxes", name+".json"), nil
}

func (e *Env) LoadBox(name string) (BoxConfig, error) {
	p, err := e.ConfigPath(name)
	if err != nil {
		return BoxConfig{}, err
	}
	b, err := os.ReadFile(p) // #nosec G304 -- validated name in the Portenv directory
	if errors.Is(err, os.ErrNotExist) {
		return BoxConfig{}, fmt.Errorf("no box named %s here; create it with portenv init %s", name, name)
	}
	if err != nil {
		return BoxConfig{}, err
	}
	var c BoxConfig
	return c, json.Unmarshal(b, &c)
}

func (e *Env) KeyStore() keys.Store {
	base := keys.Default(filepath.Join(e.Dir, "keys"))
	if os.Getenv("PORTENV_KEYS") == "file" { // tests: keep keys out of the Keychain
		base = keys.FileStore{Dir: filepath.Join(e.Dir, "keys")}
	}
	if e.Provided != nil {
		return keys.Layered{First: e.Provided, Then: base}
	}
	return base
}

// KeyIDs are the key IDs a box's keys are stored under: its repository key,
// and for SFTP storage its storage key and REST password.
func KeyIDs(c BoxConfig) []string {
	ids := []string{c.ID}
	if strings.HasPrefix(c.Storage, "sftp:") {
		ids = append(ids, StorageKeyID(c.ID))
		if c.StorageREST != "" {
			ids = append(ids, RESTKeyID(c.ID))
		}
	}
	return ids
}

// storage resolves where the box's repository is, as restic inside the box
// sees it, and which host directory the driver must mount for it.
func (e *Env) Storage(c BoxConfig) (repo, hostDir string, resticEnv []string, err error) {
	switch {
	case c.Storage == "":
		return docker.StorageMount + "/" + c.ID, filepath.Join(e.Dir, "Repositories"), nil, nil
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

// Session is everything needed to work on one box.
type Session struct {
	E         *Env
	Cfg       BoxConfig
	Drv       *docker.Driver
	Repo      string
	ResticEnv []string
	Key       []byte
	SSHKey    []byte
	// restPassword is the box's REST server password (StorageREST).
	RESTPassword []byte
}

func (e *Env) Open(name string) (*Session, error) {
	c, err := e.LoadBox(name)
	if err != nil {
		return nil, err
	}
	repo, hostDir, resticEnv, err := e.Storage(c)
	if err != nil {
		return nil, err
	}
	if hostDir != "" {
		if err := os.MkdirAll(hostDir, 0o700); err != nil { // #nosec G703 -- storage the user configured
			return nil, err
		}
	}
	mc, err := e.MachineConfig()
	if err != nil {
		return nil, err
	}
	drv, err := docker.New(docker.Config{
		StateDir: filepath.Join(e.Dir, "driver"), StorageDir: hostDir, HomesDir: mc.HomesDir,
		Namespace: os.Getenv("PORTENV_DOCKER_NAMESPACE"),
	})
	if err != nil {
		return nil, err
	}
	key, err := e.KeyStore().Get(c.ID)
	if err != nil {
		return nil, fmt.Errorf("repository key for %s: %w", name, err)
	}
	s := &Session{E: e, Cfg: c, Drv: drv, Repo: repo, ResticEnv: resticEnv, Key: key}
	if strings.HasPrefix(c.Storage, "sftp:") {
		if s.SSHKey, err = e.KeyStore().Get(StorageKeyID(c.ID)); err != nil {
			return nil, fmt.Errorf("SFTP storage key for %s: %w", name, err)
		}
	}
	if c.StorageREST != "" {
		if s.RESTPassword, err = e.KeyStore().Get(RESTKeyID(c.ID)); err != nil {
			return nil, fmt.Errorf("REST storage password for %s: %w", name, err)
		}
	}
	return s, nil
}

func (s *Session) ID() driver.BoxID { return driver.BoxID(s.Cfg.ID) }

// HostRestic finds restic on this machine for the commands that do not need
// a box's /home: PORTENV_RESTIC, then next to the portenv binary, then PATH.
func HostRestic() string {
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
func (s *Session) HostRepo() string {
	c := s.Cfg
	switch {
	case c.Storage == "":
		return filepath.Join(s.E.Dir, "Repositories", c.ID)
	case filepath.IsAbs(c.Storage):
		return filepath.Join(c.Storage, "boxes", c.ID)
	}
	if f := s.RESTForward(); f != nil {
		return "rest:http+unix://" + f.Socket(ControlDir()) + ":/" + s.Cfg.ID + "/"
	}
	return strings.Replace(s.Repo, "host.portenv.internal", "127.0.0.1", 1)
}

// SFTPAddr returns host:port of an SFTP repository address, in either
// restic form: sftp:user@host:/path or sftp://user@host:port//path.
func SFTPAddr(repo string) string {
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
func (s *Session) HostExecutor() boxsync.Executor {
	bin := HostRestic()
	if bin == "" {
		return nil
	}
	// On Linux, a local storage directory written by restic in the box
	// belongs to portenv-sync (uid 990), which this user cannot read; keep
	// everything in the box there. (Servers use SFTP storage, which one
	// account owns.)
	if runtime.GOOS == "linux" && (s.Cfg.Storage == "" || filepath.IsAbs(s.Cfg.Storage)) {
		return nil
	}
	return boxsync.LocalExecutor{
		Bin:      bin,
		CacheDir: filepath.Join(s.E.Dir, "state", s.Cfg.ID, "host-cache"),
		// Short on purpose: SSH control sockets must fit in 104 bytes, which
		// macOS's per-user temporary directory does not leave room for.
		ControlDir: ControlDir(),
		REST:       s.RESTForward(),
	}
}

func ControlDir() string { return fmt.Sprintf("/tmp/portenv-%d", os.Getuid()) }

// restForward reaches the box's REST server (StorageREST) through the
// storage account's SSH connection, on this server too (as 127.0.0.1), so
// there is one path.
func (s *Session) RESTForward() *boxsync.RESTForward {
	if s.Cfg.StorageREST == "" || len(s.RESTPassword) == 0 || !strings.HasPrefix(s.Cfg.Storage, "sftp:") {
		return nil
	}
	user, host, port := SFTPTarget(strings.Replace(s.Repo, "host.portenv.internal", "127.0.0.1", 1))
	if host == "" {
		return nil
	}
	return &boxsync.RESTForward{Via: user + "@" + host, Port: port, Remote: s.Cfg.StorageREST, User: s.Cfg.ID, Password: string(s.RESTPassword)}
}

// SFTPTarget splits an SFTP repository address, in either restic form
// (sftp:user@host:/path or sftp://user@host:port//path), into user, host
// and port (empty for 22).
func SFTPTarget(repo string) (user, host, port string) {
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
func (s *Session) StorageReachable() bool {
	c := s.Cfg
	var addr string
	switch {
	case strings.HasPrefix(c.Storage, "sftp:"):
		// The SFTP server's address, even when restic on this machine goes
		// through the REST forward (spike).
		if addr = SFTPAddr(strings.Replace(s.Repo, "host.portenv.internal", "127.0.0.1", 1)); addr == "" {
			return true // unparsable: let restic report the real error
		}
	case strings.HasPrefix(c.Storage, "s3:"):
		u, err := url.Parse(strings.TrimPrefix(c.Storage, "s3:"))
		if err != nil || u.Host == "" {
			return true
		}
		addr = net.JoinHostPort(u.Hostname(), "443")
	case c.Storage == "":
		return fileExists(filepath.Join(s.E.Dir, "Repositories"))
	default:
		// A local directory is reachable when its root exists (an
		// unmounted external drive is not); the boxes folder comes later.
		return fileExists(c.Storage) // #nosec G703 -- the user's own configured storage directory
	}
	// Short: a reachable server answers within a second. "No network at
	// all" never gets here on the Mac (the app reports it; no probe).
	conn, err := net.DialTimeout("tcp", addr, time.Second) // #nosec G704 -- the user's own configured storage server
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// sync opens the sync engine: backup and restore run inside the box,
// everything else on this machine when it has restic.
func (s *Session) Sync() (*boxsync.Box, error) {
	return s.SyncWith(boxsync.AgentExecutor{Driver: s.Drv, Box: s.ID()})
}

// SyncWith opens the sync engine with in-box runs going through ex: the
// agent channel for portenvd (ADR 0010), docker exec for the Phase 0 CLI.
func (s *Session) SyncWith(ex boxsync.Executor) (*boxsync.Box, error) {
	return boxsync.Open(boxsync.Config{
		MetaExecutor:   s.HostExecutor(),
		MetaRepository: s.HostRepo(),
		Executor:       ex,
		Repository:     s.Repo,
		Password:       s.Key,
		Env:            s.ResticEnv,
		SSHKey:         s.SSHKey,
		SSHHostKey:     s.Cfg.StorageHostKey,
		BoxID:          s.Cfg.ID,
		MachineID:      s.E.Machine,
		HomeDir:        "/home",
		ExcludeFile:    "/home/work/.portenv/excludes",
		StateDir:       filepath.Join(s.E.Dir, "state", s.Cfg.ID),
		Trace:          Trace,
		Deadlines:      testDeadlines(),
	})
}

// testDeadlines shortens restic's deadlines for end-to-end tests only
// (PORTENV_TEST_RESTIC_DEADLINES, one duration for every command), so a
// test of a hung run finishes in seconds. Unset: the defaults.
func testDeadlines() boxsync.Deadlines {
	d, err := time.ParseDuration(os.Getenv("PORTENV_TEST_RESTIC_DEADLINES"))
	if err != nil || d <= 0 {
		return boxsync.Deadlines{}
	}
	return boxsync.Deadlines{Short: d, Check: d, Base: d, BytesPerSecond: 1 << 20}
}

// Trace prints a phase's duration when PORTENV_TRACE=1.
func Trace(phase string, d time.Duration) {
	if os.Getenv("PORTENV_TRACE") == "1" {
		fmt.Fprintf(os.Stderr, "Trace  %-28s %6.2f s\n", phase, d.Seconds())
	}
}

// Timed runs fn and traces its duration.
func Timed(phase string, fn func() error) error {
	start := time.Now()
	err := fn()
	Trace(phase, time.Since(start))
	return err
}

func HomeVolume(boxID string) string {
	if ns := os.Getenv("PORTENV_DOCKER_NAMESPACE"); ns != "" {
		return "portenv-home-" + ns + "-" + boxID
	}
	return "portenv-home-" + boxID
}

// WaitAgent waits until the agent reports one of the wanted states and
// returns the state and detail. It polls quickly at first (a box is usually
// ready within a second), then backs off to once a second.
func WaitAgent(ctx context.Context, d driver.Driver, id driver.BoxID, want ...string) (string, string, error) {
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

func (e *Env) SaveBox(c BoxConfig) error {
	p, err := e.ConfigPath(c.Name)
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

// FromRegistry reports whether an image reference names a registry host
// ("ghcr.io/portenv/toolbox-node:main"), as opposed to a local image
// ("portenv/toolbox-node:dev").
func FromRegistry(ref string) bool {
	first, _, ok := strings.Cut(ref, "/")
	return ok && (strings.ContainsAny(first, ".:") || first == "localhost")
}

// LoopbackAddr reports whether addr is 127.0.0.1:PORT: the REST server must
// never listen anywhere a network can reach.
func LoopbackAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	return err == nil && host == "127.0.0.1" && port != ""
}

// EnsureCreated creates the box's instance on this machine if the driver
// does not know it yet: a registry image is pinned to the digest it
// resolved to (so every machine runs exactly that image), and the home
// storage is attached. notify reports what it did.
func (s *Session) EnsureCreated(ctx context.Context, notify func(string)) error {
	id := s.ID()
	if s.Drv.Known(id) {
		return nil
	}
	if _, err := s.Drv.Create(ctx, driver.Box{ID: id, Name: s.Cfg.Name, ToolboxImage: s.Cfg.Image}); err != nil {
		return err
	}
	// Local development images stay as they are: their digest exists only
	// on this machine.
	if FromRegistry(s.Cfg.Image) {
		pinned, err := s.Drv.ImageDigest(ctx, s.Cfg.Image)
		if err != nil {
			return err
		}
		if pinned != s.Cfg.Image {
			s.Cfg.Image = pinned
			if err := s.E.SaveBox(s.Cfg); err != nil {
				return err
			}
			notify("toolbox pinned to " + pinned)
		}
	}
	return s.Drv.MountHome(ctx, id, driver.HomeStorage{Ref: HomeVolume(s.Cfg.ID)})
}

// Restart stops the box and starts it again, with a fresh home from the
// image's skeleton when fresh is set, so its start sequence runs on the
// current home.
func (s *Session) Restart(ctx context.Context, fresh bool) error {
	if _, err := s.Drv.Stop(ctx, s.ID(), 0); err != nil {
		return err
	}
	if fresh {
		if err := s.Drv.MountHome(ctx, s.ID(), driver.HomeStorage{Ref: HomeVolume(s.Cfg.ID), Fresh: true}); err != nil {
			return err
		}
	}
	_, err := s.Drv.Start(ctx, s.ID())
	return err
}
