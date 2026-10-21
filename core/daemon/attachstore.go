// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The attachment store (ADR 0030 point 6): this Mac's support folder,
// one folder per workspace, one folder per attachment, named by its ULID:
//
//	Attachments/<box-id>/            0700
//	    <attachment-id>/             0700
//	        data                     0600, the bytes, exactly as checked
//	        meta.json                0600, the record
//
// portenvd is its one writer and reader; the app asks portenvd. Nothing
// here is ever sent anywhere. An attachment is made in .tmp-<id>/ and
// renamed into place only when it's whole and checked.

const (
	attachData     = "data"
	attachMetaName = "meta.json"
	// attachTmp starts a folder still being written.
	attachTmp = ".tmp-"
	// staleTmp is when a sweep removes a .tmp- folder a crash left.
	staleTmp = time.Hour
)

// attachMeta is meta.json: an attachment's record on this Mac.
type attachMeta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	MediaType   string    `json:"media_type"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	Width       int32     `json:"width,omitempty"`
	Height      int32     `json:"height,omitempty"`
	From        string    `json:"from"`
	CreatedAt   time.Time `json:"created_at"`
	MessageIDs  []string  `json:"message_ids"`
	DecisionIDs []string  `json:"decision_ids"`
}

// ensureStoreDir makes the workspace's store folder and its parent, 0700,
// refuses a link in their place, and excludes both from Time Machine,
// checked at every use.
func ensureStoreDir(dir string) error {
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s isn't a folder", d)
		}
		if fi.Mode().Perm() != 0o700 {
			if err := os.Chmod(d, 0o700); err != nil { // #nosec G302 -- a folder: 0700, the owner's only
				return err
			}
		}
		if err := excludeFromBackup(d); err != nil {
			return fmt.Errorf("exclude %s from backups: %w", d, err)
		}
	}
	return nil
}

// stagedAttachment is one attachment being written: its bytes go to
// .tmp-<id>/data, hashed and counted as they arrive.
type stagedAttachment struct {
	dir, id string
	f       *os.File
	h       hash.Hash
	size    int64
	max     int64
	over    bool
}

// stage starts an attachment in the workspace's store.
func stage(dir, id string, max int64) (*stagedAttachment, error) {
	if !ulidRE.MatchString(id) {
		return nil, fmt.Errorf("%q isn't an attachment id", id)
	}
	tmp := filepath.Join(dir, attachTmp+id)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(tmp, attachData), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the store's own path, from a ULID
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	return &stagedAttachment{dir: dir, id: id, f: f, h: sha256.New(), max: max}, nil
}

// Write takes the next bytes; past max it keeps none of them.
func (s *stagedAttachment) Write(p []byte) (int, error) {
	s.size += int64(len(p))
	if s.size > s.max {
		s.over = true
		return len(p), nil
	}
	s.h.Write(p)
	return s.f.Write(p)
}

func (s *stagedAttachment) sum() string { return hex.EncodeToString(s.h.Sum(nil)) }

func (s *stagedAttachment) tmp() string { return filepath.Join(s.dir, attachTmp+s.id) }

// finish closes the bytes, synced.
func (s *stagedAttachment) finish() error {
	if s.f == nil {
		return nil
	}
	err := errors.Join(s.f.Sync(), s.f.Close())
	s.f = nil
	return err
}

// open reads the staged bytes back, for the checks.
func (s *stagedAttachment) open() (*os.File, error) {
	return os.Open(filepath.Join(s.tmp(), attachData)) // #nosec G304 -- the store's own path
}

// discard removes the attachment, staged or committed.
func (s *stagedAttachment) discard() {
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	_ = os.RemoveAll(s.tmp())
	_ = os.RemoveAll(filepath.Join(s.dir, s.id))
}

// commit writes the record and moves the attachment into place.
func (s *stagedAttachment) commit(rec attachMeta) error {
	if err := writeRecord(s.tmp(), rec); err != nil {
		return err
	}
	return os.Rename(s.tmp(), filepath.Join(s.dir, s.id))
}

// writeRecord writes meta.json in an attachment's folder, 0600, through a
// temporary file and a rename.
func writeRecord(folder string, rec attachMeta) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := filepath.Join(folder, attachMetaName+".new")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- the store's own path
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(folder, attachMetaName))
}

// readRecord reads an attachment's record; an id that isn't a ULID, or
// isn't in the store, is not on this Mac.
func readRecord(dir, id string) (attachMeta, error) {
	var rec attachMeta
	if !ulidRE.MatchString(id) {
		return rec, errNotOnThisMac
	}
	b, err := os.ReadFile(filepath.Join(dir, id, attachMetaName)) // #nosec G304 -- a ULID in the store
	if err != nil {
		return rec, errNotOnThisMac
	}
	if json.Unmarshal(b, &rec) != nil || rec.ID != id {
		return rec, errNotOnThisMac
	}
	return rec, nil
}

// errNotOnThisMac is the app's line for an attachment this Mac's store
// doesn't hold.
var errNotOnThisMac = errors.New("This attachment isn't on this Mac.") //nolint:staticcheck // ST1005: a plain line, shown as it is

// storeUsage is the bytes the workspace's attachments use, those being
// written included.
func storeUsage(dir string) int64 {
	es, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range es {
		if fi, err := os.Lstat(filepath.Join(dir, e.Name(), attachData)); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
	}
	return n
}

// sweepStore removes the attachments no board line names (keep), and the
// .tmp- folders a crash left; only ever in this workspace's folder, and
// only names that are the store's own.
func sweepStore(dir string, keep map[string]bool, now time.Time) []string {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range es {
		name := e.Name()
		switch {
		case ulidRE.MatchString(name) && !keep[name]:
		case strings.HasPrefix(name, attachTmp) && ulidRE.MatchString(strings.TrimPrefix(name, attachTmp)):
			fi, err := e.Info()
			if err != nil || now.Sub(fi.ModTime()) < staleTmp {
				continue
			}
		default:
			continue
		}
		if os.RemoveAll(filepath.Join(dir, name)) == nil {
			removed = append(removed, name)
		}
	}
	return removed
}
