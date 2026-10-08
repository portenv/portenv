// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	// SSHKey and SSHHostKey are for SFTP storage: the private key (served
	// from memory inside the box) and the server's pinned host key.
	SSHKey     []byte
	SSHHostKey string

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
	// Trace, when set, receives the duration of each restic call.
	Trace func(phase string, d time.Duration)
	// Deadlines bound each restic run (zero values: DefaultDeadlines).
	Deadlines Deadlines
	// OnRetry, when set, hears when a restic run that hit its deadline is
	// being retried (true) and when that ends (false): the state line shows
	// "Not saved since … · retrying".
	OnRetry func(retrying bool)
	// MetaExecutor, when set, runs restic commands that do not need /home
	// on the host (lease checks, listing, tags, init, keys, retention), and
	// MetaRepository is the repository's address from the host. Backup and
	// restore always use Executor.
	MetaExecutor   Executor
	MetaRepository string
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
	metaRepo := cfg.MetaRepository
	if metaRepo == "" {
		metaRepo = cfg.Repository
	}
	b := &Box{cfg: cfg, restic: &restic{exec: cfg.Executor, repo: cfg.Repository, trace: cfg.Trace,
		deadlines: cfg.Deadlines, onRetry: cfg.OnRetry,
		meta: cfg.MetaExecutor, metaRepo: metaRepo, cred: Credentials{
			Password: cfg.Password, Env: cfg.Env, SSHKey: cfg.SSHKey, SSHHostKey: cfg.SSHHostKey,
		}}}
	if err := b.checkVersionOnce(context.Background()); err != nil {
		return nil, err
	}
	return b, nil
}

// OnRetry sets the function that hears when a restic run that missed its
// deadline is being retried (true) and when that ends (false).
func (b *Box) OnRetry(fn func(retrying bool)) { b.restic.onRetry = fn }

// versionFile records which restic binary passed the version check.
const versionFile = "restic-checked"

// checkVersionOnce checks restic's version once per binary: restic is
// pinned and hash-checked when installed, so an open that finds the same
// binary (same path, size and time) skips running it. An executor that
// cannot identify its binary (restic in the box) is checked every time.
func (b *Box) checkVersionOnce(ctx context.Context) error {
	ex, _ := b.restic.pick([]string{"version"})
	idr, ok := ex.(interface{ BinaryID() (string, error) })
	if !ok || b.cfg.StateDir == "" {
		return b.restic.checkVersion(ctx)
	}
	id, err := idr.BinaryID()
	if err != nil {
		return b.restic.checkVersion(ctx)
	}
	path := filepath.Join(b.cfg.StateDir, versionFile)
	if got, err := os.ReadFile(path); err == nil && string(got) == id { // #nosec G304 -- this box's state directory
		return nil
	}
	if err := b.restic.checkVersion(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(b.cfg.StateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(id), 0o600)
}

// Init creates the box's repository if it does not exist yet. Once this
// machine has state for the box it knows the repository, and Init costs
// nothing.
func (b *Box) Init(ctx context.Context) error {
	if _, known, err := loadState(b.cfg.StateDir); err == nil && known {
		return nil
	}
	return b.restic.initIfMissing(ctx)
}

// AddKey adds a repository key for this machine, with newPassword, so the
// key's derivation cost is tuned on this machine (restic key add calibrates
// where it runs). It needs the host executor; the box never runs key
// commands.
func (b *Box) AddKey(ctx context.Context, newPassword []byte, host string) error {
	if b.restic.meta == nil {
		return errors.New("adding a key needs the host executor")
	}
	cred := b.restic.cred
	cred.NewPassword = newPassword
	res, err := b.restic.meta.Restic(ctx, []string{"--repo", b.restic.metaRepo, "key", "add",
		"--new-password-file", "/dev/fd/4", "--host", host}, cred)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return resticError([]string{"key"}, res.Stderr, &ExitError{Code: res.ExitCode})
	}
	return nil
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
	// Offline is true when the box started from the local home without
	// reaching storage (ResumeOffline).
	Offline bool
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
	st = b.resolve(ctx, snaps, st)
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
			st.Tree, st.Snapshot, st.SavedAt = newest.Tree, newest.origin(), newest.Time
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
		st.Tree, st.Snapshot, st.SavedAt = newest.Tree, newest.origin(), newest.Time
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
	st.Lease = LeaseHeld
	if err := saveState(b.cfg.StateDir, st); err != nil {
		return ResumeResult{}, err
	}
	return res, nil
}

// ErrNoSavePoint: Revert To ▸ Last Save Point with no save point in the
// box's history.
var ErrNoSavePoint = errors.New("no save point yet")

// RevertResult is what RevertToLastSavePoint did.
type RevertResult struct {
	Restored    Snapshot // the save point now in the home
	SavedBefore Snapshot // the home as it was just before, saved first
}

// RevertToLastSavePoint puts the newest save point back in the home. The
// home is saved first (an autosave, with the usual lease checks), so the
// revert itself can be undone from history; only then is the save point
// restored over it. The box stays open on this machine.
func (b *Box) RevertToLastSavePoint(ctx context.Context) (RevertResult, error) {
	empty, err := b.homeEmpty()
	if err != nil {
		return RevertResult{}, err
	}
	if empty {
		return RevertResult{}, ErrEmptyHome
	}
	snaps, err := b.restic.snapshots(ctx)
	if err != nil {
		return RevertResult{}, err
	}
	var point Snapshot
	found := false
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Kind == KindPoint {
			point, found = snaps[i], true
			break
		}
	}
	if !found {
		return RevertResult{}, ErrNoSavePoint
	}
	before, err := b.saveListed(ctx, snaps, SaveOptions{Kind: SaveAutosave})
	if err != nil {
		return RevertResult{}, fmt.Errorf("save the home before reverting: %w", err)
	}
	st, _, err := loadState(b.cfg.StateDir)
	if err != nil {
		return RevertResult{}, err
	}
	// The home is saved (before), so restoring over it loses nothing; if the
	// restore fails half way, the state still says dirty and the next save
	// or a retry fixes it.
	if err := b.restic.restoreTo(ctx, point, b.cfg.HomeDir, true); err != nil {
		return RevertResult{}, fmt.Errorf("restore save point %s: %w", point.short(), err)
	}
	// The home now holds the save point's content, which is saved; the save
	// made just before is the newest recorded save.
	st.Tree, st.Snapshot, st.Dirty, st.Lease, st.SavedAt = point.Tree, point.origin(), true, LeaseHeld, before.Time
	if err := saveState(b.cfg.StateDir, st); err != nil {
		return RevertResult{}, err
	}
	return RevertResult{Restored: point, SavedBefore: before}, nil
}

// ErrOfflineUnsafe: storage is unreachable and this machine cannot show that
// starting from its local home is safe.
var ErrOfflineUnsafe = errors.New("cannot start offline")

// ResumeOffline starts the box from the local home without reaching storage.
// It is allowed only when this machine's state says its home equals the
// last save it made or restored (not opened since) and it holds the lease or
// released it cleanly. The box is marked open; saves wait until storage is
// reachable, and the next online Resume applies the resume rules (rule 4 if
// another machine saved meanwhile, so nothing is lost).
func (b *Box) ResumeOffline() (ResumeResult, error) {
	st, known, err := loadState(b.cfg.StateDir)
	switch {
	case err != nil:
		return ResumeResult{}, err
	case !known:
		return ResumeResult{}, fmt.Errorf("%w: this machine has never synced the box", ErrOfflineUnsafe)
	case st.Tree == "" && st.Snapshot == "":
		return ResumeResult{}, fmt.Errorf("%w: the box has never been saved from this machine", ErrOfflineUnsafe)
	case st.Dirty:
		return ResumeResult{}, fmt.Errorf("%w: the box was not closed cleanly here, so its home may differ from the last save", ErrOfflineUnsafe)
	case st.Lease != LeaseHeld && st.Lease != LeaseReleased:
		return ResumeResult{}, fmt.Errorf("%w: this machine's lease state is unknown", ErrOfflineUnsafe)
	}
	st.Dirty = true
	if err := saveState(b.cfg.StateDir, st); err != nil {
		return ResumeResult{}, err
	}
	return ResumeResult{Rule: 3, Action: ActionStartLocal, Offline: true}, nil
}

// saveOrphan saves the local home as orphaned and records it as the synced
// state, so restoring over it afterwards loses nothing.
func (b *Box) saveOrphan(ctx context.Context, snaps []Snapshot, st State) (Snapshot, error) {
	s, err := b.backup(ctx, snaps, st, []string{tagMachinePfx + b.cfg.MachineID, tagOrphaned})
	if err != nil {
		return Snapshot{}, err
	}
	if err := saveState(b.cfg.StateDir, State{Tree: s.Tree, Snapshot: s.origin(), Dirty: false, KeyHint: st.KeyHint, SavedAt: s.Time}); err != nil {
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
	return saveState(b.cfg.StateDir, State{Tree: s.Tree, Snapshot: s.origin(), Dirty: false, KeyHint: st.KeyHint, SavedAt: s.Time})
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
	return b.saveListed(ctx, snaps, opts)
}

// saveListed is Save with the repository already listed.
func (b *Box) saveListed(ctx context.Context, snaps []Snapshot, opts SaveOptions) (Snapshot, error) {
	st, _, err := loadState(b.cfg.StateDir)
	if err != nil {
		return Snapshot{}, err
	}
	st = b.resolve(ctx, snaps, st)
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
	// The new snapshot is created already carrying the lease tag (or none on
	// release): no separate tag step. The save is complete once restic's
	// summary reports the snapshot ID and the state records it; its tree is
	// filled in at the next listing.
	id, err := b.backupID(ctx, snaps, st, tags)
	if err != nil {
		return Snapshot{}, err
	}
	when := b.restic.lastStart // restic's own time for this snapshot
	if when.IsZero() {
		when = b.cfg.Now()
	}
	s := Snapshot{ID: id, Tags: tags, Hostname: snapshotHost, Time: when}
	s.parseTags()
	lease := LeaseHeld
	if opts.Kind == SaveRelease {
		lease = LeaseReleased
	}
	if err := saveState(b.cfg.StateDir, State{Snapshot: id, Dirty: opts.Kind != SaveRelease, Lease: lease, KeyHint: st.KeyHint, SavedAt: s.Time}); err != nil {
		return Snapshot{}, fmt.Errorf("save %s made but not recorded: %w", s.short(), err)
	}
	// Older snapshots may still carry active tags; readers only look at the
	// current save, and Housekeep clears the rest when the machine is idle.
	return s, nil
}

// backup saves the home with tags, using the snapshot this machine last
// synced to as the parent so unchanged files are not re-read, and returns
// the new snapshot as restic lists it.
func (b *Box) backup(ctx context.Context, snaps []Snapshot, st State, tags []string) (Snapshot, error) {
	id, err := b.backupID(ctx, snaps, st, tags)
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

// backupID saves the home with tags and returns the new snapshot's ID from
// restic's summary, without listing afterwards.
func (b *Box) backupID(ctx context.Context, snaps []Snapshot, st State, tags []string) (string, error) {
	parent := ""
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		synced := (st.Tree != "" && s.Tree == st.Tree) || (st.Snapshot != "" && s.origin() == st.Snapshot)
		if synced || (parent == "" && s.Machine == b.cfg.MachineID) {
			parent = s.ID
			if synced {
				break
			}
		}
	}
	exclude := ""
	if b.cfg.ExcludeFile != "" {
		info, err := b.cfg.Executor.PathInfo(ctx, b.cfg.ExcludeFile)
		if err != nil {
			return "", err
		}
		if info.Exists && !info.IsDir {
			exclude = b.cfg.ExcludeFile
		}
	}
	return b.restic.backup(ctx, backupArgs{home: b.cfg.HomeDir, tags: tags, parent: parent, excludeFile: exclude})
}

// resolve fills in the tree of the snapshot this machine last recorded, and
// this machine's key hint, from a fresh listing, saving the state when it
// learns something.
func (b *Box) resolve(ctx context.Context, snaps []Snapshot, st State) State {
	changed := false
	if st.Tree == "" && st.Snapshot != "" {
		for _, s := range snaps {
			if s.origin() == st.Snapshot {
				st.Tree, changed = s.Tree, true
				break
			}
		}
	}
	if st.KeyHint == "" && len(snaps) > 0 {
		if id, err := b.restic.currentKeyID(ctx); err == nil && id != "" {
			st.KeyHint, changed = id, true
		}
	}
	b.restic.keyHint = st.KeyHint
	if changed {
		_ = saveState(b.cfg.StateDir, st) // a hint, not a commitment
	}
	return st
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
	snaps, err := b.restic.snapshots(ctx)
	if err != nil {
		return nil, err
	}
	// Only the current save's active tag is the lease; older ones are
	// leftovers housekeeping has not cleared yet.
	cur, _ := current(snaps)
	for i := range snaps {
		if snaps[i].ID != cur.ID {
			snaps[i].Active = ""
		}
	}
	return snaps, nil
}

// Forget applies the retention policy (keep the last 20, hourly for 24
// hours, daily for 14 days, weekly for 8 weeks, monthly for 12 months, and
// every orphaned save). With prune, unreferenced data is removed too.
func (b *Box) Forget(ctx context.Context, prune bool) error {
	return b.Housekeep(ctx, prune)
}

// Housekeep tidies the repository while the machine is idle: it clears
// active tags left on snapshots older than the current save, then applies
// the retention policy. The current save is never removed: it is taken out
// of the policy's plan before anything is forgotten.
func (b *Box) Housekeep(ctx context.Context, prune bool) error {
	snaps, err := b.restic.snapshots(ctx)
	if err != nil {
		return err
	}
	cur, saved := current(snaps)
	byTag := map[string][]string{}
	for _, s := range snaps {
		if s.Active != "" && (!saved || s.ID != cur.ID) {
			byTag[tagActivePfx+s.Active] = append(byTag[tagActivePfx+s.Active], s.ID)
		}
	}
	for tag, ids := range byTag {
		if err := b.restic.untag(ctx, ids, tag); err != nil {
			return err
		}
	}
	if len(byTag) > 0 { // untag rewrote IDs
		if snaps, err = b.restic.snapshots(ctx); err != nil {
			return err
		}
		cur, saved = current(snaps)
	}
	plan, err := b.restic.expired(ctx)
	if err != nil {
		return err
	}
	keep := slices.DeleteFunc(plan, func(id string) bool { return saved && id == cur.ID })
	return b.restic.forgetIDs(ctx, keep, prune)
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
