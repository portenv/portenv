// SPDX-License-Identifier: Apache-2.0

package local

import (
	"strings"
	"testing"
)

func TestSFTPTarget(t *testing.T) {
	for repo, want := range map[string][3]string{
		"sftp:portenv-storage@203.0.113.7:/storage/boxes/b":      {"portenv-storage", "203.0.113.7", ""},
		"sftp://portenv-storage@127.0.0.1:2222//storage/boxes/b": {"portenv-storage", "127.0.0.1", "2222"},
		"sftp:host:/x": {"", "host", ""},
	} {
		u, h, p := SFTPTarget(repo)
		if [3]string{u, h, p} != want {
			t.Errorf("SFTPTarget(%q) = %q %q %q, want %v", repo, u, h, p, want)
		}
	}
}

// The REST server must only ever be reached on the server's loopback.
func TestStorageRESTIsLoopbackOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:7422": true, "0.0.0.0:7422": false, "203.0.113.7:7422": false,
		"localhost:7422": false, "127.0.0.1": false, "[::]:7422": false,
	} {
		if LoopbackAddr(addr) != ok {
			t.Errorf("LoopbackAddr(%q) = %v", addr, !ok)
		}
	}
}

// The forward's local socket lives in the private control directory and
// fits the 104-byte limit for unix socket paths; the REST password is in no
// repository address (so never in argv).
func TestRESTForwardSocketAndRepo(t *testing.T) {
	s := &Session{
		Cfg:          BoxConfig{ID: "box-0123456789ab", Storage: "sftp:portenv-storage@203.0.113.7:/storage", StorageREST: "127.0.0.1:7422"},
		Repo:         "sftp:portenv-storage@203.0.113.7:/storage/boxes/box-0123456789ab",
		RESTPassword: []byte("secret-rest-password"),
	}
	f := s.RESTForward()
	if f == nil || f.Via != "portenv-storage@203.0.113.7" || f.User != "box-0123456789ab" {
		t.Fatalf("forward %+v", f)
	}
	sock := f.Socket(ControlDir())
	if !strings.HasPrefix(sock, ControlDir()+"/") || len(sock) >= 104 {
		t.Fatalf("socket %q", sock)
	}
	repo := s.HostRepo()
	if repo != "rest:http+unix://"+sock+":/box-0123456789ab/" || strings.Contains(repo, "secret") {
		t.Fatalf("repo %q", repo)
	}
}
