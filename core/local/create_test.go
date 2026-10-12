// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/keys/recovery"
	boxsync "github.com/portenv/portenv/core/sync"
)

// The ADR's test vector: tests use it, never a generated key.
const vectorText = "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5"

func TestMain(m *testing.M) {
	// CreateBox runs restic on this machine: the pinned one from bin/.
	if os.Getenv("PORTENV_RESTIC") == "" {
		if p := os.Getenv("RESTIC"); p != "" {
			_ = os.Setenv("PORTENV_RESTIC", p)
		} else if p, err := filepath.Abs(filepath.Join("..", "..", "bin", "restic")); err == nil {
			if _, err := os.Stat(p); err == nil {
				_ = os.Setenv("PORTENV_RESTIC", p)
			}
		}
	}
	if os.Getenv("PORTENV_TEST_CREATE_CRASH") != "" {
		os.Exit(crashingCreate())
	}
	os.Exit(m.Run())
}

func needRestic(t *testing.T) string {
	t.Helper()
	p := HostRestic()
	if p == "" {
		t.Fatal("restic not found: run `make tools` (it installs the pinned version into bin/) or set RESTIC")
	}
	return p
}

func newCreateEnv(t *testing.T) *Env {
	t.Helper()
	return &Env{Dir: t.TempDir(), Machine: "mac-1", Provided: keys.NewMemory()}
}

// vectorPassword is the recovery-derived password for boxID, from the
// vector key.
func vectorPassword(t *testing.T, boxID string) []byte {
	t.Helper()
	k, err := recovery.Parse(vectorText)
	if err != nil {
		t.Fatal(err)
	}
	pw, err := recovery.Password(k, boxID)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(pw)
}

// repoKeyFor is a fixed repository key per box ID, so a crashed process and
// its retry hand over the same one.
func repoKeyFor(boxID string) []byte {
	sum := sha256.Sum256([]byte("repository key for " + boxID))
	return []byte(hex.EncodeToString(sum[:]))
}

func createRequest(t *testing.T, name, boxID, storage string) CreateRequest {
	t.Helper()
	return CreateRequest{
		Name: name, BoxID: boxID, Image: "img", Storage: storage,
		Keys:             map[string][]byte{boxID: repoKeyFor(boxID)},
		RecoveryPassword: vectorPassword(t, boxID),
	}
}

// repoOf is where CreateBox puts the request's repository, seen from here.
func repoOf(e *Env, req CreateRequest) string {
	s := &Session{E: e, Cfg: BoxConfig{ID: req.BoxID, Storage: req.Storage}}
	return s.HostRepo()
}

// engine opens the sync engine on the box's repository with password,
// with a home directory for saving and restoring.
func engine(t *testing.T, repo, boxID, machine string, password []byte, root string) *boxsync.Box {
	t.Helper()
	home := filepath.Join(root, machine, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := boxsync.Open(boxsync.Config{
		Restic: needRestic(t), Repository: repo, Password: password, BoxID: boxID, MachineID: machine,
		HomeDir: home, StateDir: filepath.Join(root, machine, "state"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// hostEngine opens the repository with password only (no home), to check
// what opens it.
func hostEngine(t *testing.T, repo, boxID string, password []byte) *boxsync.Box {
	t.Helper()
	state := t.TempDir()
	ex := boxsync.LocalExecutor{Bin: needRestic(t), CacheDir: filepath.Join(state, "cache")}
	b, err := boxsync.Open(boxsync.Config{
		Executor: ex, MetaExecutor: ex, Repository: repo, MetaRepository: repo, Password: password,
		BoxID: boxID, MachineID: "checker", HomeDir: "/nonexistent", StateDir: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func opens(t *testing.T, repo, boxID string, password []byte) bool {
	t.Helper()
	ok, err := hostEngine(t, repo, boxID, password).Opens(context.Background())
	if errors.Is(err, boxsync.ErrNoRepository) {
		return false
	}
	if err != nil {
		t.Fatalf("open %s: %v", repo, err)
	}
	return ok
}

// assertInvariant is ADR 0019's guarantee after any step, failure or
// crash: no snapshot unless the box's config exists and both keys open the
// repository; a box still being created can't be opened; and no key is on
// disk.
func assertInvariant(t *testing.T, e *Env, req CreateRequest) {
	t.Helper()
	repo := repoOf(e, req)
	c, err := e.LoadBox(req.Name)
	configExists := err == nil
	if configExists {
		if c.ID != req.BoxID || !c.Recovery {
			t.Fatalf("config %+v", c)
		}
		if !opens(t, repo, req.BoxID, req.Keys[req.BoxID]) || !opens(t, repo, req.BoxID, req.RecoveryPassword) {
			t.Fatal("the config exists but a key doesn't open the repository")
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "config")); err == nil && opens(t, repo, req.BoxID, req.Keys[req.BoxID]) {
		snaps, err := hostEngine(t, repo, req.BoxID, req.Keys[req.BoxID]).History(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(snaps) != 0 && !configExists {
			t.Fatalf("%d snapshots without a config", len(snaps))
		}
	}
	if !configExists {
		if _, err := e.Open(req.Name); err == nil {
			t.Fatal("a box still being created opened")
		}
	}
	assertNoSecrets(t, e.Dir, nil, req.Keys[req.BoxID], req.RecoveryPassword, []byte(vectorText), []byte(strings.ReplaceAll(vectorText, "-", "")))
}

// assertNoSecrets: no file under dir and no error message holds any secret.
func assertNoSecrets(t *testing.T, dir string, errs []error, secrets ...[]byte) {
	t.Helper()
	check := func(where string, data []byte) {
		for _, s := range secrets {
			if len(s) > 0 && (bytes.Contains(data, s) || bytes.Contains(bytes.ToLower(data), bytes.ToLower(s))) {
				t.Fatalf("a secret is in %s", where)
			}
		}
	}
	for _, err := range errs {
		if err != nil {
			check("an error message", []byte(err.Error()))
		}
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// The repository's own files are restic's encrypted data.
		if strings.Contains(p, string(filepath.Separator)+"Repositories"+string(filepath.Separator)) || strings.Contains(p, string(filepath.Separator)+"boxes"+string(filepath.Separator)+"box-") {
			return nil
		}
		b, rerr := os.ReadFile(p) // #nosec G304 -- the test's own directory
		if rerr == nil {
			check(p, b)
		}
		return nil
	})
}

func keyHosts(t *testing.T, repo, boxID string, password []byte) []string {
	t.Helper()
	h, err := hostEngine(t, repo, boxID, password).KeyHosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func assertOneWorkingBox(t *testing.T, e *Env, req CreateRequest) {
	t.Helper()
	assertInvariant(t, e, req)
	if _, err := e.LoadBox(req.Name); err != nil {
		t.Fatalf("no box after the retry: %v", err)
	}
	hosts := keyHosts(t, repoOf(e, req), req.BoxID, req.Keys[req.BoxID])
	n := 0
	for _, h := range hosts {
		if h == "portenv-recovery" {
			n++
		}
	}
	if len(hosts) != 2 || n != 1 {
		t.Fatalf("keys %v, want the repository key and one recovery key", hosts)
	}
	if cs, err := e.Creations(); err != nil || len(cs) != 0 {
		t.Fatalf("creations left: %v, %v", cs, err)
	}
}

func kindOf(err error) CreateErrorKind {
	var ce *CreateError
	if errors.As(err, &ce) {
		return ce.Kind
	}
	return -1
}

// TestCreateBoxMakesABoxBothKeysOpen: CreateBox makes the repository with
// the repository key, adds the recovery key, checks both, and writes the
// config last, with the recovery flag. No key is written anywhere.
func TestCreateBoxMakesABoxBothKeysOpen(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	already, err := e.CreateBox(context.Background(), req, CreateOptions{})
	if err != nil || already {
		t.Fatalf("CreateBox: %v, %v", already, err)
	}
	assertOneWorkingBox(t, e, req)
	if _, err := os.Stat(filepath.Join(e.Dir, "keys")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a key directory exists: %v", err)
	}
	if k, err := e.Provided.Get(req.BoxID); err != nil || !bytes.Equal(k, req.Keys[req.BoxID]) {
		t.Fatal("the repository key isn't held in memory for the first open")
	}
	if _, err := e.Provided.Get("recovery"); err == nil {
		t.Fatal("the recovery password was kept")
	}
}

// steps are CreateBox's steps (ADR 0019), and "done": after the config is
// written, before the pending record goes.
var steps = []string{"D1", "D2", "D3", "D4", "D5", "D6", "done"}

// TestCreateBoxFailingAtEachStep: a failure before any step leaves no box
// that can save without both keys, and a retry with the same box ID ends in
// one working box with one recovery key.
func TestCreateBoxFailingAtEachStep(t *testing.T) {
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			e := newCreateEnv(t)
			req := createRequest(t, "acme-api", "box-0123456789ab", "")
			injected := errors.New("injected failure")
			_, err := e.CreateBox(context.Background(), req, CreateOptions{Fault: func(s string) error {
				if s == step {
					return injected
				}
				return nil
			}})
			if !errors.Is(err, injected) {
				t.Fatalf("failure at %s: %v", step, err)
			}
			assertInvariant(t, e, req)
			already, err := e.CreateBox(context.Background(), req, CreateOptions{})
			if err != nil {
				t.Fatalf("retry after %s: %v", step, err)
			}
			if already != (step == "done") {
				t.Fatalf("retry after %s: already_created %v", step, already)
			}
			assertOneWorkingBox(t, e, req)
		})
	}
}

// crashingCreate is the child process of TestCreateBoxCrashBetweenSteps: it
// runs CreateBox and dies at the step named, without cleaning up.
func crashingCreate() int {
	step := os.Getenv("PORTENV_TEST_CREATE_CRASH")
	e := &Env{Dir: os.Getenv("PORTENV_TEST_CREATE_DIR"), Machine: "mac-1", Provided: keys.NewMemory()}
	k, _ := recovery.Parse(vectorText)
	pw, _ := recovery.Password(k, "box-0123456789ab")
	req := CreateRequest{Name: "acme-api", BoxID: "box-0123456789ab", Image: "img",
		Keys: map[string][]byte{"box-0123456789ab": repoKeyFor("box-0123456789ab")}, RecoveryPassword: []byte(pw)}
	_, err := e.CreateBox(context.Background(), req, CreateOptions{Fault: func(s string) error {
		if s == step {
			os.Exit(3) // killed here: no deferred cleanup runs
		}
		return nil
	}})
	fmt.Fprintln(os.Stderr, "CreateBox returned without crashing:", err)
	return 1
}

// TestCreateBoxCrashBetweenSteps: a process killed between any two steps
// leaves the same invariant, and the next portenvd finishes the box.
func TestCreateBoxCrashBetweenSteps(t *testing.T) {
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			e := newCreateEnv(t)
			cmd := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- this test binary
			cmd.Env = append(os.Environ(), "PORTENV_TEST_CREATE_CRASH="+step, "PORTENV_TEST_CREATE_DIR="+e.Dir)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatalf("the child didn't die at %s: %v\n%s", step, err, out)
			}
			req := createRequest(t, "acme-api", "box-0123456789ab", "")
			assertInvariant(t, e, req)
			if _, err := e.CreateBox(context.Background(), req, CreateOptions{}); err != nil {
				t.Fatalf("finishing after a crash at %s: %v", step, err)
			}
			assertOneWorkingBox(t, e, req)
		})
	}
}

// treeSum hashes every file's path, mode and content under dir.
func treeSum(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		_, _ = fmt.Fprintf(h, "%s %v %d\n", rel, fi.Mode(), fi.ModTime().UnixNano())
		if !d.IsDir() {
			b, err := os.ReadFile(p) // #nosec G304 -- the test's own directory
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestCreateBoxLeavesAForeignRepositoryUntouched: a repository already at
// the path that these keys don't open fails with FailedPrecondition and is
// left byte for byte as it was; discarding the creation leaves it too.
func TestCreateBoxLeavesAForeignRepositoryUntouched(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	repo := repoOf(e, req)
	if err := hostEngine(t, repo, req.BoxID, []byte("someone else's key")).Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := treeSum(t, repo)
	_, err := e.CreateBox(context.Background(), req, CreateOptions{})
	if kindOf(err) != ErrMismatch {
		t.Fatalf("CreateBox over a foreign repository: %v, want a mismatch", err)
	}
	if treeSum(t, repo) != before {
		t.Fatal("the foreign repository changed")
	}
	if _, err := e.LoadBox("acme-api"); err == nil {
		t.Fatal("a config was written")
	}
	cs, err := e.Creations()
	if err != nil || len(cs) != 1 || cs[0].BoxID != req.BoxID || cs[0].Name != "acme-api" {
		t.Fatalf("creations %v, %v", cs, err)
	}
	if err := e.DiscardCreation(req.BoxID); err != nil {
		t.Fatal(err)
	}
	if cs, _ := e.Creations(); len(cs) != 0 {
		t.Fatalf("still creating after a discard: %v", cs)
	}
	if treeSum(t, repo) != before {
		t.Fatal("discarding the creation touched the repository")
	}
	assertNoSecrets(t, e.Dir, []error{err}, req.Keys[req.BoxID], req.RecoveryPassword)
}

// TestCreateBoxIsIdempotentOnBoxID: the same request again is
// already_created; the same box ID with other settings, or the name of
// another box, is refused.
func TestCreateBoxIsIdempotentOnBoxID(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	ctx := context.Background()
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	if _, err := e.CreateBox(ctx, req, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if already, err := e.CreateBox(ctx, req, CreateOptions{}); err != nil || !already {
		t.Fatalf("the same request again: %v, %v, want already_created", already, err)
	}
	other := req
	other.Storage = t.TempDir()
	if _, err := e.CreateBox(ctx, other, CreateOptions{}); kindOf(err) != ErrMismatch {
		t.Fatalf("the same box ID with other storage: %v, want a mismatch", err)
	}
	renamed := req
	renamed.Name = "acme-web"
	if _, err := e.CreateBox(ctx, renamed, CreateOptions{}); kindOf(err) != ErrMismatch {
		t.Fatalf("the same box ID under another name: %v, want a mismatch", err)
	}
	taken := createRequest(t, "acme-api", "box-0123456789ac", "")
	_, err := e.CreateBox(ctx, taken, CreateOptions{})
	if kindOf(err) != ErrNameTaken || err.Error() != "A box named acme-api already exists." {
		t.Fatalf("another box with the name: %v", err)
	}
	assertOneWorkingBox(t, e, req)
}

// TestACreationReservesItsName: a box being created holds its name until it
// is finished or discarded.
func TestACreationReservesItsName(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	ctx := context.Background()
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	stop := errors.New("stop")
	if _, err := e.CreateBox(ctx, req, CreateOptions{Fault: func(s string) error {
		if s == "D3" {
			return stop
		}
		return nil
	}}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	second := createRequest(t, "acme-api", "box-0123456789ac", "")
	if _, err := e.CreateBox(ctx, second, CreateOptions{}); kindOf(err) != ErrNameTaken {
		t.Fatalf("the name of a box being created: %v", err)
	}
	if err := e.DiscardCreation(req.BoxID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateBox(ctx, second, CreateOptions{}); err != nil {
		t.Fatalf("after the discard: %v", err)
	}
	assertOneWorkingBox(t, e, second)
}

// TestCreateBoxChecksTheRequestFirst: a bad request writes nothing.
func TestCreateBoxChecksTheRequestFirst(t *testing.T) {
	t.Parallel()
	good := createRequest(t, "acme-api", "box-0123456789ab", "")
	k, _ := recovery.Parse(vectorText)
	drives := t.TempDir()
	notAFolder := filepath.Join(drives, "a-file")
	if err := os.WriteFile(notAFolder, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := filepath.Join(drives, "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) }) // #nosec G302 -- so the test's folder can be removed
	cases := []struct {
		name string
		edit func(*CreateRequest)
		want CreateErrorKind
	}{
		{"name", func(r *CreateRequest) { r.Name = "../etc" }, ErrInvalid},
		{"box ID", func(r *CreateRequest) {
			r.Keys = map[string][]byte{"box-1": repoKeyFor("box-1")}
			r.BoxID = "box-1"
		}, ErrInvalid},
		{"upper-case box ID", func(r *CreateRequest) {
			r.Keys = map[string][]byte{"box-0123456789AB": repoKeyFor("x")}
			r.BoxID = "box-0123456789AB"
		}, ErrInvalid},
		{"missing key", func(r *CreateRequest) { r.Keys = map[string][]byte{} }, ErrInvalid},
		{"extra key", func(r *CreateRequest) {
			r.Keys = map[string][]byte{r.BoxID: repoKeyFor(r.BoxID), r.BoxID + "-storage": []byte("x")}
		}, ErrInvalid},
		{"empty key", func(r *CreateRequest) { r.Keys = map[string][]byte{r.BoxID: nil} }, ErrInvalid},
		{"no recovery password", func(r *CreateRequest) { r.RecoveryPassword = nil }, ErrInvalid},
		{"the recovery key itself", func(r *CreateRequest) { r.RecoveryPassword = []byte(k.Grouped()) }, ErrInvalid},
		{"upper-case recovery password", func(r *CreateRequest) {
			r.RecoveryPassword = bytes.ToUpper(r.RecoveryPassword)
		}, ErrInvalid},
		{"SFTP storage", func(r *CreateRequest) {
			r.Storage = "sftp:portenv-storage@host:/storage"
			r.StorageHostKey = "ssh-ed25519 AAAA"
			r.Keys = map[string][]byte{r.BoxID: repoKeyFor(r.BoxID), r.BoxID + "-storage": []byte("x")}
		}, ErrUnsupported},
		{"S3 storage", func(r *CreateRequest) { r.Storage = "s3:https://s3.example.com/bucket" }, ErrUnsupported},
		{"a drive that isn't there", func(r *CreateRequest) { r.Storage = "/Volumes/portenv-test-not-mounted" }, ErrUnreachable},
		{"a file, not a folder", func(r *CreateRequest) { r.Storage = notAFolder }, ErrUnreachable},
	}
	if os.Geteuid() != 0 { // root writes anywhere
		cases = append(cases, struct {
			name string
			edit func(*CreateRequest)
			want CreateErrorKind
		}{"a folder Portenv can't write to", func(r *CreateRequest) { r.Storage = readOnly }, ErrUnreachable})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newCreateEnv(t)
			req := good
			req.Keys = map[string][]byte{good.BoxID: good.Keys[good.BoxID]}
			c.edit(&req)
			_, err := e.CreateBox(context.Background(), req, CreateOptions{})
			if kindOf(err) != c.want {
				t.Fatalf("%v, want kind %v", err, c.want)
			}
			for _, sub := range []string{"state", "boxes", "Repositories", "keys"} {
				if ents, _ := os.ReadDir(filepath.Join(e.Dir, sub)); len(ents) != 0 {
					t.Fatalf("%s written for a bad request", sub)
				}
			}
			assertNoSecrets(t, e.Dir, []error{err}, good.Keys[good.BoxID], good.RecoveryPassword, []byte(k.Grouped()))
		})
	}
}

// fileSums maps each file under dir (relative path) to its SHA-256.
func fileSums(t *testing.T, dir string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) // #nosec G304 -- the test's own directory
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sum := sha256.Sum256(b)
		sums[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sums
}

// TestRestoreWithOnlyTheRecoveryKey (ADR 0019, test 5): a box made by
// CreateBox and saved, then every key and all state lost; a fresh Mac with
// only the printed recovery key and the storage gets every file back.
func TestRestoreWithOnlyTheRecoveryKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	storage := filepath.Join(root, "drive") // an external drive, say
	if err := os.MkdirAll(storage, 0o700); err != nil {
		t.Fatal(err)
	}

	// The first Mac: CreateBox, then a save with known files.
	first := &Env{Dir: filepath.Join(root, "first-mac"), Machine: "mac-a", Provided: keys.NewMemory()}
	req := createRequest(t, "acme-api", "box-0123456789ab", storage)
	if _, err := first.CreateBox(ctx, req, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	b := engine(t, repoOf(first, req), req.BoxID, "mac-a", req.Keys[req.BoxID], filepath.Join(root, "first-engine"))
	if _, err := b.Resume(ctx, boxsync.ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "first-engine", "mac-a", "home")
	files := map[string]string{
		"work/acme-api/README.md":        "# acme-api\n",
		"work/acme-api/src/retries.ts":   "export const retries = 3;\n",
		"work/.portenv/apt-packages.txt": "ripgrep\n",
	}
	for rel, content := range files {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Save(ctx, boxsync.SaveOptions{Kind: boxsync.SaveRelease}); err != nil {
		t.Fatal(err)
	}
	want := fileSums(t, home)

	// Everything on the first Mac is lost: its key store, state, configs
	// and restic's cache.
	if err := os.RemoveAll(first.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "first-engine")); err != nil {
		t.Fatal(err)
	}

	// A clean Mac with the recovery key, typed from paper.
	printed := strings.ToLower(strings.ReplaceAll(vectorText, "-", " "))
	typo := []byte(printed)
	typo[3] = 'r' // "k7qr …"
	if _, err := recovery.Parse(string(typo)); err == nil {
		t.Fatal("a key with one wrong character got past the check")
	}
	key, err := recovery.Parse(printed)
	if err != nil {
		t.Fatal(err)
	}
	clean := &Env{Dir: filepath.Join(root, "clean-mac"), Machine: "mac-b", Provided: keys.NewMemory()}
	ents, err := os.ReadDir(filepath.Join(storage, "boxes"))
	if err != nil || len(ents) != 1 {
		t.Fatalf("boxes on the drive: %v, %v", ents, err)
	}
	boxID := ents[0].Name()
	pw, err := recovery.Password(key, boxID)
	if err != nil {
		t.Fatal(err)
	}

	// Control: another valid recovery key doesn't open the box.
	otherKey, err := recovery.Generate(bytes.NewReader(bytes.Repeat([]byte{7}, 23)))
	if err != nil {
		t.Fatal(err)
	}
	otherPW, _ := recovery.Password(otherKey, boxID)
	newKey := repoKeyFor("the clean Mac's key")
	rec := CreateRequest{Name: "acme-api", BoxID: boxID, Image: "img", Storage: storage,
		Keys: map[string][]byte{boxID: newKey}, RecoveryPassword: []byte(otherPW)}
	if _, err := clean.RecoverBox(ctx, rec); kindOf(err) != ErrMismatch {
		t.Fatalf("another recovery key: %v, want a mismatch", err)
	}
	if _, err := clean.LoadBox("acme-api"); err == nil {
		t.Fatal("a config after a failed recovery")
	}

	rec.RecoveryPassword = []byte(pw)
	if already, err := clean.RecoverBox(ctx, rec); err != nil || already {
		t.Fatalf("RecoverBox: %v, %v", already, err)
	}
	if already, err := clean.RecoverBox(ctx, rec); err != nil || !already {
		t.Fatalf("RecoverBox again: %v, %v, want already_recovered", already, err)
	}
	c, err := clean.LoadBox("acme-api")
	if err != nil || c.ID != boxID || !c.Recovery {
		t.Fatalf("recovered config %+v, %v", c, err)
	}
	if !opens(t, repoOf(clean, rec), boxID, newKey) {
		t.Fatal("the clean Mac's new key doesn't open the repository")
	}

	// The box opens with the new key and every file comes back.
	nb := engine(t, repoOf(clean, rec), boxID, "mac-b", newKey, filepath.Join(root, "clean-engine"))
	if _, err := nb.Resume(ctx, boxsync.ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	got := fileSums(t, filepath.Join(root, "clean-engine", "mac-b", "home"))
	for rel, sum := range want {
		if got[rel] != sum {
			t.Fatalf("%s: got %q, want %q", rel, got[rel], sum)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("restored %d files, saved %d", len(got), len(want))
	}
	assertNoSecrets(t, clean.Dir, nil, newKey, []byte(pw), []byte(vectorText), []byte(key.Canonical()))
}

// TestConcurrentCreatesOfOneName: two CreateBox calls at once (a
// double-click, or a retry while the first still runs) with one name and
// two box IDs make exactly one box. The other is refused as taken, its
// pending record and keys are never written, and neither repository has a
// snapshot. Without one lock over the whole call, both pass D1 and the
// second config overwrites the first.
func TestConcurrentCreatesOfOneName(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	ctx := context.Background()
	reqs := []CreateRequest{
		createRequest(t, "acme-api", "box-0123456789ab", ""),
		createRequest(t, "acme-api", "box-0123456789ac", ""),
	}
	// Each call waits at D2 (up to a second) for the other to get there
	// too: without the lock both do, and both reserve the name.
	var atD2 sync.WaitGroup
	atD2.Add(len(reqs))
	errs := make([]error, len(reqs))
	var wg sync.WaitGroup
	for i, req := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.CreateBox(ctx, req, CreateOptions{Fault: func(s string) error {
				if s == "D2" {
					atD2.Done()
					waitOrTimeout(&atD2, time.Second)
				}
				return nil
			}})
		}()
	}
	wg.Wait()

	made, refused := -1, -1
	for i, err := range errs {
		switch {
		case err == nil:
			made = i
		case kindOf(err) == ErrNameTaken:
			refused = i
		default:
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if made < 0 || refused < 0 {
		t.Fatalf("results %v, want one box made and one call refused as taken", errs)
	}
	c, err := e.LoadBox("acme-api")
	if err != nil || c.ID != reqs[made].BoxID {
		t.Fatalf("config %+v, %v, want box %s", c, err, reqs[made].BoxID)
	}
	assertOneWorkingBox(t, e, reqs[made])
	loser := reqs[refused]
	if _, ok, err := e.creation(loser.BoxID); err != nil || ok {
		t.Fatalf("the refused call left a pending record: %v, %v", ok, err)
	}
	if _, err := e.Provided.Get(loser.BoxID); err == nil {
		t.Fatal("the refused call's key is held")
	}
	for _, req := range reqs {
		repo := repoOf(e, req)
		if _, err := os.Stat(filepath.Join(repo, "config")); err != nil {
			continue
		}
		snaps, err := hostEngine(t, repo, req.BoxID, req.Keys[req.BoxID]).History(ctx)
		if err != nil || len(snaps) != 0 {
			t.Fatalf("%s: %d snapshots, %v", req.BoxID, len(snaps), err)
		}
	}
}

// waitOrTimeout waits for wg, or gives up after d.
func waitOrTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// TestCreateBoxRefusesANameTakenBeforeD6: if a config with the name appears
// with another box ID while CreateBox runs (another tool wrote it), D6
// refuses as taken and writes nothing over it.
func TestCreateBoxRefusesANameTakenBeforeD6(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	other := BoxConfig{ID: "box-0123456789ac", Name: "acme-api", Image: "img"}
	_, err := e.CreateBox(context.Background(), req, CreateOptions{Fault: func(s string) error {
		if s == "D6" {
			return e.writeConfig(other)
		}
		return nil
	}})
	if kindOf(err) != ErrNameTaken {
		t.Fatalf("CreateBox: %v, want the name taken", err)
	}
	if c, err := e.LoadBox("acme-api"); err != nil || c != other {
		t.Fatalf("config %+v, %v: the other box's config was overwritten", c, err)
	}
	assertNoSecrets(t, e.Dir, []error{err}, req.Keys[req.BoxID], req.RecoveryPassword)
}

// TestWriteAtomicTempNamesDontCollide: writers of one path never share a
// temporary file, and none is left behind.
func TestWriteAtomicTempNamesDontCollide(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "boxes", "acme-api.json")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = writeAtomic(p, bytes.Repeat([]byte{byte('a' + i)}, 1<<16))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(p) // #nosec G304 -- the test's own file
	if err != nil || len(b) != 1<<16 || bytes.Count(b, b[:1]) != len(b) {
		t.Fatalf("the file is a mix of writes (%d bytes, %v)", len(b), err)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("left in the folder: %v", ents)
	}
}

// TestRecoverBoxRefusesANameBeingCreated: a recovery and an unfinished
// creation can't share a name.
func TestRecoverBoxRefusesANameBeingCreated(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	ctx := context.Background()
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	stop := errors.New("stop")
	if _, err := e.CreateBox(ctx, req, CreateOptions{Fault: func(s string) error {
		if s == "D3" {
			return stop
		}
		return nil
	}}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	rec := createRequest(t, "acme-api", "box-0123456789ac", "")
	if _, err := e.RecoverBox(ctx, rec); kindOf(err) != ErrNameTaken {
		t.Fatalf("RecoverBox onto a name being created: %v, want the name taken", err)
	}
	same := createRequest(t, "acme-web", req.BoxID, "")
	if _, err := e.RecoverBox(ctx, same); kindOf(err) != ErrMismatch {
		t.Fatalf("RecoverBox of a box ID being created: %v, want a mismatch", err)
	}
	if _, err := e.LoadBox("acme-api"); err == nil {
		t.Fatal("a config was written")
	}
}

// TestDiscardCreationForgetsItsKeys: a discarded box's keys don't stay in
// portenvd's memory.
func TestDiscardCreationForgetsItsKeys(t *testing.T) {
	t.Parallel()
	e := newCreateEnv(t)
	ctx := context.Background()
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	stop := errors.New("stop")
	if _, err := e.CreateBox(ctx, req, CreateOptions{Fault: func(s string) error {
		if s == "D3" {
			return stop
		}
		return nil
	}}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if _, err := e.Provided.Get(req.BoxID); err != nil {
		t.Fatal("the key isn't held after the failed call")
	}
	if err := e.DiscardCreation(req.BoxID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Provided.Get(req.BoxID); err == nil {
		t.Fatal("the discarded box's key is still held")
	}
	// A finished box keeps its keys: the discard has nothing to forget.
	done := createRequest(t, "acme-web", "box-0123456789ac", "")
	if _, err := e.CreateBox(ctx, done, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.DiscardCreation(done.BoxID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Provided.Get(done.BoxID); err != nil {
		t.Fatal("discarding a finished box forgot its key")
	}
}

// TestBoxIDPatternIsTheRecoveryKeys: portenvd's box ID pattern is the one
// the recovery key's derivation accepts (portenvd doesn't link that
// package; this test may).
func TestBoxIDPatternIsTheRecoveryKeys(t *testing.T) {
	if boxIDRE.String() != recovery.BoxIDPattern.String() {
		t.Fatalf("local %q, recovery %q", boxIDRE.String(), recovery.BoxIDPattern.String())
	}
}
