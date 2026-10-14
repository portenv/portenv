// SPDX-License-Identifier: Apache-2.0

package local

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sunPathMax is macOS's sun_path: a socket's path, its NUL included.
const sunPathMax = 104

// TestSocketsLiveInThePerUserTempFolder (ADR 0021): on a Mac, the folder
// for Portenv's sockets is in the per-user temp folder confstr gives, never
// the TMPDIR variable or /tmp, and its longest socket path fits.
func TestSocketsLiveInThePerUserTempFolder(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the per-user temp folder is a Mac's")
	}
	t.Setenv("TMPDIR", "/tmp/not-this/")
	dir, err := SocketDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(dir, "/tmp/") || !strings.HasPrefix(dir, "/var/folders/") || filepath.Base(dir) != "pv" {
		t.Fatalf("socket folder %q", dir)
	}
	// The longest names: the ControlMaster's (%C is 40 hex digits), a
	// forward's, and 1b's per-run agent socket.
	for _, name := range []string{"c-" + strings.Repeat("f", 40), "fwd-" + strings.Repeat("f", 12) + ".sock", "a-" + strings.Repeat("f", 8)} {
		if p := filepath.Join(dir, name); len(p)+1 > sunPathMax {
			t.Fatalf("%s is %d bytes, over macOS's %d", p, len(p)+1, sunPathMax)
		}
	}
}

// TestTheSocketFolderIsChecked: made 0700, and refused (never used) when
// it's someone else's, open to others, or a symlink, so nobody can make it
// first and listen there (ADR 0021's squatting race).
func TestTheSocketFolderIsChecked(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pv")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("made %v, %v", fi.Mode(), err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(dir); err == nil {
		t.Fatal("a folder others can read was used")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("a symlink was used")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(file); err == nil {
		t.Fatal("a file was used")
	}
}

// TestSSHUsesTheCheckedFolder: the ControlMaster socket is c-%C in it.
func TestSSHUsesTheCheckedFolder(t *testing.T) {
	args, err := controlOptions()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	dir, _ := SocketDir()
	if !strings.Contains(joined, "ControlPath="+filepath.Join(dir, "c-%C")) || !strings.Contains(joined, "ControlPersist=600") {
		t.Fatalf("options %q", joined)
	}
}
