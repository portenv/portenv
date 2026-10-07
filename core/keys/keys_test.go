// SPDX-License-Identifier: Apache-2.0

package keys

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestFileStore(t *testing.T) {
	s := FileStore{Dir: filepath.Join(t.TempDir(), "keys")}
	if _, err := s.Get("box1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before Put: %v", err)
	}
	k, err := NewKey()
	if err != nil || len(k) != 64 {
		t.Fatalf("NewKey: %q %v", k, err)
	}
	if err := s.Put("box1", k); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("box1")
	if err != nil || string(got) != string(k) {
		t.Fatalf("Get: %q %v", got, err)
	}
	if err := s.Put("box1", []byte("other")); err == nil {
		t.Fatal("Put overwrote an existing key")
	}
	fi, _ := os.Stat(filepath.Join(s.Dir, "box1.key"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", fi.Mode().Perm())
	}
}

func TestFileStoreRefusesOpenPermissions(t *testing.T) {
	s := FileStore{Dir: filepath.Join(t.TempDir(), "keys")}
	k, _ := NewKey()
	if err := s.Put("box1", k); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(s.Dir, "box1.key"), 0o644); err != nil { // #nosec G302 -- the test makes it unsafe on purpose
		t.Fatal(err)
	}
	if _, err := s.Get("box1"); err == nil {
		t.Fatal("read a world-readable key file")
	}
	if err := os.Chmod(s.Dir, 0o755); err != nil { // #nosec G302 -- the test makes it unsafe on purpose
		t.Fatal(err)
	}
	if err := s.Put("box2", k); err == nil {
		t.Fatal("wrote into a world-readable key directory")
	}
}

func TestFileStoreValidatesIDs(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	for _, id := range []string{"", "../x", "a/b"} {
		if err := s.Put(id, []byte("k")); err == nil {
			t.Errorf("Put accepted box ID %q", id)
		}
	}
}

func TestNewSSHKey(t *testing.T) {
	priv, pub, err := NewSSHKey("portenv storage box-1")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("private key does not parse: %v", err)
	}
	parsed, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		t.Fatalf("public key does not parse: %v", err)
	}
	if !bytes.Equal(parsed.Marshal(), signer.PublicKey().Marshal()) || comment != "portenv storage box-1" || parsed.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("public key %q does not match the private key", pub)
	}
}
