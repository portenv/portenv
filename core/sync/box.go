// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

// Config describes one box on one machine.
type Config struct {
	// Restic is the restic binary (0.17 or later) for a LocalExecutor. Not
	// needed when Executor is set.
	Restic string
	// Executor runs restic; nil means a LocalExecutor with Restic.
	Executor Executor
	// Repository is the box's restic repository, <storage>/boxes/<box-id>.
	Repository string
	// Password is the repository key for this machine. It is passed to
	// restic through a pipe and never written anywhere.
	Password []byte
	// Env adds environment for restic, for example storage credentials.
	Env []string

	BoxID     string
	MachineID string
	// HomeDir is where this machine sees the box's /home.
	HomeDir string
	// ExcludeFile is the box's excludes file; empty means none.
	ExcludeFile string
	// StateDir holds this machine's state for the box and the restic cache.
	StateDir string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (c Config) validate() error {
	switch {
	case c.Restic == "" && c.Executor == nil:
		return errors.New("sync: no restic binary configured")
	case c.Repository == "":
		return errors.New("sync: no repository configured")
	case len(c.Password) == 0:
		return errors.New("sync: no repository password")
	case !idRE.MatchString(c.BoxID):
		return fmt.Errorf("sync: invalid box ID %q", c.BoxID)
	case !idRE.MatchString(c.MachineID):
		return fmt.Errorf("sync: invalid machine ID %q", c.MachineID)
	case c.HomeDir == "" || c.StateDir == "":
		return errors.New("sync: home and state directories are required")
	}
	return nil
}

// Box saves and resumes one box's home on one machine. It is not safe for
// concurrent use; portenvd serialises operations per box.
type Box struct {
	cfg    Config
	restic *restic
}

// Open validates cfg and checks the restic version.
func Open(cfg Config) (*Box, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Executor == nil {
		cfg.Executor = LocalExecutor{Bin: cfg.Restic, CacheDir: filepath.Join(cfg.StateDir, "cache")}
	}
	b := &Box{cfg: cfg, restic: &restic{exec: cfg.Executor, repo: cfg.Repository, password: cfg.Password, env: cfg.Env}}
	if err := b.restic.checkVersion(context.Background()); err != nil {
		return nil, err
	}
	return b, nil
}

// Init creates the box's repository if it does not exist yet.
func (b *Box) Init(ctx context.Context) error {
	return b.restic.initIfMissing(ctx)
}

// Errors.
var (
	// ErrEmptyHome: a save was refused because the home is empty.
	ErrEmptyHome = errors.New("refusing to save an empty home")
	// errUnsavedChanges: a restore was refused because the local home may
	// hold unsaved changes. Resume never reaches it; it guards the invariant.
	errUnsavedChanges = errors.New("refusing to restore over unsaved changes")
)

// LeaseHeldError: another machine holds the box open (resume rule 2).
type LeaseHeldError struct{ Lease Lease }

func (e *LeaseHeldError) Error() string {
	stale := ""
	if e.Lease.Stale {
		stale = " (stale)"
	}
	return fmt.Sprintf("box is open on %s%s since %s", e.Lease.Machine, stale, e.Lease.LastSeen.Format(time.RFC3339))
}

// LeaseChangedError: since this machine last synced, another machine saved
// or took the lease. Saving needs confirmation; the other version stays in
// history either way.
type LeaseChangedError struct{ Newest Snapshot }

func (e *LeaseChangedError) Error() string {
	return fmt.Sprintf("box changed elsewhere: newest save %s from %s at %s",
		e.Newest.short(), e.Newest.Machine, e.Newest.Time.Format(time.RFC3339))
}

// Action is what Resume did to the local home.
type Action int

// Resume actions.
const (
	// ActionNewBox: nothing is saved and there is no local home. Start the
	// box with a fresh home from the image's skeleton (PORTENV_INIT_HOME=1).
	ActionNewBox Action = iota + 1
	// ActionStartLocal: the local home is current (it equals the newest
	// save, or adds this machine's own unsaved changes to it). No download.
	ActionStartLocal
	// ActionRestored: the newest save was restored incrementally.
	ActionRestored
	// ActionRestoredKeptLocal: the local home had unsaved changes and a
	// newer save existed elsewhere. The local home was saved as orphaned,
	// then the newest save restored. Both are in history.
	ActionRestoredKeptLocal
)

func (a Action) String() string {
	switch a {
	case ActionNewBox:
		return "new box"
	case ActionStartLocal:
		return "start local"
	case ActionRestored:
		return "restored"
	case ActionRestoredKeptLocal:
		return "restored, local kept as orphaned"
	}
	return fmt.Sprintf("Action(%d)", int(a))
}

// ResumeOptions control Resume.
type ResumeOptions struct {
	// TakeOver takes the lease from another machine. Only on the user's
	// explicit choice.
	TakeOver bool
}

// ResumeResult says which resume rule applied and what happened.
type ResumeResult struct {
	Rule     int // 1, 3, 4 or 5 (rule 2 is a LeaseHeldError)
	Action   Action
	Restored *Snapshot // the save now in the local home, if one was restored
	Orphaned *Snapshot // the local home saved before restoring (rule 4)
}

// Resume brings the local home up to date before the box starts, applying
// the resume rules in order, takes the lease and marks the box open.
func (b *Box) Resume(ctx context.Context, opts ResumeOptions) (ResumeResult, error) {
	snaps, err := b.restic.snapshots(ctx)
	if err != nil {
		return ResumeResult{}, err
	}
	st, known, err := loadState(b.cfg.StateDir)
	if err != nil {
		return ResumeResult{}, err
	}
	empty, err := b.homeEmpty()
	if err != nil {
		return ResumeResult{}, err
	}

	// Rule 1: nothing saved yet. A local home, if any, is kept as it is; the
	// first save records it and takes the lease.
	newest, saved := current(snaps)
	if !saved {
		res := ResumeResult{Rule: 1, Action: ActionStartLocal}
		if empty {
			res.Action = ActionNewBox
		}
		st.Dirty = true
		return res, saveState(b.cfg.StateDir, st)
	}

	me := b.cfg.MachineID

	// Rule 2: another machine holds the lease.
	if newest.Active != "" && newest.Active != me && !opts.TakeOver {
		return ResumeResult{}, &LeaseHeldError{Lease: b.leaseOf(newest)}
	}

	// Whether the newest save is newer than the local home: made by another
	// machine and not what this machine last synced to. A save this machine
	// made itself (even one whose state update was lost) is never newer than
	// its own home.
	foreignNewer := newest.Tree != st.Tree && newest.Machine != me
	var res ResumeResult
	switch {
	case empty:
		// Nothing local to lose.
		res = ResumeResult{Rule: 5, Action: ActionRestored}
	case !foreignNewer:
		// Rule 3.
		res = ResumeResult{Rule: 3, Action: ActionStartLocal}
		if newest.Machine == me {
			st.Tree = newest.Tree
		}
	case !known || st.Dirty:
		// Rule 4: possibly unsaved local changes, and a newer save. Keep the
		// local home first. Unknown state counts as unsaved.
		orphan, err := b.saveOrphan(ctx, snaps, st)
		if err != nil {
			return ResumeResult{}, fmt.Errorf("save local home before restoring: %w", err)
		}
		res = ResumeResult{Rule: 4, Action: ActionRestoredKeptLocal, Orphaned: &orphan}
	default:
		// Rule 5.
		res = ResumeResult{Rule: 5, Action: ActionRestored}
	}

	if res.Action == ActionRestored || res.Action == ActionRestoredKeptLocal {
		if err := b.restoreSnapshot(ctx, newest); err != nil {
			return ResumeResult{}, err
		}
		res.Restored = &newest
		st.Tree = newest.Tree
	}

	// Take the lease: move the active tag to this machine.
	if newest.Active != me {
		var remove []string
		if newest.Active != "" {
			remove = []string{tagActivePfx + newest.Active}
		}
		if err := b.restic.tag(ctx, newest.ID, []string{tagActivePfx + me}, remove); err != nil {
			return ResumeResult{}, fmt.Errorf("take lease: %w", err)
		}
	}

	st.Dirty = true // the box is open from here on
	if err := saveState(b.cfg.StateDir, st); err != nil {
		return ResumeResult{}, err
	}
	return res, nil
}

// saveOrphan saves the local home as orphaned and records it as the synced
// state, so restoring over it afterwards loses nothing.
func (b *Box) saveOrphan(ctx context.Context, snaps []Snapshot, st State) (Snapshot, error) {
	s, err := b.backup(ctx, snaps, st, []string{tagMachinePfx + b.cfg.MachineID, tagOrphaned})
	if err != nil {
		return Snapshot{}, err
	}
	if err := saveState(b.cfg.StateDir, State{Tree: s.Tree, Dirty: false}); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// restoreSnapshot restores s into the local home. It refuses when the home
// may hold unsaved changes: not empty, and dirty or of unknown state.
func (b *Box) restoreSnapshot(ctx context.Context, s Snapshot) error {
	st, known, err := loadState(b.cfg.StateDir)
	if err != nil {
		return err
	}
	empty, err := b.homeEmpty()
	if err != nil {
		return err
	}
	if !empty && (!known || st.Dirty) {
		return errUnsavedChanges
	}
	if err := b.restic.restoreTo(ctx, s, b.cfg.HomeDir, true); err != nil {
		return fmt.Errorf("restore %s: %w", s.short(), err)
	}
	return saveState(b.cfg.StateDir, State{Tree: s.Tree, Dirty: false})
}

// SaveKind says what a save is for.
type SaveKind int

// Save kinds.
const (
	// SaveAutosave: periodic save while the box is open.
	SaveAutosave SaveKind = iota
	// SavePoint: a save point (⌘S); the box stays open.
	SavePoint
	// SaveRelease: the box is closing; the lease is released.
	SaveRelease
)

func (k SaveKind) String() string {
	return [...]string{"autosave", "point", "release"}[k]
}

// SaveOptions control Save.
type SaveOptions struct {
	Kind SaveKind
	// ConfirmLeaseChange saves even though another machine saved or took
	// the lease since this machine last synced. The user has confirmed.
	ConfirmLeaseChange bool
}

// Save saves the local home. The save is complete only when restic has
// reported the snapshot and this machine's state records it.
func (b *Box) Save(ctx context.Context, opts SaveOptions) (Snapshot, error) {
	empty, err := b.homeEmpty()
	if err != nil {
		return Snapshot{}, err
	}
	if empty {
		return Snapshot{}, ErrEmptyHome
	}
	snaps, err := b.restic.snapshots(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	st, _, err := loadState(b.cfg.StateDir)
	if err != nil {
		return Snapshot{}, err
	}
	me := b.cfg.MachineID
	if newest, saved := current(snaps); saved && !opts.ConfirmLeaseChange {
		takenOver := newest.Active != "" && newest.Active != me
		savedElsewhere := newest.Machine != me && newest.Tree != st.Tree
		if takenOver || savedElsewhere {
			return Snapshot{}, &LeaseChangedError{Newest: newest}
		}
	}

	tags := []string{tagMachinePfx + me}
	switch opts.Kind {
	case SavePoint:
		tags = append(tags, tagPoint, tagActivePfx+me)
	case SaveRelease:
		tags = append(tags, tagRelease)
	default:
		tags = append(tags, tagActivePfx+me)
	}
	s, err := b.backup(ctx, snaps, st, tags)
	if err != nil {
		return Snapshot{}, err
	}
	if err := saveState(b.cfg.StateDir, State{Tree: s.Tree, Dirty: opts.Kind != SaveRelease}); err != nil {
		return Snapshot{}, fmt.Errorf("save %s made but not recorded: %w", s.short(), err)
	}

	// Only the newest snapshot carries the lease. Clearing older active
	// tags is tidying: the lease is read from the newest snapshot alone, so
	// a failure here does not undo the save.
	byTag := map[string][]string{}
	for _, old := range snaps {
		if old.Active != "" {
			byTag[tagActivePfx+old.Active] = append(byTag[tagActivePfx+old.Active], old.ID)
		}
	}
	for tag, ids := range byTag {
		_ = b.restic.untag(ctx, ids, tag)
	}
	return s, nil
}

// backup saves the home with tags, using the snapshot this machine last
// synced to as the parent so unchanged files are not re-read, and returns
// the new snapshot as restic lists it.
func (b *Box) backup(ctx context.Context, snaps []Snapshot, st State, tags []string) (Snapshot, error) {
	parent := ""
	for i := len(snaps) - 1; i >= 0; i-- {
		if s := snaps[i]; (st.Tree != "" && s.Tree == st.Tree) || (parent == "" && s.Machine == b.cfg.MachineID) {
			parent = s.ID
			if s.Tree == st.Tree {
				break
			}
		}
	}
	exclude := ""
	if b.cfg.ExcludeFile != "" {
		info, err := b.cfg.Executor.PathInfo(ctx, b.cfg.ExcludeFile)
		if err != nil {
			return Snapshot{}, err
		}
		if info.Exists && !info.IsDir {
			exclude = b.cfg.ExcludeFile
		}
	}
	id, err := b.restic.backup(ctx, backupArgs{home: b.cfg.HomeDir, tags: tags, parent: parent, excludeFile: exclude})
	if err != nil {
		return Snapshot{}, err
	}
	after, err := b.restic.snapshots(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for _, s := range after {
		if s.ID == id {
			return s, nil
		}
	}
	return Snapshot{}, fmt.Errorf("snapshot %s reported by restic is not in the repository", id)
}

// Lease returns the current lease, or nil when no machine holds the box.
func (b *Box) Lease(ctx context.Context) (*Lease, error) {
	snaps, err := b.restic.snapshots(ctx)
	if err != nil {
		return nil, err
	}
	newest, saved := current(snaps)
	if !saved || newest.Active == "" {
		return nil, nil
	}
	l := b.leaseOf(newest)
	return &l, nil
}

func (b *Box) leaseOf(s Snapshot) Lease {
	return Lease{
		Machine:  s.Active,
		LastSeen: s.Time,
		Stale:    b.cfg.Now().Sub(s.Time) > staleAfter,
		Source:   LeaseFromSnapshotTag,
	}
}

// History returns every save of the box, oldest first.
func (b *Box) History(ctx context.Context) ([]Snapshot, error) {
	return b.restic.snapshots(ctx)
}

// Forget applies the retention policy (keep the last 20, hourly for 24
// hours, daily for 14 days, weekly for 8 weeks, monthly for 12 months, and
// every orphaned save). With prune, unreferenced data is removed too.
func (b *Box) Forget(ctx context.Context, prune bool) error {
	return b.restic.forget(ctx, prune)
}

func (b *Box) homeEmpty() (bool, error) {
	info, err := b.cfg.Executor.PathInfo(context.Background(), b.cfg.HomeDir)
	if err != nil {
		return false, err
	}
	if !info.IsDir {
		return false, fmt.Errorf("home %s is not a directory", b.cfg.HomeDir)
	}
	return info.Empty, nil
}

// LocalState returns this machine's sync state for the box; ok is false when
// there is none.
func (b *Box) LocalState() (st State, ok bool, err error) {
	return loadState(b.cfg.StateDir)
}

// current returns the box's current save: the newest snapshot that is not
// orphaned. An orphan (rule 4) is newer by time than the save restored over
// it, but it is history, never the current version, and never carries the
// lease.
func current(snaps []Snapshot) (Snapshot, bool) {
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Kind != KindOrphaned {
			return snaps[i], true
		}
	}
	return Snapshot{}, false
}
