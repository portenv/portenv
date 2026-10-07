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

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/keys"
	boxsync "github.com/portenv/portenv/core/sync"
)

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
	}
	return nil
}

func cmdInit(_ context.Context, e *env, name string, args []string) error {
	fs := flags("init")
	image := fs.String("image", defaultImage, "toolbox image")
	storage := fs.String("storage", "", "storage: an absolute directory, sftp:USER@HOST:/PATH or s3:URL (default: this Mac only)")
	hostKey := fs.String("storage-host-key", "", "SFTP storage: the server's host key, \"ssh-ed25519 AAAA...\"")
	if err := parse(fs, args); err != nil {
		return err
	}
	p, err := e.configPath(name)
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
	c := boxConfig{ID: "box-" + hex.EncodeToString(b), Name: name, Image: *image, Storage: *storage, StorageHostKey: *hostKey}
	if _, _, _, err := e.storage(c); err != nil {
		return err
	}
	key, err := keys.NewKey()
	if err != nil {
		return err
	}
	if err := e.keyStore().Put(c.ID, key); err != nil {
		return err
	}
	var storagePub string
	if strings.HasPrefix(c.Storage, "sftp:") {
		priv, pub, err := keys.NewSSHKey("portenv storage " + c.ID)
		if err != nil {
			return err
		}
		if err := e.keyStore().Put(storageKeyID(c.ID), priv); err != nil {
			return err
		}
		storagePub = pub
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Printf("created box %s (%s) on %s\n", name, c.ID, e.machine)
	if storagePub != "" {
		fmt.Printf("add this key to the storage account's authorized_keys on the server:\n%s\n", storagePub)
	}
	fmt.Printf("next: portenv resume %s\n", name)
	return nil
}

// cmdJoin enrols an existing box on this machine: the repository key comes
// on stdin (from portenv move, over SSH) and goes only into the key store.
func cmdJoin(_ context.Context, e *env, name string, args []string) error {
	fs := flags("join")
	id := fs.String("id", "", "the box's ID")
	image := fs.String("image", defaultImage, "toolbox image")
	storage := fs.String("storage", "", "where this machine reaches the box's storage: an absolute directory, sftp:USER@HOST:/PATH or s3:URL")
	hostKey := fs.String("storage-host-key", "", "SFTP storage: the server's host key")
	if err := parse(fs, args); err != nil {
		return err
	}
	if !nameRE.MatchString(*id) {
		return usageError{"join needs --id BOX-ID"}
	}
	p, err := e.configPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("box %s already exists here", name)
	}
	c := boxConfig{ID: *id, Name: name, Image: *image, Storage: *storage, StorageHostKey: *hostKey}
	if _, _, _, err := e.storage(c); err != nil {
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
	if err := e.keyStore().Put(c.ID, []byte(in.RepositoryKey)); err != nil {
		return err
	}
	if in.StorageKey != "" {
		if err := e.keyStore().Put(storageKeyID(c.ID), []byte(in.StorageKey)); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Printf("joined box %s (%s) on %s\n", name, c.ID, e.machine)
	return nil
}

// joinKeys is what portenv move sends to portenv join on stdin.
type joinKeys struct {
	RepositoryKey string `json:"repository_key"`
	StorageKey    string `json:"storage_key,omitempty"` // SFTP storage, OpenSSH PEM
}

// cmdSSHConfig prints the "ssh portenv" entry for a box on a server: it
// lands in the box's tmux session (Phase 0 only: docker exec).
func cmdSSHConfig(_ context.Context, e *env, name string, args []string) error {
	fs := flags("ssh-config")
	host := fs.String("host", "", "the server")
	user := fs.String("user", "ubuntu", "your SSH user on the server")
	alias := fs.String("alias", "portenv", "the Host name to use")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *host == "" {
		return usageError{"ssh-config needs --host"}
	}
	c, err := e.loadBox(name)
	if err != nil {
		return err
	}
	fmt.Printf("Host %s\n  HostName %s\n  User %s\n  RequestTTY yes\n  RemoteCommand sudo docker exec -it -u work -w /home/work %s tmux new-session -A -s main\n",
		*alias, *host, *user, "portenv-"+c.ID)
	return nil
}

func cmdResume(ctx context.Context, e *env, name string, args []string) error {
	fs := flags("resume")
	takeOver := fs.Bool("take-over", false, "take the box over from the machine that has it open")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := e.open(name)
	if err != nil {
		return err
	}
	id := s.id()
	if !s.drv.Known(id) {
		if _, err := s.drv.Create(ctx, driver.Box{ID: id, Name: name, ToolboxImage: s.cfg.Image}); err != nil {
			return err
		}
		if err := s.drv.MountHome(ctx, id, driver.HomeStorage{Ref: homeVolume(s.cfg.ID)}); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "starting the box")
	if err := timed("box start", func() error { _, err := s.drv.Start(ctx, id); return err }); err != nil {
		return err
	}
	// An empty home makes the agent report FAILED; restic still runs.
	if err := timed("agent ready", func() error { _, _, err := waitAgent(ctx, s.drv, id, "READY", "FAILED"); return err }); err != nil {
		return err
	}
	sb, err := s.sync()
	if err != nil {
		return err
	}
	if err := sb.Init(ctx); err != nil {
		return err
	}
	var res boxsync.ResumeResult
	err = timed("resume (all sync)", func() error {
		var e error
		res, e = sb.Resume(ctx, boxsync.ResumeOptions{TakeOver: *takeOver})
		return e
	})
	var held *boxsync.LeaseHeldError
	if errors.As(err, &held) {
		_, _ = s.drv.Stop(ctx, id, 0)
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
		if err := s.restart(ctx, true); err != nil {
			return err
		}
	case boxsync.ActionRestored, boxsync.ActionRestoredKeptLocal:
		if err := timed("restart after restore", func() error { return s.restart(ctx, false) }); err != nil {
			return err
		}
	}
	var state, detail string
	err = timed("agent ready (final)", func() error { var e error; state, detail, e = waitAgent(ctx, s.drv, id, "READY", "FAILED"); return e })
	if err != nil {
		return err
	}
	if state != "READY" {
		return fmt.Errorf("the box failed to start: %s", detail)
	}
	fmt.Printf("%s is open on %s (resume rule %d: %s)\n", name, e.machine, res.Rule, res.Action)
	if res.Orphaned != nil {
		fmt.Printf("unsaved work from %s was kept as a separate save, %s (%s), before restoring the newer one; see portenv history %s\n",
			res.Orphaned.Machine, res.Orphaned.ID[:8], res.Orphaned.Time.Local().Format(time.DateTime), name)
	}
	return nil
}

// restart stops the box and starts it again, with a fresh home from the
// image's skeleton when fresh is set, so its start sequence runs on the
// current home.
func (s *session) restart(ctx context.Context, fresh bool) error {
	if _, err := s.drv.Stop(ctx, s.id(), 0); err != nil {
		return err
	}
	if fresh {
		if err := s.drv.MountHome(ctx, s.id(), driver.HomeStorage{Ref: homeVolume(s.cfg.ID), Fresh: true}); err != nil {
			return err
		}
	}
	_, err := s.drv.Start(ctx, s.id())
	return err
}

func (s *session) requireRunning(ctx context.Context) error {
	st, err := s.drv.Stats(ctx, s.id())
	if err != nil {
		return err
	}
	if st.State != driver.StateRunning {
		return fmt.Errorf("%s is not open on this machine; run portenv resume %s", s.cfg.Name, s.cfg.Name)
	}
	return nil
}

func save(ctx context.Context, e *env, name string, args []string, kind boxsync.SaveKind) (*session, error) {
	fs := flags(kind.String())
	confirm := fs.Bool("confirm", false, "save even though the box changed on another machine")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	s, err := e.open(name)
	if err != nil {
		return nil, err
	}
	if err := s.requireRunning(ctx); err != nil {
		return nil, err
	}
	sb, err := s.sync()
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

func cmdSave(ctx context.Context, e *env, name string, args []string) error {
	_, err := save(ctx, e, name, args, boxsync.SaveAutosave)
	return err
}

func cmdPoint(ctx context.Context, e *env, name string, args []string) error {
	_, err := save(ctx, e, name, args, boxsync.SavePoint)
	return err
}

func cmdClose(ctx context.Context, e *env, name string, args []string) error {
	s, err := save(ctx, e, name, args, boxsync.SaveRelease)
	if err != nil {
		return err
	}
	if _, err := s.drv.Stop(ctx, s.id(), 0); err != nil {
		return err
	}
	fmt.Printf("%s is closed and released\n", name)
	return nil
}

// whileRunning runs fn with the box running, starting it for the duration
// if needed (read-only queries: no lease is taken).
func (s *session) whileRunning(ctx context.Context, fn func(*boxsync.Box) error) error {
	st, err := s.drv.Stats(ctx, s.id())
	if err != nil {
		return err
	}
	if !s.drv.Known(s.id()) {
		return errors.New("the box has never been opened on this machine")
	}
	if st.State != driver.StateRunning {
		if _, err := s.drv.Start(ctx, s.id()); err != nil {
			return err
		}
		defer func() { _, _ = s.drv.Stop(context.Background(), s.id(), 0) }()
		if _, _, err := waitAgent(ctx, s.drv, s.id(), "READY", "FAILED"); err != nil {
			return err
		}
	}
	sb, err := s.sync()
	if err != nil {
		return err
	}
	return fn(sb)
}

func cmdStatus(ctx context.Context, e *env, name string, args []string) error {
	if err := parse(flags("status"), args); err != nil {
		return err
	}
	s, err := e.open(name)
	if err != nil {
		return err
	}
	st, err := s.drv.Stats(ctx, s.id())
	if err != nil {
		return err
	}
	storage := s.cfg.Storage
	if storage == "" {
		storage = "this Mac only"
	}
	fmt.Printf("box      %s (%s)\nmachine  %s\nstorage  %s\nimage    %s\nstate    %s\n",
		name, s.cfg.ID, e.machine, storage, s.cfg.Image, stateName(st.State))
	if st.State == driver.StateRunning {
		fmt.Printf("memory   %d MiB, %d processes\n", st.MemoryBytes>>20, st.ProcessCount)
	}
	if !s.drv.Known(s.id()) {
		return nil
	}
	return s.whileRunning(ctx, func(sb *boxsync.Box) error {
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

func cmdHistory(ctx context.Context, e *env, name string, args []string) error {
	if err := parse(flags("history"), args); err != nil {
		return err
	}
	s, err := e.open(name)
	if err != nil {
		return err
	}
	return s.whileRunning(ctx, func(sb *boxsync.Box) error {
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

func cmdMove(ctx context.Context, e *env, name string, args []string) error {
	fs := flags("move")
	to := fs.String("to", "", "SSH host to resume the box on")
	confirm := fs.Bool("confirm", false, "save even though the box changed on another machine")
	joinStorage := fs.String("join-storage", "", "first enrol the box on the host, which reaches its storage here (a directory, or sftp:...)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *to == "" || (*to)[0] == '-' {
		return usageError{"move needs --to SSH-HOST"}
	}
	closeArgs := []string{}
	if *confirm {
		closeArgs = append(closeArgs, "--confirm")
	}
	if err := cmdClose(ctx, e, name, closeArgs); err != nil {
		return err
	}
	if *joinStorage != "" {
		c, err := e.loadBox(name)
		if err != nil {
			return err
		}
		key, err := e.keyStore().Get(c.ID)
		if err != nil {
			return err
		}
		keys := joinKeys{RepositoryKey: string(key)}
		joinArgs := []string{"sudo", "portenv", "join", name, "--id", c.ID, "--image", c.Image, "--storage", *joinStorage}
		if strings.HasPrefix(*joinStorage, "sftp:") {
			sk, err := e.keyStore().Get(storageKeyID(c.ID))
			if err != nil {
				return fmt.Errorf("storage key: %w", err)
			}
			keys.StorageKey = string(sk)
			joinArgs = append(joinArgs, "--storage-host-key", "'"+c.StorageHostKey+"'")
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
