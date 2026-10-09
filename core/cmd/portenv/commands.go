// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
)

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usageError{Msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{Msg: fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
	}
	return nil
}

func cmdInit(_ context.Context, e *local.Env, name string, args []string) error {
	fs := flags("init")
	image := fs.String("image", local.DefaultImage, "toolbox image")
	storage := fs.String("storage", "", "storage: an absolute directory, sftp:USER@HOST:/PATH or s3:URL (default: this Mac only)")
	hostKey := fs.String("storage-host-key", "", "SFTP storage: the server's host key, \"ssh-ed25519 AAAA...\"")
	rest := fs.String("storage-rest", "", "SFTP storage: restic's REST server on the server's loopback, 127.0.0.1:PORT (server/setup.sh sets it up)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *rest != "" && (!strings.HasPrefix(*storage, "sftp:") || !local.LoopbackAddr(*rest)) {
		return usageError{Msg: "--storage-rest needs SFTP storage and a loopback address (127.0.0.1:PORT)"}
	}
	p, err := e.ConfigPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil { // #nosec G703 -- box names are validated by local.NameRE
		return fmt.Errorf("box %s already exists", name)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	c := local.BoxConfig{ID: "box-" + hex.EncodeToString(b), Name: name, Image: *image, Storage: *storage, StorageHostKey: *hostKey, StorageREST: *rest}
	if _, _, _, err := e.Storage(c); err != nil {
		return err
	}
	key, err := keys.NewKey()
	if err != nil {
		return err
	}
	if err := e.KeyStore().Put(c.ID, key); err != nil {
		return err
	}
	var storagePub string
	if strings.HasPrefix(c.Storage, "sftp:") {
		priv, pub, err := keys.NewSSHKey("portenv storage " + c.ID)
		if err != nil {
			return err
		}
		if err := e.KeyStore().Put(local.StorageKeyID(c.ID), priv); err != nil {
			return err
		}
		storagePub = pub
	}
	var restUser string
	if c.StorageREST != "" {
		// 192 random bits, as hex: within bcrypt's 72-byte limit.
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		pw := []byte(hex.EncodeToString(raw))
		if err := e.KeyStore().Put(local.RESTKeyID(c.ID), pw); err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword(pw, bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		restUser = c.ID + ":" + string(hash)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil { // #nosec G703 -- box names are validated by local.NameRE
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil { // #nosec G703 -- box names are validated by local.NameRE
		return err
	}
	fmt.Printf("created box %s (%s) on %s\n", name, c.ID, e.Machine)
	if storagePub != "" {
		fmt.Printf("add this key to the storage account's authorized_keys on the server:\n%s\n", storagePub)
	}
	if restUser != "" {
		// Only the bcrypt hash leaves this machine.
		fmt.Printf("add this REST user on the server (setup.sh --rest-user):\nrest-user %s\n", restUser)
	}
	fmt.Printf("next: open it in Portenv (PORTENV_BOX=%s), or portenv app open %s\n", name, name)
	return nil
}

// cmdJoin enrols an existing box on this machine: the repository key comes
// on stdin (from Move To, over SSH) and goes only into the key store.
func cmdJoin(ctx context.Context, e *local.Env, name string, args []string) error {
	fs := flags("join")
	id := fs.String("id", "", "the box's ID")
	image := fs.String("image", local.DefaultImage, "toolbox image")
	storage := fs.String("storage", "", "where this machine reaches the box's storage: an absolute directory, sftp:USER@HOST:/PATH or s3:URL")
	hostKey := fs.String("storage-host-key", "", "SFTP storage: the server's host key")
	rest := fs.String("storage-rest", "", "SFTP storage: restic's REST server on the server's loopback (password on stdin)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *rest != "" && !local.LoopbackAddr(*rest) {
		return usageError{Msg: "--storage-rest needs a loopback address (127.0.0.1:PORT)"}
	}
	if !local.NameRE.MatchString(*id) {
		return usageError{Msg: "join needs --id BOX-ID"}
	}
	p, err := e.ConfigPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil { // #nosec G703 -- box names are validated by local.NameRE
		return fmt.Errorf("box %s already exists here", name)
	}
	c := local.BoxConfig{ID: *id, Name: name, Image: *image, Storage: *storage, StorageHostKey: *hostKey, StorageREST: *rest}
	if _, _, _, err := e.Storage(c); err != nil {
		return err
	}
	// The keys arrive on stdin as JSON (Move To sends them over SSH);
	// they go only into this machine's key store.
	var in local.JoinKeys
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 16<<10)).Decode(&in); err != nil {
		return fmt.Errorf("join reads its keys from stdin as JSON: %w", err)
	}
	if len(in.RepositoryKey) < 32 {
		return errors.New("join needs the repository key on stdin")
	}
	if strings.HasPrefix(c.Storage, "sftp:") && in.StorageKey == "" {
		return errors.New("SFTP storage needs the storage key on stdin")
	}
	if in.StorageKey != "" {
		if err := e.KeyStore().Put(local.StorageKeyID(c.ID), []byte(in.StorageKey)); err != nil {
			return err
		}
	}
	if c.StorageREST != "" {
		if in.RESTPassword == "" {
			return errors.New("REST storage needs its password on stdin")
		}
		if err := e.KeyStore().Put(local.RESTKeyID(c.ID), []byte(in.RESTPassword)); err != nil {
			return err
		}
	}
	// This machine's own repository key, added here so its derivation cost
	// is tuned for this machine. The key that came on stdin is used once to
	// add it and is never stored.
	own, err := keys.NewKey()
	if err != nil {
		return err
	}
	if err := addOwnKey(ctx, e, c, []byte(in.RepositoryKey), []byte(in.StorageKey), []byte(in.RESTPassword), own); err != nil {
		return fmt.Errorf("add this machine's repository key: %w", err)
	}
	if err := e.KeyStore().Put(c.ID, own); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil { // #nosec G703 -- box names are validated by local.NameRE
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil { // #nosec G703 -- box names are validated by local.NameRE
		return err
	}
	fmt.Printf("joined box %s (%s) on %s\n", name, c.ID, e.Machine)
	return nil
}

// addOwnKey runs restic key add on this machine with the transferred key.
func addOwnKey(ctx context.Context, e *local.Env, c local.BoxConfig, transferred, storageKey, restPassword, own []byte) error {
	repo, _, resticEnv, err := e.Storage(c)
	if err != nil {
		return err
	}
	s := &local.Session{E: e, Cfg: c, Repo: repo, ResticEnv: resticEnv, Key: transferred, SSHKey: storageKey, RESTPassword: restPassword}
	host := s.HostExecutor()
	if host == nil {
		return errors.New("no restic on this machine (the server setup installs it)")
	}
	sb, err := boxsync.Open(boxsync.Config{
		Executor: host, MetaExecutor: host, MetaRepository: s.HostRepo(),
		Repository: s.HostRepo(), Password: transferred, Env: resticEnv,
		SSHKey: storageKey, SSHHostKey: c.StorageHostKey,
		BoxID: c.ID, MachineID: e.Machine, HomeDir: "/nonexistent",
		StateDir: filepath.Join(e.Dir, "state", c.ID),
	})
	if err != nil {
		return err
	}
	return sb.AddKey(ctx, own, e.Machine)
}

// cmdSSHConfig prints the "ssh portenv" entry for a box on a server: it
// lands in the box's tmux local.Session (Phase 0 only: docker exec).
func cmdSSHConfig(_ context.Context, e *local.Env, name string, args []string) error {
	fs := flags("ssh-config")
	host := fs.String("host", "", "the server")
	user := fs.String("user", "ubuntu", "your SSH user on the server")
	alias := fs.String("alias", "portenv", "the Host name to use")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *host == "" {
		return usageError{Msg: "ssh-config needs --host"}
	}
	c, err := e.LoadBox(name)
	if err != nil {
		return err
	}
	fmt.Printf("Host %s\n  HostName %s\n  User %s\n  RequestTTY yes\n  RemoteCommand sudo docker exec -it -u work -w /home/work %s tmux new-local.Session -A -s main\n",
		*alias, *host, *user, "portenv-"+c.ID)
	return nil
}

func cmdStatus(ctx context.Context, e *local.Env, name string, args []string) error {
	if err := parse(flags("status"), args); err != nil {
		return err
	}
	s, err := e.Open(name)
	if err != nil {
		return err
	}
	st, err := s.Drv.Stats(ctx, s.ID())
	if err != nil {
		return err
	}
	storage := s.Cfg.Storage
	if storage == "" {
		storage = "this Mac only"
	}
	fmt.Printf("box      %s (%s)\nmachine  %s\nstorage  %s\nimage    %s\nstate    %s\n",
		name, s.Cfg.ID, e.Machine, storage, s.Cfg.Image, stateName(st.State))
	if st.State == driver.StateRunning {
		fmt.Printf("memory   %d MiB, %d processes\n", st.MemoryBytes>>20, st.ProcessCount)
	}
	// What this machine recorded, read from disk: status never starts the
	// box or runs restic (no docker exec, ADR 0010). The lease and every
	// save: portenv app history, through portenvd.
	local, ok, err := boxsync.LoadState(filepath.Join(e.Dir, "state", s.Cfg.ID))
	if err != nil {
		return err
	}
	switch {
	case !ok:
		fmt.Println("local    never synced on this machine")
	case local.Dirty:
		fmt.Println("local    open (may hold unsaved changes)")
	default:
		fmt.Println("local    in sync with its last save")
	}
	if ok && !local.SavedAt.IsZero() {
		fmt.Printf("saved    %s (lease %s here)\n", local.SavedAt.Local().Format(time.DateTime), local.Lease)
	}
	return nil
}

// remote runs a command on host over SSH. PORTENV_SSH overrides the ssh
func stateName(s driver.State) string {
	return map[driver.State]string{
		driver.StateCreated: "created", driver.StateStarting: "starting", driver.StateRunning: "running",
		driver.StateStopping: "stopping", driver.StateStopped: "stopped", driver.StateFailed: "failed",
	}[s]
}
