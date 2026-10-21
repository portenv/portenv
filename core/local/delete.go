// SPDX-License-Identifier: Apache-2.0

package local

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Deleting a box (ADR 0029). portenvd runs the steps; these are the parts
// that touch this machine's Portenv folder and the box's saves on it.

// The deletion steps, in order, as recorded in a box's config.
const (
	DeleteStepInstance = "instance"
	DeleteStepHome     = "home"
	// DeleteStepAttachments removes the box's attachment store on this
	// machine (ADR 0030 point 6).
	DeleteStepAttachments = "attachments"
	DeleteStepSaves       = "saves"
	DeleteStepKey         = "key"
	DeleteStepRecord      = "record"
)

// DeleteSteps is the order of the steps.
var DeleteSteps = []string{DeleteStepInstance, DeleteStepHome, DeleteStepAttachments, DeleteStepSaves, DeleteStepKey, DeleteStepRecord}

// DeleteProgress is a deletion that started and hasn't finished, kept in
// the box's config so the config, removed last, takes it with it.
type DeleteProgress struct {
	// Step is the step the deletion is at: done up to the one before it.
	Step string `json:"step"`
	// SavesLeftAt: the saves aren't on this machine and stay where they
	// are, as people read the place.
	SavesLeftAt string `json:"saves_left_at,omitempty"`
}

// safeIDRE is a box ID that can be one path element: no separators, never
// "." or "..".
var safeIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// AttachmentsDir is the box's attachment store on this machine (ADR 0030
// point 6): the support folder's Attachments/<box-id>.
func (e *Env) AttachmentsDir(c BoxConfig) (string, bool) {
	if !safeIDRE.MatchString(c.ID) || c.ID == "." || c.ID == ".." {
		return "", false
	}
	return filepath.Join(e.Dir, "Attachments", c.ID), true
}

// RemoveAttachments removes the box's attachment store, and only that
// box's: a missing folder is fine, so the step is safe to repeat.
func (e *Env) RemoveAttachments(c BoxConfig) error {
	dir, ok := e.AttachmentsDir(c)
	if !ok {
		return fmt.Errorf("%q isn't a workspace ID", c.ID)
	}
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ErrDeleting: the box is being deleted; it can't be opened.
var ErrDeleting = errors.New("the workspace is being deleted")

// deletingError says a box whose deletion stopped can't be opened.
func deletingError(name string) error {
	return fmt.Errorf("deleting %s stopped before it finished. Choose Delete Workspace… again to finish: %w", name, ErrDeleting)
}

// LocalRepository is the folder holding the box's saves when they are on
// this machine (this Mac only, or a folder the person chose), and false
// when they're elsewhere (a server, a bucket).
func (e *Env) LocalRepository(c BoxConfig) (string, bool) {
	if !safeIDRE.MatchString(c.ID) {
		return "", false
	}
	switch {
	case c.Storage == "":
		return filepath.Join(e.Dir, "Repositories", c.ID), true
	case filepath.IsAbs(c.Storage):
		return filepath.Join(c.Storage, "boxes", c.ID), true // workspace-word: ok (a path)
	}
	return "", false
}

// SavesPlace is where the box's saves are, as people read it: "this Mac",
// the folder, or the server's or bucket's name.
func SavesPlace(c BoxConfig) string {
	switch {
	case c.Storage == "":
		return "this Mac"
	case filepath.IsAbs(c.Storage):
		return c.Storage
	case strings.HasPrefix(c.Storage, "sftp:"):
		user, host, _ := SFTPTarget(c.Storage)
		if user != "" {
			return ServerDisplayName(user + "@" + host)
		}
		return host
	case strings.HasPrefix(c.Storage, "s3:"):
		if u, err := url.Parse(strings.TrimPrefix(c.Storage, "s3:")); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return c.Storage
}

// CountSaves counts the saves in a repository folder without a key (restic
// keeps one file per snapshot in snapshots/) and the folder's size on disk.
// A folder that isn't there has none.
func CountSaves(repo string) (saves int, bytes int64, err error) {
	ents, err := os.ReadDir(filepath.Join(repo, "snapshots"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, 0, err
	}
	for _, ent := range ents {
		if ent.Type().IsRegular() {
			saves++
		}
	}
	err = filepath.WalkDir(repo, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			bytes += info.Size()
		}
		return nil
	})
	return saves, bytes, err
}

// RemoveLocalRepository deletes the box's saves on this machine: exactly
// its own repository folder, never the folder holding every box's, never
// another box's. A folder already gone is fine. Saves elsewhere are never
// touched here.
func (e *Env) RemoveLocalRepository(c BoxConfig) error {
	dir, ok := e.LocalRepository(c)
	if !ok {
		return fmt.Errorf("the saves of %s aren't on this Mac", c.Name)
	}
	if filepath.Base(dir) != c.ID || filepath.Clean(dir) != dir {
		return fmt.Errorf("the saves of %s aren't in a folder of their own", c.Name)
	}
	return os.RemoveAll(dir)
}

// RemoveBoxState deletes this machine's state for the box (sync state,
// restic's cache, its location): state/<box-id>.
func (e *Env) RemoveBoxState(c BoxConfig) error {
	if !safeIDRE.MatchString(c.ID) {
		return fmt.Errorf("invalid workspace ID %q", c.ID)
	}
	return os.RemoveAll(filepath.Join(e.Dir, "state", c.ID))
}

// RemoveConfig deletes the box's record, boxes/<name>.json, if it is still
// this box's (the same ID). It goes last: with it go the box and its
// deletion progress, in one step.
func (e *Env) RemoveConfig(c BoxConfig) error {
	cur, err := e.LoadBox(c.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.ID != c.ID {
		return fmt.Errorf("%s is another workspace now: not removed", c.Name)
	}
	p, err := e.ConfigPath(c.Name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(p))
}

// syncDir makes a removal in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- the Portenv directory's boxes folder
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
