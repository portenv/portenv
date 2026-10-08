// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	if _, err := os.Stat(p); err == nil {
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
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
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
	fmt.Printf("next: portenv resume %s\n", name)
	return nil
}

// cmdJoin enrols an existing box on this machine: the repository key comes
// on stdin (from portenv move, over SSH) and goes only into the key store.
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
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("box %s already exists here", name)
	}
	c := local.BoxConfig{ID: *id, Name: name, Image: *image, Storage: *storage, StorageHostKey: *hostKey, StorageREST: *rest}
	if _, _, _, err := e.Storage(c); err != nil {
		return err
	}
	// The keys arrive on stdin as JSON (portenv move sends them over SSH);
	// they go only into this machine's key store.
	var in joinKeys
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
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
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

// joinKeys is what portenv move sends to portenv join on stdin.
type joinKeys struct {
	RepositoryKey string `json:"repository_key"`
	StorageKey    string `json:"storage_key,omitempty"`   // SFTP storage, OpenSSH PEM
	RESTPassword  string `json:"rest_password,omitempty"` // REST storage (StorageREST)
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

func cmdResume(ctx context.Context, e *local.Env, name string, args []string) error {
	fs := flags("resume")
	takeOver := fs.Bool("take-over", false, "take the box over from the machine that has it open")
	if err := parse(fs, args); err != nil {
		return err
	}
	var s *local.Session
	if err := local.Timed("open", func() error { var e2 error; s, e2 = e.Open(name); return e2 }); err != nil {
		return err
	}
	id := s.ID()
	// Probe storage while the box starts: an unreachable server costs its
	// timeout once, in parallel, not before every offline start.
	reach := make(chan bool, 1)
	go func() {
		start := time.Now()
		ok := s.StorageReachable()
		local.Trace("storage probe (parallel)", time.Since(start))
		reach <- ok
	}()
	if !s.Drv.Known(id) {
		if _, err := s.Drv.Create(ctx, driver.Box{ID: id, Name: name, ToolboxImage: s.Cfg.Image}); err != nil {
			return err
		}
		// Pin a registry image to the digest it resolved to, so every machine
		// pulls exactly that image. Local development images stay as they
		// are: their digest exists only on this machine.
		if !local.FromRegistry(s.Cfg.Image) {
			// nothing to pin
		} else if pinned, err := s.Drv.ImageDigest(ctx, s.Cfg.Image); err != nil {
			return err
		} else if pinned != s.Cfg.Image {
			s.Cfg.Image = pinned
			if err := e.SaveBox(s.Cfg); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "toolbox pinned to %s\n", pinned)
		}
		if err := s.Drv.MountHome(ctx, id, driver.HomeStorage{Ref: local.HomeVolume(s.Cfg.ID)}); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "starting the box")
	if err := local.Timed("box start", func() error { _, err := s.Drv.Start(ctx, id); return err }); err != nil {
		return err
	}
	// An empty home makes the agent report FAILED; restic still runs.
	if err := local.Timed("agent ready", func() error { _, _, err := local.WaitAgent(ctx, s.Drv, id, "READY", "FAILED"); return err }); err != nil {
		return err
	}
	var sb *boxsync.Box
	if err := local.Timed("sync open", func() error { var e2 error; sb, e2 = s.Sync(); return e2 }); err != nil {
		return err
	}
	var reachable bool
	_ = local.Timed("storage probe (wait)", func() error { reachable = <-reach; return nil })
	var res boxsync.ResumeResult
	var err error
	if !reachable {
		// Offline: start from the local home if this machine's state allows
		// it; saves wait until storage is reachable again.
		if res, err = sb.ResumeOffline(); err != nil {
			_, _ = s.Drv.Stop(ctx, id, 0)
			return fmt.Errorf("storage is unreachable and %w", err)
		}
	} else {
		if err := local.Timed("repository check", func() error { return sb.Init(ctx) }); err != nil {
			return err
		}
		err = local.Timed("resume (all sync)", func() error {
			var e error
			res, e = sb.Resume(ctx, boxsync.ResumeOptions{TakeOver: *takeOver})
			return e
		})
	}
	var held *boxsync.LeaseHeldError
	if errors.As(err, &held) {
		_, _ = s.Drv.Stop(ctx, id, 0)
		hint := "run portenv resume " + name + " --take-over to take it over"
		if held.Lease.Stale {
			hint = "it looks stale; " + hint
		}
		return fmt.Errorf("%w; %s", err, hint)
	}
	if err != nil {
		return err
	}

	switch res.Action {
	case boxsync.ActionNewBox:
		fmt.Fprintln(os.Stderr, "nothing saved yet: creating a fresh home")
		if err := restart(ctx, s, true); err != nil {
			return err
		}
	case boxsync.ActionRestored, boxsync.ActionRestoredKeptLocal:
		if err := local.Timed("restart after restore", func() error { return restart(ctx, s, false) }); err != nil {
			return err
		}
	}
	var state, detail string
	err = local.Timed("agent ready (final)", func() error {
		var e error
		state, detail, e = local.WaitAgent(ctx, s.Drv, id, "READY", "FAILED")
		return e
	})
	if err != nil {
		return err
	}
	if state != "READY" {
		return fmt.Errorf("the box failed to start: %s", detail)
	}
	if res.Offline {
		fmt.Printf("%s is open on %s, offline (Offline · will save later)\n", name, e.Machine)
		return nil
	}
	fmt.Printf("%s is open on %s (resume rule %d: %s)\n", name, e.Machine, res.Rule, res.Action)
	if res.Orphaned != nil {
		fmt.Printf("unsaved work from %s was kept as a separate save, %s (%s), before restoring the newer one; see portenv history %s\n",
			res.Orphaned.Machine, res.Orphaned.ID[:8], res.Orphaned.Time.Local().Format(time.DateTime), name)
	}
	return nil
}

// restart stops the box and starts it again, with a fresh home from the
// image's skeleton when fresh is set, so its start sequence runs on the
// current home.
func restart(ctx context.Context, s *local.Session, fresh bool) error {
	if _, err := s.Drv.Stop(ctx, s.ID(), 0); err != nil {
		return err
	}
	if fresh {
		if err := s.Drv.MountHome(ctx, s.ID(), driver.HomeStorage{Ref: local.HomeVolume(s.Cfg.ID), Fresh: true}); err != nil {
			return err
		}
	}
	_, err := s.Drv.Start(ctx, s.ID())
	return err
}

func requireRunning(ctx context.Context, s *local.Session) error {
	st, err := s.Drv.Stats(ctx, s.ID())
	if err != nil {
		return err
	}
	if st.State != driver.StateRunning {
		return fmt.Errorf("%s is not open on this machine; run portenv resume %s", s.Cfg.Name, s.Cfg.Name)
	}
	return nil
}

func save(ctx context.Context, e *local.Env, name string, args []string, kind boxsync.SaveKind) (*local.Session, error) {
	fs := flags(kind.String())
	confirm := fs.Bool("confirm", false, "save even though the box changed on another machine")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	s, err := e.Open(name)
	if err != nil {
		return nil, err
	}
	if err := requireRunning(ctx, s); err != nil {
		return nil, err
	}
	if !s.StorageReachable() {
		return nil, errOffline
	}
	sb, err := s.Sync()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	snap, err := sb.Save(ctx, boxsync.SaveOptions{Kind: kind, ConfirmLeaseChange: *confirm})
	var changed *boxsync.LeaseChangedError
	if errors.As(err, &changed) {
		return nil, fmt.Errorf("%w; your version and that one both stay in history: add --confirm to save yours", err)
	}
	if err != nil {
		return nil, err
	}
	fmt.Printf("saved %s (%s) in %s\n", snap.ID[:8], snap.Kind, time.Since(start).Round(100*time.Millisecond))
	return s, nil
}

// errOffline: storage is unreachable, so the save waits. Nothing is lost:
// the work stays in the box, which stays open (and dirty) until a save
// reaches storage.
var errOffline = errors.New("storage is unreachable (Offline · will save later): your work stays in the box")

func cmdSave(ctx context.Context, e *local.Env, name string, args []string) error {
	_, err := save(ctx, e, name, args, boxsync.SaveAutosave)
	return err
}

func cmdPoint(ctx context.Context, e *local.Env, name string, args []string) error {
	_, err := save(ctx, e, name, args, boxsync.SavePoint)
	return err
}

func cmdClose(ctx context.Context, e *local.Env, name string, args []string) error {
	s, err := save(ctx, e, name, args, boxsync.SaveRelease)
	if err != nil {
		return err
	}
	if _, err := s.Drv.Stop(ctx, s.ID(), 0); err != nil {
		return err
	}
	fmt.Printf("%s is closed and released\n", name)
	return nil
}

// whileRunning runs fn with the box running, starting it for the duration
// if needed (read-only queries: no lease is taken).
func whileRunning(ctx context.Context, s *local.Session, fn func(*boxsync.Box) error) error {
	st, err := s.Drv.Stats(ctx, s.ID())
	if err != nil {
		return err
	}
	if !s.Drv.Known(s.ID()) {
		return errors.New("the box has never been opened on this machine")
	}
	if st.State != driver.StateRunning {
		if _, err := s.Drv.Start(ctx, s.ID()); err != nil {
			return err
		}
		defer func() { _, _ = s.Drv.Stop(context.Background(), s.ID(), 0) }()
		if _, _, err := local.WaitAgent(ctx, s.Drv, s.ID(), "READY", "FAILED"); err != nil {
			return err
		}
	}
	sb, err := s.Sync()
	if err != nil {
		return err
	}
	return fn(sb)
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
	if !s.Drv.Known(s.ID()) {
		return nil
	}
	return whileRunning(ctx, s, func(sb *boxsync.Box) error {
		local, ok, err := sb.LocalState()
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
		lease, err := sb.Lease(ctx)
		if err != nil {
			return err
		}
		switch {
		case lease == nil:
			fmt.Println("lease    free")
		case lease.Stale:
			fmt.Printf("lease    %s (stale, last seen %s)\n", lease.Machine, lease.LastSeen.Local().Format(time.DateTime))
		default:
			fmt.Printf("lease    %s (last seen %s)\n", lease.Machine, lease.LastSeen.Local().Format(time.DateTime))
		}
		hist, err := sb.History(ctx)
		if err != nil {
			return err
		}
		if n := len(hist); n > 0 {
			last := hist[n-1]
			fmt.Printf("saved    %s, %s from %s (%d saves)\n", last.Time.Local().Format(time.DateTime), last.Kind, last.Machine, n)
		}
		printKept(hist)
		return nil
	})
}

func cmdHistory(ctx context.Context, e *local.Env, name string, args []string) error {
	if err := parse(flags("history"), args); err != nil {
		return err
	}
	s, err := e.Open(name)
	if err != nil {
		return err
	}
	return whileRunning(ctx, s, func(sb *boxsync.Box) error {
		hist, err := sb.History(ctx)
		if err != nil {
			return err
		}
		if len(hist) == 0 {
			fmt.Println("no saves yet")
		}
		for _, h := range hist {
			note := ""
			if h.Active != "" {
				note = "  open on " + h.Active
			}
			if h.Kind == boxsync.KindOrphaned {
				note = "  unsaved work from " + h.Machine + ", kept as a separate save"
			}
			fmt.Printf("%s  %s  %-8s  %s%s\n", h.ID[:8], h.Time.Local().Format(time.DateTime), h.Kind, h.Machine, note)
		}
		return nil
	})
}

// cmdHousekeep tidies the repository while nothing else is happening:
// leftover lease tags on older saves, then retention (never the current
// save). It is not part of close, so closing stays fast.
func cmdHousekeep(ctx context.Context, e *local.Env, name string, args []string) error {
	fs := flags("housekeep")
	prune := fs.Bool("prune", false, "also delete data no snapshot uses")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := e.Open(name)
	if err != nil {
		return err
	}
	return whileRunning(ctx, s, func(sb *boxsync.Box) error {
		return sb.Housekeep(ctx, *prune)
	})
}

func cmdMove(ctx context.Context, e *local.Env, name string, args []string) error {
	fs := flags("move")
	to := fs.String("to", "", "SSH host to resume the box on")
	confirm := fs.Bool("confirm", false, "save even though the box changed on another machine")
	joinStorage := fs.String("join-storage", "", "first enrol the box on the host, which reaches its storage here (a directory, or sftp:...)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *to == "" || (*to)[0] == '-' {
		return usageError{Msg: "move needs --to SSH-HOST"}
	}
	closeArgs := []string{}
	if *confirm {
		closeArgs = append(closeArgs, "--confirm")
	}
	if err := cmdClose(ctx, e, name, closeArgs); err != nil {
		return err
	}
	if *joinStorage != "" {
		c, err := e.LoadBox(name)
		if err != nil {
			return err
		}
		key, err := e.KeyStore().Get(c.ID)
		if err != nil {
			return err
		}
		keys := joinKeys{RepositoryKey: string(key)}
		joinArgs := []string{"sudo", "portenv", "join", name, "--id", c.ID, "--image", c.Image, "--storage", *joinStorage}
		if strings.HasPrefix(*joinStorage, "sftp:") {
			sk, err := e.KeyStore().Get(local.StorageKeyID(c.ID))
			if err != nil {
				return fmt.Errorf("storage key: %w", err)
			}
			keys.StorageKey = string(sk)
			joinArgs = append(joinArgs, "--storage-host-key", "'"+c.StorageHostKey+"'")
			if c.StorageREST != "" {
				rp, err := e.KeyStore().Get(local.RESTKeyID(c.ID))
				if err != nil {
					return fmt.Errorf("REST storage password: %w", err)
				}
				keys.RESTPassword = string(rp)
				joinArgs = append(joinArgs, "--storage-rest", c.StorageREST)
			}
		}
		payload, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "enrolling %s on %s\n", name, *to)
		join := remote(ctx, *to, joinArgs...)
		join.Stdin = bytes.NewReader(payload) // the keys travel on SSH's stdin, never on disk
		join.Stdout, join.Stderr = os.Stdout, os.Stderr
		if err := join.Run(); err != nil {
			return fmt.Errorf("enrol on %s: %w (the box is saved and released)", *to, err)
		}
	}
	fmt.Fprintf(os.Stderr, "resuming %s on %s\n", name, *to)
	if err := runRemote(ctx, *to, "sudo", "portenv", "resume", name); err != nil {
		return fmt.Errorf("resume on %s: %w (the box is saved and released; resume it anywhere)", *to, err)
	}
	return nil
}

// remote runs a command on host over SSH. PORTENV_SSH overrides the ssh
// command and its options (tests).
func remote(ctx context.Context, host string, argv ...string) *exec.Cmd {
	sshCmd := []string{"ssh"}
	if v := os.Getenv("PORTENV_SSH"); v != "" {
		sshCmd = strings.Fields(v)
	}
	full := append(append(sshCmd[1:], "--", host), argv...)
	return exec.CommandContext(ctx, sshCmd[0], full...) // #nosec G204 G702 -- the user's own SSH host, after -- so it cannot be an option
}

// runRemote runs a non-interactive command on host, showing its output. It
// never forwards stdin: an ssh that inherits an open stdin can wait forever
// after the remote command has finished.
func runRemote(ctx context.Context, host string, argv ...string) error {
	cmd := remote(ctx, host, argv...)
	cmd.Stdin = nil // /dev/null
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func stateName(s driver.State) string {
	return map[driver.State]string{
		driver.StateCreated: "created", driver.StateStarting: "starting", driver.StateRunning: "running",
		driver.StateStopping: "stopping", driver.StateStopped: "stopped", driver.StateFailed: "failed",
	}[s]
}

// printKept lists unsaved work that was kept as separate saves (resume rule
// 4, usually after a take over), newest first, at most three.
func printKept(hist []boxsync.Snapshot) {
	var kept []boxsync.Snapshot
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Kind == boxsync.KindOrphaned {
			kept = append(kept, hist[i])
		}
	}
	for i, k := range kept {
		if i == 3 {
			fmt.Printf("kept     … and %d more (portenv history)\n", len(kept)-3)
			break
		}
		fmt.Printf("kept     unsaved work from %s as a separate save, %s (%s)\n", k.Machine, k.ID[:8], k.Time.Local().Format(time.DateTime))
	}
}
