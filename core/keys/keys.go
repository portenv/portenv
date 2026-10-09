// SPDX-License-Identifier: Apache-2.0

package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// ErrNotFound means the store holds no key for the box.
var ErrNotFound = errors.New("no key stored for this box")

// ErrNeedsApproval means the Keychain would have to ask the person first.
// Background processes and scripts never raise that prompt: they get this
// one plain line instead, and the app (in front of the person) reads the key.
var ErrNeedsApproval = errors.New("Keychain needs your approval. Open Portenv on this Mac to allow it.") //nolint:staticcheck // ST1005: a sentence shown to people as it is (PLAN.md, Phase 1 item 1d)

// Memory holds keys in memory only: the keys the app read from the Keychain
// and handed to portenvd. Safe for concurrent use.
type Memory struct {
	mu   sync.Mutex
	keys map[string][]byte
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{keys: map[string][]byte{}} }

// Get returns a key handed over, or ErrNotFound.
func (m *Memory) Get(id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[id]; ok {
		return append([]byte(nil), k...), nil
	}
	return nil, ErrNotFound
}

// Put keeps a key in memory.
func (m *Memory) Put(id string, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[id] = append([]byte(nil), key...)
	return nil
}

// Layered reads First, then Then; it writes to Then.
type Layered struct{ First, Then Store }

// Get returns First's key, else Then's.
func (l Layered) Get(id string) ([]byte, error) {
	if k, err := l.First.Get(id); err == nil {
		return k, nil
	}
	return l.Then.Get(id)
}

// Put stores in Then.
func (l Layered) Put(id string, key []byte) error { return l.Then.Put(id, key) }

// Store keeps one repository key per box. Phase 0 minimum (docs/PLAN.md):
// the Keychain on a Mac, a root-only file on a server.
type Store interface {
	Get(boxID string) ([]byte, error)
	Put(boxID string, key []byte) error
}

// NewKey returns a random repository key: 32 random bytes, hex-encoded so it
// is a valid restic password.
func NewKey() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, "%x", b), nil
}

var boxIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// FileStore keeps keys as files in a directory that only its owner can
// read. It refuses to read or write if the directory or a key file is
// accessible to anyone else.
type FileStore struct{ Dir string }

func (f FileStore) path(boxID string) (string, error) {
	if !boxIDRE.MatchString(boxID) {
		return "", fmt.Errorf("invalid box ID %q", boxID)
	}
	return filepath.Join(f.Dir, boxID+".key"), nil
}

func (f FileStore) checkDir() error {
	fi, err := os.Stat(f.Dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("key directory %s has mode %v; it must be 0700", f.Dir, fi.Mode().Perm())
	}
	return nil
}

// Get implements Store.
func (f FileStore) Get(boxID string) ([]byte, error) {
	p, err := f.path(boxID)
	if err != nil {
		return nil, err
	}
	if err := f.checkDir(); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("key file %s has mode %v; it must be 0600", p, fi.Mode().Perm())
	}
	return os.ReadFile(p) // #nosec G304 -- validated box ID in the key directory
}

// Put implements Store. It never overwrites an existing key.
func (f FileStore) Put(boxID string, key []byte) error {
	p, err := f.path(boxID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	if err := f.checkDir(); err != nil {
		return err
	}
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- validated box ID
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("a key for box %s already exists", boxID)
	}
	if err != nil {
		return err
	}
	if _, err := fh.Write(key); err != nil {
		_ = fh.Close()
		return err
	}
	if err := fh.Sync(); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

// NewSSHKey returns a new Ed25519 key for SFTP storage: the private key in
// OpenSSH PEM form and the public key as an authorized_keys line.
func NewSSHKey(comment string) (private []byte, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(block), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment, nil
}
