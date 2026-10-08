// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// world is one box's repository shared by simulated machines. Every test
// runs against a real restic repository in a temporary directory.
type world struct {
	t        *testing.T
	root     string
	repo     string
	password []byte
	restic   string

	initialised  bool
	inspectorBox *machine
	byTree       map[string]map[string]string // snapshot contents by tree ID
}

// machine is one simulated machine: its own view of the box's home, its own
// per-machine state, the shared repository.
type machine struct {
	*Box
	w    *world
	id   string
	home string
}

func resticBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("RESTIC"); p != "" {
		return p
	}
	if p, err := filepath.Abs(filepath.Join("..", "..", "bin", "restic")); err == nil {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("restic"); err == nil {
		return p
	}
	t.Fatal("restic not found: run `make tools` (it installs the pinned version into bin/) or set RESTIC")
	return ""
}

// newWorld returns a fresh repository. Every restic call costs about half a
// second of key derivation, so tests using a world run in parallel.
func newWorld(t *testing.T) *world {
	t.Helper()
	t.Parallel()
	root := t.TempDir()
	return &world{
		t: t, root: root, repo: filepath.Join(root, "storage", "boxes", "box1"),
		password: []byte("test-password"), restic: resticBinary(t),
		byTree: map[string]map[string]string{},
	}
}

func (w *world) machine(id string) *machine {
	return w.machineAt(id, time.Now)
}

func (w *world) machineAt(id string, now func() time.Time) *machine {
	w.t.Helper()
	home := filepath.Join(w.root, id, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		w.t.Fatal(err)
	}
	b, err := Open(Config{
		Restic:     w.restic,
		Repository: w.repo,
		Password:   w.password,
		BoxID:      "box1",
		MachineID:  id,
		HomeDir:    home,
		StateDir:   filepath.Join(w.root, id, "state"),
		Now:        now,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if !w.initialised {
		if err := b.Init(context.Background()); err != nil {
			w.t.Fatal(err)
		}
		w.initialised = true
	}
	return &machine{Box: b, w: w, id: id, home: home}
}

func (m *machine) write(rel, content string) {
	m.w.t.Helper()
	p := filepath.Join(m.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		m.w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		m.w.t.Fatal(err)
	}
}

func (m *machine) remove(rel string) {
	m.w.t.Helper()
	if err := os.Remove(filepath.Join(m.home, rel)); err != nil {
		m.w.t.Fatal(err)
	}
}

// files returns every regular file under dir as path → content.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p) // #nosec G304 -- test fixture
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (m *machine) files() map[string]string { return files(m.w.t, m.home) }

// inspector is a machine that only reads the repository.
func (w *world) inspector() *machine {
	if w.inspectorBox == nil {
		w.inspectorBox = w.machine("inspector")
	}
	return w.inspectorBox
}

// snapshotFiles restores a snapshot into a scratch directory and returns its
// files. Contents are cached by tree ID: equal trees have equal contents.
func (w *world) snapshotFiles(s Snapshot) map[string]string {
	w.t.Helper()
	if f, ok := w.byTree[s.Tree]; ok {
		return f
	}
	dir := filepath.Join(w.t.TempDir(), "restore")
	if err := w.inspector().restic.restoreTo(context.Background(), s, dir, false); err != nil {
		w.t.Fatal(err)
	}
	f := files(w.t, dir)
	w.byTree[s.Tree] = f
	return f
}

func (w *world) snapshots() []Snapshot {
	w.t.Helper()
	snaps, err := w.inspector().History(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return snaps
}

func (w *world) check() {
	w.t.Helper()
	if out, err := w.inspector().restic.run(context.Background(), "check"); err != nil {
		w.t.Fatalf("restic check: %v\n%s", err, out)
	}
}

func equalFiles(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Fatalf("%s:\n got  %v\n want %v", what, sortedKeys(got), sortedKeys(want))
	}
}

func sortedKeys(m map[string]string) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)
	for i, k := range keys {
		keys[i] = k + "=" + m[k]
	}
	return keys
}

func token() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func mustResume(t *testing.T, m *machine, opts ResumeOptions) ResumeResult {
	t.Helper()
	res, err := m.Resume(context.Background(), opts)
	if err != nil {
		t.Fatalf("%s: Resume: %v", m.id, err)
	}
	return res
}

func mustSave(t *testing.T, m *machine, kind SaveKind) Snapshot {
	t.Helper()
	s, err := m.Save(context.Background(), SaveOptions{Kind: kind})
	if err != nil {
		t.Fatalf("%s: Save(%v): %v", m.id, kind, err)
	}
	return m.w.resolve(s)
}

// resolve looks a saved snapshot up in the repository (by its original ID),
// for its tree and paths.
func (w *world) resolve(s Snapshot) Snapshot {
	w.t.Helper()
	for _, r := range w.snapshots() {
		if r.origin() == s.origin() {
			return r
		}
	}
	w.t.Fatalf("snapshot %s not in the repository", s.ID)
	return Snapshot{}
}

func isLeaseHeld(err error) (*LeaseHeldError, bool) {
	var e *LeaseHeldError
	return e, errors.As(err, &e)
}

func isLeaseChanged(err error) (*LeaseChangedError, bool) {
	var e *LeaseChangedError
	return e, errors.As(err, &e)
}

func hasTag(s Snapshot, tag string) bool { return slices.Contains(s.Tags, tag) }

func tagsWithPrefix(snaps []Snapshot, prefix string) []string {
	var out []string
	for _, s := range snaps {
		for _, tag := range s.Tags {
			if strings.HasPrefix(tag, prefix) {
				out = append(out, s.ID[:8]+":"+tag)
			}
		}
	}
	return out
}
