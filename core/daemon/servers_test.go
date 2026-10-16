// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// testHostKey is a fresh Ed25519 host key, "type base64".
func testHostKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// TestListServersSaysWhereEachKeyStands: by ID and name, never the
// address; the fingerprint of the pinned key.
func TestListServersSaysWhereEachKeyStands(t *testing.T) {
	s := placementServer(t, "me@one", "me@two")
	if err := s.env.ForgetServerKey(local.ServerID("me@two")); err != nil {
		t.Fatal(err)
	}
	r, err := s.ListServers(context.Background(), &daemonv1.ListServersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	infos := r.GetInfos()
	if len(infos) != 2 || len(r.GetServers()) != 2 {
		t.Fatalf("got %v", r)
	}
	one, two := infos[0], infos[1]
	if one.GetId() != local.ServerID("me@one") || one.GetName() != "one" || one.GetState() != typesv1.ServerKeyState_SERVER_KEY_STATE_CONFIRMED || !strings.HasPrefix(one.GetFingerprint(), "SHA256:") {
		t.Fatalf("one: %v", one)
	}
	if two.GetState() != typesv1.ServerKeyState_SERVER_KEY_STATE_UNCONFIRMED || two.GetFingerprint() != "" {
		t.Fatalf("two: %v", two)
	}
	for _, i := range infos {
		if strings.Contains(i.String(), "me@") {
			t.Fatalf("an address in %v", i)
		}
	}
}

// TestMoveToAnUnconfirmedServerStopsFirst: refused before anything closes
// or connects, with the words the workspace shows.
func TestMoveToAnUnconfirmedServerStopsFirst(t *testing.T) {
	log := filepath.Join(t.TempDir(), "ssh.log")
	fake := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" >>"+log+"\nexit 255\n"), 0o700); err != nil { // #nosec G306 -- test helper
		t.Fatal(err)
	}
	t.Setenv("PORTENV_SSH", fake)
	s := placementServer(t, "me@test-server")
	if err := s.env.ForgetServerKey(local.ServerID("me@test-server")); err != nil {
		t.Fatal(err)
	}
	_, err := s.MoveBox(context.Background(), &daemonv1.MoveBoxRequest{Name: "acme-api", Target: "me@test-server"})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "Confirm test-server to open this." {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		b, _ := os.ReadFile(log)
		t.Fatalf("ssh ran: %s", b)
	}
}

// TestTrustNeedsTheFingerprintShown: a fingerprint that isn't the last
// scan's is refused; Forget gives the server back unconfirmed.
func TestTrustNeedsTheFingerprintShown(t *testing.T) {
	s := placementServer(t, "me@test-server")
	ctx := context.Background()
	id := local.ServerID("me@test-server")
	if _, err := s.ForgetServerKey(ctx, &daemonv1.ForgetServerKeyRequest{ServerId: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrustServerKey(ctx, &daemonv1.TrustServerKeyRequest{ServerId: id, Fingerprint: "SHA256:nope"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v", err)
	}
	if _, err := s.TrustServerKey(ctx, &daemonv1.TrustServerKeyRequest{ServerId: "srv-000000000000", Fingerprint: "SHA256:nope"}); status.Code(err) != codes.NotFound {
		t.Fatalf("an unknown server: %v", err)
	}
}

// TestAForgottenChangedKeySaysSo: ListServers carries was_changed, for the
// alert's opening line (R-0185).
func TestAForgottenChangedKeySaysSo(t *testing.T) {
	s := placementServer(t, "me@test-server")
	if err := s.env.MarkServerKeyChanged("me@test-server"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetServerKey(context.Background(), &daemonv1.ForgetServerKeyRequest{ServerId: local.ServerID("me@test-server")}); err != nil {
		t.Fatal(err)
	}
	r, err := s.ListServers(context.Background(), &daemonv1.ListServersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if i := r.GetInfos()[0]; i.GetState() != typesv1.ServerKeyState_SERVER_KEY_STATE_UNCONFIRMED || !i.GetWasChanged() {
		t.Fatalf("got %v", i)
	}
}

// TestAMadeKeyIsHeldNotWritten: ProvideServerKey keeps the key in memory:
// nothing in the Portenv folder or the log carries it; ListServers says
// which servers sign in with a made key.
func TestAMadeKeyIsHeldNotWritten(t *testing.T) {
	var logged strings.Builder
	s := placementServer(t, "me@test-server")
	s.log = slog.New(slog.NewTextHandler(&logged, nil))
	id := local.ServerID("me@test-server")
	if err := s.env.UseMadeKey(id); err != nil {
		t.Fatal(err)
	}
	seed := []byte("0123456789abcdef0123456789abcdef")
	if _, err := s.ProvideServerKey(context.Background(), &daemonv1.ProvideServerKeyRequest{ServerId: id, Key: seed}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProvideServerKey(context.Background(), &daemonv1.ProvideServerKeyRequest{ServerId: "srv-000000000000", Key: seed}); status.Code(err) != codes.NotFound {
		t.Fatalf("an unknown server: %v", err)
	}
	r, _ := s.ListServers(context.Background(), &daemonv1.ListServersRequest{})
	if !r.GetInfos()[0].GetMadeKey() {
		t.Fatalf("got %v", r.GetInfos()[0])
	}
	_ = filepath.Walk(s.env.Dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, seed) {
				t.Errorf("%s holds the key", p)
			}
		}
		return nil
	})
	if strings.Contains(logged.String(), string(seed)) {
		t.Fatal("the key was logged")
	}
}

// TestCheckServerIsRefusedBeforeConfirming: Check runs only on a confirmed
// server (ADR 0021); an unknown one is NotFound.
func TestCheckServerIsRefusedBeforeConfirming(t *testing.T) {
	s := placementServer(t, "me@test-server")
	id := local.ServerID("me@test-server")
	if err := s.env.ForgetServerKey(id); err != nil {
		t.Fatal(err)
	}
	_, err := s.CheckServer(context.Background(), &daemonv1.CheckServerRequest{ServerId: id})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "Confirm test-server to open this." {
		t.Fatalf("unconfirmed: %v", err)
	}
	if _, err := s.CheckServer(context.Background(), &daemonv1.CheckServerRequest{ServerId: "srv-000000000000"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown: %v", err)
	}
	// local's states map to the proto's.
	if checkState(local.CheckPassed) != typesv1.CheckState_CHECK_STATE_PASSED || checkState(local.CheckFailed) != typesv1.CheckState_CHECK_STATE_FAILED ||
		checkState(local.CheckInfo) != typesv1.CheckState_CHECK_STATE_INFO {
		t.Fatal("the states' numbers differ")
	}
}
