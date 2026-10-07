// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/docker"
	"github.com/portenv/portenv/core/keys"
	boxsync "github.com/portenv/portenv/core/sync"
)

const defaultImage = "ghcr.io/portenv/toolbox-node:v1"

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
	return s, nil
}

func (s *session) id() driver.BoxID { return driver.BoxID(s.cfg.ID) }

// sync opens the sync engine, running restic inside the box. The box must be
// running.
func (s *session) sync() (*boxsync.Box, error) {
	return boxsync.Open(boxsync.Config{
		Executor:    boxsync.AgentExecutor{Driver: s.drv, Box: s.id()},
		Repository:  s.repo,
		Password:    s.key,
		Env:         s.resticEnv,
		SSHKey:      s.sshKey,
		SSHHostKey:  s.cfg.StorageHostKey,
		BoxID:       s.cfg.ID,
		MachineID:   s.e.machine,
		HomeDir:     "/home",
		ExcludeFile: "/home/work/.portenv/excludes",
		StateDir:    filepath.Join(s.e.dir, "state", s.cfg.ID),
	})
}

func homeVolume(boxID string) string {
	if ns := os.Getenv("PORTENV_DOCKER_NAMESPACE"); ns != "" {
		return "portenv-home-" + ns + "-" + boxID
	}
	return "portenv-home-" + boxID
}

// waitAgent waits until the agent reports one of the wanted states and
// returns the state and detail.
func waitAgent(ctx context.Context, d driver.Driver, id driver.BoxID, want ...string) (string, string, error) {
	deadline := time.Now().Add(15 * time.Minute)
	last := ""
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
		case <-time.After(time.Second):
		}
	}
	return "", "", errors.New("the box did not finish starting within 15 minutes")
}
