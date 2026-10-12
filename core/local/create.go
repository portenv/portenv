// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/portenv/portenv/core/keys"
	boxsync "github.com/portenv/portenv/core/sync"
)

// Making boxes from the app (ADR 0019): the app makes the keys and stores
// them in the Keychain, then hands them over with CreateBox, which writes
// no key anywhere. portenvd never derives a recovery password: it receives
// one, uses it to add and check the recovery key, and drops it.

// recoveryHost labels the recovery key in restic key list. It isn't secret.
const recoveryHost = "portenv-recovery"

var (
	// boxIDRE is a box ID from the app's pool (and portenv init's format).
	// The recovery key's package has the same pattern; portenvd doesn't
	// link that package (ADR 0019, test 8).
	boxIDRE = regexp.MustCompile(`^box-[0-9a-f]{12}$`)
	// recoveryPasswordRE is a derived recovery password: 32 bytes as hex.
	// A recovery key (24 letters and digits) never matches.
	recoveryPasswordRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CreateRequest is a box to make (CreateBox) or to restore with the
// recovery key (RecoverBox). It's never logged.
type CreateRequest struct {
	Name, BoxID, Image                   string
	Storage, StorageHostKey, StorageREST string
	// Keys are exactly the box's key IDs for its storage (KeyIDs).
	Keys map[string][]byte
	// RecoveryPassword is derived from the recovery key in the app.
	RecoveryPassword []byte
}

// CreateOptions are for tests.
type CreateOptions struct {
	// Queued runs when the call has to wait for another one to finish.
	Queued func()
	// Fault runs before each step (D1 to D6, then "done": after the config
	// is written, before the pending record goes) and stops CreateBox with
	// its error.
	Fault func(step string) error
}

// CreateErrorKind says what went wrong, for portenvd's status code.
type CreateErrorKind int

const (
	ErrInvalid     CreateErrorKind = iota // a bad name, box ID, key set or password
	ErrNameTaken                          // another box has the name
	ErrMismatch                           // the box ID or repository belongs to something else
	ErrUnreachable                        // the storage can't be reached
	ErrUnsupported                        // storage that CreateBox can't set up yet
	ErrDeadline                           // restic missed its deadline (ADR 0012)
	ErrInternal                           // anything else
)

// CreateError is one plain line, never holding a key.
type CreateError struct {
	Kind CreateErrorKind
	Msg  string
	// Err is the cause, when there is one (never shown: restic's own words
	// stay in Msg's summary).
	Err error
}

func (e *CreateError) Error() string { return e.Msg }
func (e *CreateError) Unwrap() error { return e.Err }

func createErr(kind CreateErrorKind, format string, a ...any) error {
	return &CreateError{Kind: kind, Msg: fmt.Sprintf(format, a...)}
}

// resticFailed turns a restic failure into a CreateError: a missed deadline
// keeps its kind, anything else is internal.
func resticFailed(what string, err error) error {
	var de *boxsync.DeadlineError
	if errors.As(err, &de) {
		return &CreateError{Kind: ErrDeadline, Msg: fmt.Sprintf("restic took too long to %s; try again.", what), Err: err}
	}
	// One line: restic's last words, joined.
	detail := strings.Join(strings.Fields(strings.ReplaceAll(err.Error(), "\n", "; ")), " ")
	return &CreateError{Kind: ErrInternal, Msg: fmt.Sprintf("Couldn't %s: %s", what, detail), Err: err}
}

// Creation is a box CreateBox started and hasn't finished: its pending
// record, which holds no key.
type Creation struct {
	BoxID          string `json:"box_id"`
	Name           string `json:"name"`
	Image          string `json:"image"`
	Storage        string `json:"storage"`
	StorageHostKey string `json:"storage_host_key,omitempty"`
	StorageREST    string `json:"storage_rest,omitempty"`
}

func (c Creation) config() BoxConfig {
	return BoxConfig{ID: c.BoxID, Name: c.Name, Image: c.Image, Storage: c.Storage, StorageHostKey: c.StorageHostKey, StorageREST: c.StorageREST}
}

// sameBox: a and b are the same box (the image is left out: the first open
// pins it to a digest).
func sameBox(a, b BoxConfig) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Storage == b.Storage && a.StorageHostKey == b.StorageHostKey && a.StorageREST == b.StorageREST
}

func (e *Env) pendingPath(boxID string) string {
	return filepath.Join(e.Dir, "state", boxID, "creating.json")
}

// Creations lists the boxes being created.
func (e *Env) Creations() ([]Creation, error) {
	ents, err := os.ReadDir(filepath.Join(e.Dir, "state"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cs []Creation
	for _, ent := range ents {
		if !boxIDRE.MatchString(ent.Name()) {
			continue
		}
		c, ok, err := e.creation(ent.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			cs = append(cs, c)
		}
	}
	return cs, nil
}

func (e *Env) creation(boxID string) (Creation, bool, error) {
	b, err := os.ReadFile(e.pendingPath(boxID)) // #nosec G304 -- a checked box ID in the Portenv directory
	if errors.Is(err, os.ErrNotExist) {
		return Creation{}, false, nil
	}
	if err != nil {
		return Creation{}, false, err
	}
	var c Creation
	if err := json.Unmarshal(b, &c); err != nil {
		return Creation{}, false, fmt.Errorf("pending record for %s: %w", boxID, err)
	}
	return c, true, nil
}

// takeSlot waits for the create slot until ctx ends; queued, if set, runs
// when another call holds it.
func (e *Env) takeSlot(ctx context.Context, queued func()) (release func(), err error) {
	e.createSlotOnce.Do(func() { e.createSlot = make(chan struct{}, 1) })
	select {
	case e.createSlot <- struct{}{}:
		return func() { <-e.createSlot }, nil
	default:
	}
	if queued != nil {
		queued()
	}
	select {
	case e.createSlot <- struct{}{}:
		return func() { <-e.createSlot }, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &CreateError{Kind: ErrDeadline, Msg: "Portenv is still setting up another workspace. Try again in a moment.", Err: ctx.Err()}
		}
		return nil, ctx.Err()
	}
}

// DiscardCreation forgets an unfinished creation, and the keys held for
// it. A repository it left (with no snapshot: nothing could save into it)
// is never deleted. A finished box keeps its keys.
func (e *Env) DiscardCreation(ctx context.Context, boxID string) error {
	if !boxIDRE.MatchString(boxID) {
		return createErr(ErrInvalid, "A box ID is box- and 12 lowercase hex digits.")
	}
	release, err := e.takeSlot(ctx, nil)
	if err != nil {
		return err
	}
	defer release()
	if err := e.removePending(boxID); err != nil {
		return err
	}
	if _, made := e.boxWithID(boxID); !made && e.Provided != nil {
		e.Provided.Forget(boxID, StorageKeyID(boxID), RESTKeyID(boxID))
	}
	return nil
}

func (e *Env) removePending(boxID string) error {
	if err := os.Remove(e.pendingPath(boxID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// writeAtomic writes data to path through a temporary file, synced, then
// renamed, then the directory synced: a crash leaves the old file or the
// new one, never part of one.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// A temporary file of its own (mode 0600), so writers never share one.
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := writeSynced(f, data); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	d, err := os.Open(dir) // #nosec G304 -- a directory in the Portenv directory
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func writeSynced(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// writeNewConfig writes a box's config at D6: it replaces only a config
// with the same box ID, and publishes a new one only where there is none.
// A config it can't read (corrupt, no permission) is never written over.
func (e *Env) writeNewConfig(c BoxConfig) error {
	existing, err := e.LoadBox(c.Name)
	switch {
	case err == nil && existing.ID == c.ID:
		return e.writeConfig(c)
	case err == nil:
		return createErr(ErrNameTaken, "A box named %s already exists.", c.Name)
	case errors.Is(err, fs.ErrNotExist):
		return e.PublishConfig(c)
	default:
		return &CreateError{Kind: ErrInternal, Msg: fmt.Sprintf("Couldn't read the config already named %s; nothing was written.", c.Name), Err: err}
	}
}

// PublishConfig writes a new box's config where there is none: the file is
// written and synced under a temporary name, then hard-linked into place,
// which fails if anything is already there, whoever wrote it. portenv init
// and join publish the same way.
func (e *Env) PublishConfig(c BoxConfig) error {
	p, err := e.ConfigPath(c.Name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(p)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := writeSynced(f, append(data, '\n')); err != nil {
		return err
	}
	if err := os.Link(tmp, p); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return createErr(ErrNameTaken, "A box named %s already exists.", c.Name)
		}
		return err
	}
	d, err := os.Open(dir) // #nosec G304 -- a directory in the Portenv directory
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func (e *Env) writeConfig(c BoxConfig) error {
	p, err := e.ConfigPath(c.Name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p, append(data, '\n'))
}

// boxWithID finds the box configured here with this ID.
func (e *Env) boxWithID(boxID string) (BoxConfig, bool) {
	ents, _ := os.ReadDir(filepath.Join(e.Dir, "boxes"))
	for _, ent := range ents {
		name, ok := strings.CutSuffix(ent.Name(), ".json")
		if !ok {
			continue
		}
		if c, err := e.LoadBox(name); err == nil && c.ID == boxID {
			return c, true
		}
	}
	return BoxConfig{}, false
}

// checkPending refuses a box whose name another box being created holds,
// or whose box ID is being created with other settings (or at all, for a
// recovery).
func (e *Env) checkPending(c BoxConfig, recovering bool) error {
	pending, err := e.Creations()
	if err != nil {
		return err
	}
	for _, p := range pending {
		switch {
		case p.BoxID == c.ID && (recovering || !sameBox(p.config(), c)):
			return createErr(ErrMismatch, "Box ID %s is already being created as %s with other settings.", c.ID, p.Name)
		case p.BoxID != c.ID && p.Name == c.Name:
			return createErr(ErrNameTaken, "A box named %s already exists.", c.Name)
		}
	}
	return nil
}

// checkRequest is D1's check of everything the request says, before
// anything is written.
func checkRequest(req CreateRequest) (BoxConfig, error) {
	if !NameRE.MatchString(req.Name) {
		return BoxConfig{}, createErr(ErrInvalid, "Box names use letters, digits, '.', '_' and '-', and start with a letter or digit.")
	}
	if !boxIDRE.MatchString(req.BoxID) {
		return BoxConfig{}, createErr(ErrInvalid, "A box ID is box- and 12 lowercase hex digits.")
	}
	c := BoxConfig{ID: req.BoxID, Name: req.Name, Image: req.Image, Storage: req.Storage, StorageHostKey: req.StorageHostKey, StorageREST: req.StorageREST}
	if c.Image == "" {
		c.Image = DefaultImage
	}
	switch {
	case c.Storage == "":
	case strings.HasPrefix(c.Storage, "sftp:"), strings.HasPrefix(c.Storage, "s3:"):
		// The storage key has to be authorised on the server, and the S3
		// credentials have no home yet: both come with the server setup.
		return BoxConfig{}, createErr(ErrUnsupported, "Making a box that saves to a server or a bucket comes with server setup. Keep it on this Mac for now.")
	case filepath.IsAbs(c.Storage) && filepath.Clean(c.Storage) == c.Storage:
	default:
		return BoxConfig{}, createErr(ErrInvalid, "Storage is this Mac or an absolute folder.")
	}
	want := KeyIDs(c)
	if len(req.Keys) != len(want) {
		return BoxConfig{}, createErr(ErrInvalid, "%s needs exactly its keys: %s.", c.Name, strings.Join(want, ", "))
	}
	for id, k := range req.Keys {
		if !slices.Contains(want, id) {
			return BoxConfig{}, createErr(ErrInvalid, "%s needs exactly its keys: %s.", c.Name, strings.Join(want, ", "))
		}
		if len(k) == 0 {
			return BoxConfig{}, createErr(ErrInvalid, "The key %s is empty.", id)
		}
	}
	if !recoveryPasswordRE.Match(req.RecoveryPassword) {
		return BoxConfig{}, createErr(ErrInvalid, "The recovery password must be the 64 hex characters derived from the recovery key.")
	}
	return c, nil
}

// reachStorage is D1's check that the storage is there: this Mac's own
// folder is made; another folder (a drive) must be a folder Portenv can
// write to.
func (e *Env) reachStorage(c BoxConfig) error {
	if c.Storage == "" {
		return os.MkdirAll(filepath.Join(e.Dir, "Repositories"), 0o700)
	}
	fi, err := os.Stat(c.Storage) // #nosec G703 -- an absolute folder the user chose
	if err != nil {
		return createErr(ErrUnreachable, "%s can't be reached. Connect the drive and try again.", c.Storage)
	}
	if !fi.IsDir() {
		return createErr(ErrUnreachable, "%s isn't a folder. Choose a folder to keep saves in.", c.Storage)
	}
	probe, err := os.CreateTemp(c.Storage, ".portenv-write-check-*") // #nosec G703 -- an absolute folder the user chose
	if err != nil {
		return createErr(ErrUnreachable, "Portenv can't write to %s. Choose a folder you can write to.", c.Storage)
	}
	_ = probe.Close()
	if err := os.Remove(probe.Name()); err != nil {
		return &CreateError{Kind: ErrInternal, Msg: fmt.Sprintf("Couldn't remove Portenv's write check %s.", probe.Name()), Err: err}
	}
	return nil
}

// repoEngine opens the box's repository from this machine with one
// password, for key work only: no home, every command on the host.
func (e *Env) repoEngine(c BoxConfig, password []byte) (*boxsync.Box, error) {
	bin := HostRestic()
	if bin == "" {
		return nil, createErr(ErrInternal, "restic isn't installed next to Portenv.")
	}
	repo := (&Session{E: e, Cfg: c}).HostRepo()
	state := filepath.Join(e.Dir, "state", c.ID)
	ex := boxsync.LocalExecutor{Bin: bin, CacheDir: filepath.Join(state, "host-cache"), ControlDir: ControlDir()}
	b, err := boxsync.Open(boxsync.Config{
		Executor: ex, MetaExecutor: ex, Repository: repo, MetaRepository: repo,
		Password: password, BoxID: c.ID, MachineID: e.Machine,
		HomeDir: "/nonexistent", StateDir: state, Deadlines: testDeadlines(),
	})
	if err != nil {
		return nil, resticFailed("start restic", err)
	}
	return b, nil
}

// holdKeys keeps the box's keys in memory for its first open, like
// ProvideKeys.
func (e *Env) holdKeys(ks map[string][]byte) {
	if e.Provided == nil {
		e.Provided = keys.NewMemory()
	}
	for id, k := range ks {
		_ = e.Provided.Put(id, k)
	}
}

// CreateBox makes a new box (ADR 0019, steps D1 to D6). The repository is
// created with the box's repository key, the recovery key is added and
// both are checked, and only then is the box's config written: a box can
// save only once its config exists, so no box ever has a save that its
// repository key and its recovery key don't both open. Each step is safe
// to repeat, and the box ID makes the whole call idempotent: it returns
// true when an earlier call had finished it.
func (e *Env) CreateBox(ctx context.Context, req CreateRequest, opt CreateOptions) (alreadyCreated bool, err error) {
	release, err := e.takeSlot(ctx, opt.Queued)
	if err != nil {
		return false, err
	}
	defer release()
	fault := opt.Fault
	if fault == nil {
		fault = func(string) error { return nil }
	}

	// D1: check the request, the names and IDs in use, and the storage.
	if err := fault("D1"); err != nil {
		return false, err
	}
	c, err := checkRequest(req)
	if err != nil {
		return false, err
	}
	if existing, err := e.LoadBox(c.Name); err == nil {
		if !sameBox(existing, c) {
			if existing.ID == c.ID {
				return false, createErr(ErrMismatch, "Box ID %s is already %s with other settings.", c.ID, existing.Name)
			}
			return false, createErr(ErrNameTaken, "A box named %s already exists.", c.Name)
		}
		e.holdKeys(req.Keys)
		_ = e.removePending(c.ID)
		return true, nil
	}
	if other, ok := e.boxWithID(c.ID); ok {
		return false, createErr(ErrMismatch, "Box ID %s is already %s with other settings.", c.ID, other.Name)
	}
	if err := e.checkPending(c, false); err != nil {
		return false, err
	}
	if err := e.reachStorage(c); err != nil {
		return false, err
	}
	e.holdKeys(req.Keys)

	// D2: the pending record reserves the name. It holds no key.
	if err := fault("D2"); err != nil {
		return false, err
	}
	rec, _ := json.MarshalIndent(Creation{BoxID: c.ID, Name: c.Name, Image: c.Image, Storage: c.Storage, StorageHostKey: c.StorageHostKey, StorageREST: c.StorageREST}, "", "  ")
	if err := writeAtomic(e.pendingPath(c.ID), append(rec, '\n')); err != nil {
		return false, err
	}

	// D3: the repository, with the repository key. One already at the path
	// is used only if that key opens it, and is otherwise never touched.
	if err := fault("D3"); err != nil {
		return false, err
	}
	withKey, err := e.repoEngine(c, req.Keys[c.ID])
	if err != nil {
		return false, err
	}
	ok, err := withKey.Opens(ctx)
	switch {
	case errors.Is(err, boxsync.ErrNoRepository):
		repo := (&Session{E: e, Cfg: c}).HostRepo()
		if err := os.MkdirAll(filepath.Dir(repo), 0o700); err != nil { // #nosec G703 -- the box's own storage folder
			return false, err
		}
		if err := withKey.Create(ctx); err != nil {
			return false, resticFailed("create the repository", err)
		}
	case err != nil:
		return false, resticFailed("open the repository", err)
	case !ok:
		return false, createErr(ErrMismatch, "A repository for %s already exists, and this box's keys don't open it.", c.ID)
	}

	// D4: the recovery key, unless it's there already.
	if err := fault("D4"); err != nil {
		return false, err
	}
	withRecovery, err := e.repoEngine(c, req.RecoveryPassword)
	if err != nil {
		return false, err
	}
	if ok, err := withRecovery.Opens(ctx); err != nil {
		return false, resticFailed("open the repository", err)
	} else if !ok {
		if err := withKey.AddKey(ctx, req.RecoveryPassword, recoveryHost); err != nil {
			return false, resticFailed("add the recovery key", err)
		}
	}

	// D5: each key opens the repository on its own.
	if err := fault("D5"); err != nil {
		return false, err
	}
	for _, b := range []*boxsync.Box{withRecovery, withKey} {
		if ok, err := b.Opens(ctx); err != nil {
			return false, resticFailed("check the keys", err)
		} else if !ok {
			return false, createErr(ErrInternal, "The new keys for %s don't open its repository.", c.Name)
		}
	}

	// D6: the config, last; then the pending record goes.
	if err := fault("D6"); err != nil {
		return false, err
	}
	c.Recovery = true
	if err := e.writeNewConfig(c); err != nil {
		return false, err
	}
	if err := fault("done"); err != nil {
		return false, err
	}
	if err := e.removePending(c.ID); err != nil {
		return false, err
	}
	return false, nil
}

// RecoverBox restores a box on this machine from its repository with only
// the recovery-derived password (ADR 0019): it adds this machine's new
// repository key (from req.Keys, which the app stored first), checks it,
// and writes the config. It returns true when an earlier call had
// finished it.
func (e *Env) RecoverBox(ctx context.Context, req CreateRequest) (alreadyRecovered bool, err error) {
	release, err := e.takeSlot(ctx, nil)
	if err != nil {
		return false, err
	}
	defer release()
	c, err := checkRequest(req)
	if err != nil {
		return false, err
	}
	if existing, err := e.LoadBox(c.Name); err == nil {
		if !sameBox(existing, c) {
			if existing.ID == c.ID {
				return false, createErr(ErrMismatch, "Box ID %s is already %s with other settings.", c.ID, existing.Name)
			}
			return false, createErr(ErrNameTaken, "A box named %s already exists.", c.Name)
		}
		e.holdKeys(req.Keys)
		return true, nil
	}
	if other, ok := e.boxWithID(c.ID); ok {
		return false, createErr(ErrMismatch, "Box ID %s is already %s here.", c.ID, other.Name)
	}
	if err := e.checkPending(c, true); err != nil {
		return false, err
	}
	if err := e.reachStorage(c); err != nil {
		return false, err
	}
	withRecovery, err := e.repoEngine(c, req.RecoveryPassword)
	if err != nil {
		return false, err
	}
	switch ok, err := withRecovery.Opens(ctx); {
	case errors.Is(err, boxsync.ErrNoRepository):
		return false, createErr(ErrMismatch, "There's no box %s in that storage.", c.ID)
	case err != nil:
		return false, resticFailed("open the repository", err)
	case !ok:
		return false, createErr(ErrMismatch, "This recovery key doesn't open box %s.", c.ID)
	}
	e.holdKeys(req.Keys)
	withKey, err := e.repoEngine(c, req.Keys[c.ID])
	if err != nil {
		return false, err
	}
	if ok, err := withKey.Opens(ctx); err != nil {
		return false, resticFailed("open the repository", err)
	} else if !ok {
		if err := withRecovery.AddKey(ctx, req.Keys[c.ID], e.Machine); err != nil {
			return false, resticFailed("add this Mac's key", err)
		}
		if ok, err := withKey.Opens(ctx); err != nil || !ok {
			return false, createErr(ErrInternal, "This Mac's new key for %s doesn't open its repository.", c.Name)
		}
	}
	c.Recovery = true
	if err := e.writeNewConfig(c); err != nil {
		return false, err
	}
	return false, nil
}
