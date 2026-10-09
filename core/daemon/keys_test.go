// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestKeysHandedOverByTheApp: the app reads a box's keys from the Keychain
// and hands them to portenvd, which uses them first and keeps them in
// memory; a key that isn't one of the box's own is refused.
func TestKeysHandedOverByTheApp(t *testing.T) {
	t.Setenv("PORTENV_KEYS", "file")
	s := quietServer(t)
	c := local.BoxConfig{ID: "box-1", Name: "acme-api", Image: "img", Storage: "sftp:portenv-storage@host:/storage", StorageREST: "127.0.0.1:7422"}
	if err := os.MkdirAll(filepath.Join(s.env.Dir, "boxes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.env.SaveBox(c); err != nil {
		t.Fatal(err)
	}
	if got := local.KeyIDs(c); fmt.Sprint(got) != "[box-1 box-1-storage box-1-rest]" {
		t.Fatalf("key IDs %v", got)
	}
	_, err := s.ProvideKeys(context.Background(), &daemonv1.ProvideKeysRequest{Name: "acme-api", Keys: map[string][]byte{"box-1": []byte("repo-key"), "box-1-rest": []byte("pw")}})
	if err != nil {
		t.Fatal(err)
	}
	if k, err := s.env.KeyStore().Get("box-1"); err != nil || string(k) != "repo-key" {
		t.Fatalf("the handed-over key: %q, %v", k, err)
	}
	_, err = s.ProvideKeys(context.Background(), &daemonv1.ProvideKeysRequest{Name: "acme-api", Keys: map[string][]byte{"box-other": []byte("x")}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("another box's key: %v, want InvalidArgument", err)
	}
}

// TestNeedsApprovalIsOnePlainLine: when a key needs the person's approval,
// portenvd says so in one line the app can act on, as FailedPrecondition.
func TestNeedsApprovalIsOnePlainLine(t *testing.T) {
	err := keyError(fmt.Errorf("load keys: %w", keys.ErrNeedsApproval))
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code %v, want FailedPrecondition", status.Code(err))
	}
	if msg := status.Convert(err).Message(); msg != "Keychain needs your approval. Open Portenv on this Mac to allow it." {
		t.Fatalf("message %q", msg)
	}
}

// TestTheAppAsksForKeyIDsAndServers: the app reads both from portenvd
// (1.1), not from the CLI.
func TestTheAppAsksForKeyIDsAndServers(t *testing.T) {
	s := quietServer(t)
	c := local.BoxConfig{ID: "box-1", Name: "acme-api", Image: "img", Storage: "sftp:portenv-storage@host:/storage", StorageREST: "127.0.0.1:7422"}
	if err := os.MkdirAll(filepath.Join(s.env.Dir, "boxes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.env.SaveBox(c); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetKeyIDs(context.Background(), &daemonv1.GetKeyIDsRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(r.GetKeyIds()) != "[box-1 box-1-storage box-1-rest]" {
		t.Fatalf("key IDs %v", r.GetKeyIds())
	}
	if _, err := s.GetKeyIDs(context.Background(), &daemonv1.GetKeyIDsRequest{Name: "nope"}); err == nil {
		t.Fatal("key IDs for a box that doesn't exist")
	}
	srv, err := s.ListServers(context.Background(), &daemonv1.ListServersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.GetServers()) != 0 {
		t.Fatalf("servers on a new machine: %v", srv.GetServers())
	}
}
