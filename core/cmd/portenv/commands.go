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
	"os/exec"
	"path/filepath"
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
	storage := fs.String("storage", "", "storage: an absolute directory or s3:URL (default: this Mac only)")
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
	c := boxConfig{ID: "box-" + hex.EncodeToString(b), Name: name, Image: *image, Storage: *storage}
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
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Printf("created box %s (%s) on %s\nnext: portenv resume %s\n", name, c.ID, e.machine, name)
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
	if _, err := s.drv.Start(ctx, id); err != nil {
		return err
	}
	// An empty home makes the agent report FAILED; restic still runs.
	if _, _, err := waitAgent(ctx, s.drv, id, "READY", "FAILED"); err != nil {
		return err
	}
	sb, err := s.sync()
	if err != nil {
		return err
	}
	if err := sb.Init(ctx); err != nil {
		return err
	}
	res, err := sb.Resume(ctx, boxsync.ResumeOptions{TakeOver: *takeOver})
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
		if err := s.restart(ctx, false); err != nil {
			return err
		}
	}
	state, detail, err := waitAgent(ctx, s.drv, id, "READY", "FAILED")
	if err != nil {
		return err
	}
	if state != "READY" {
		return fmt.Errorf("the box failed to start: %s", detail)
	}
	fmt.Printf("%s is open on %s (resume rule %d: %s)\n", name, e.machine, res.Rule, res.Action)
	if res.Orphaned != nil {
		fmt.Printf("unsaved work from this machine was kept as save %s before restoring the newer one\n", res.Orphaned.ID[:8])
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
			lease := ""
			if h.Active != "" {
				lease = "  open on " + h.Active
			}
			fmt.Printf("%s  %s  %-8s  %s%s\n", h.ID[:8], h.Time.Local().Format(time.DateTime), h.Kind, h.Machine, lease)
		}
		return nil
	})
}

func cmdMove(ctx context.Context, e *env, name string, args []string) error {
	fs := flags("move")
	to := fs.String("to", "", "SSH host to resume the box on")
	confirm := fs.Bool("confirm", false, "save even though the box changed on another machine")
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
	fmt.Fprintf(os.Stderr, "resuming %s on %s\n", name, *to)
	cmd := exec.CommandContext(ctx, "ssh", "--", *to, "portenv", "resume", name) // #nosec G204 G702 -- the user's own SSH host, after -- so it cannot be an option
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("resume on %s: %w (the box is saved and released; resume it anywhere)", *to, err)
	}
	return nil
}

func stateName(s driver.State) string {
	return map[driver.State]string{
		driver.StateCreated: "created", driver.StateStarting: "starting", driver.StateRunning: "running",
		driver.StateStopping: "stopping", driver.StateStopped: "stopped", driver.StateFailed: "failed",
	}[s]
}
