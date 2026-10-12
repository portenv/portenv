// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// The ADR's vector key and its password for the test box (from
// core/keys/recovery's vectors; portenvd never derives one itself).
const (
	testBoxID     = "box-0123456789ab"
	vectorGrouped = "K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5"
	vectorDerived = "b4eef5e9da4b5563a65819e2bd99ec3291f9da036e6688e6d6be20cd120d66af"
)

func testRepoKey(boxID string) []byte {
	sum := sha256.Sum256([]byte("repository key for " + boxID))
	return []byte(hex.EncodeToString(sum[:]))
}

// useRestic points portenvd at the pinned restic for CreateBox.
func useRestic(t *testing.T) {
	t.Helper()
	p := os.Getenv("RESTIC")
	if p == "" {
		abs, err := filepath.Abs(filepath.Join("..", "..", "bin", "restic"))
		if err != nil {
			t.Fatal(err)
		}
		p = abs
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("restic not found: run `make tools` or set RESTIC")
	}
	t.Setenv("PORTENV_RESTIC", p)
}

// lockedBuffer collects portenvd's log.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func loggingServer(t *testing.T) (*Server, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	return New(&local.Env{Dir: t.TempDir(), Machine: "mac-1"}, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))), logs
}

func createBoxRequest(name, boxID string) *daemonv1.CreateBoxRequest {
	return &daemonv1.CreateBoxRequest{
		Name: name, BoxId: boxID, Image: "img",
		Keys:             map[string][]byte{boxID: testRepoKey(boxID)},
		RecoveryPassword: []byte(vectorDerived),
	}
}

// secretsIn fails if text holds any of the test's secrets.
func secretsIn(t *testing.T, where, text string) {
	t.Helper()
	for _, s := range []string{vectorGrouped, strings.ReplaceAll(vectorGrouped, "-", ""), vectorDerived, string(testRepoKey(testBoxID))} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(s)) {
			t.Fatalf("a secret is in %s", where)
		}
	}
}

// TestCreateBoxThroughPortenvd: the app's call makes the box, ListBoxes
// shows it as a box, the repository key is held for its first open, and
// the log says what happened without any key.
func TestCreateBoxThroughPortenvd(t *testing.T) {
	useRestic(t)
	s, logs := loggingServer(t)
	ctx := context.Background()
	resp, err := s.CreateBox(ctx, createBoxRequest("acme-api", testBoxID))
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetBoxId() != testBoxID || resp.GetAlreadyCreated() {
		t.Fatalf("response %v", resp)
	}
	again, err := s.CreateBox(ctx, createBoxRequest("acme-api", testBoxID))
	if err != nil || !again.GetAlreadyCreated() {
		t.Fatalf("again: %v, %v", again, err)
	}
	list, err := s.ListBoxes(ctx, &daemonv1.ListBoxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetBoxes()) != 1 || list.GetBoxes()[0].GetBox().GetId() != testBoxID || list.GetBoxes()[0].GetCreating() {
		t.Fatalf("boxes %v", list.GetBoxes())
	}
	if k, err := s.env.Provided.Get(testBoxID); err != nil || !bytes.Equal(k, testRepoKey(testBoxID)) {
		t.Fatal("the repository key isn't held for the first open")
	}
	ids, err := s.GetKeyIDs(ctx, &daemonv1.GetKeyIDsRequest{Name: "acme-api"})
	if err != nil || len(ids.GetKeyIds()) != 1 || ids.GetKeyIds()[0] != testBoxID {
		t.Fatalf("key IDs %v, %v", ids.GetKeyIds(), err)
	}
	if !strings.Contains(logs.String(), "box created") || !strings.Contains(logs.String(), testBoxID) {
		t.Fatalf("the log doesn't say what happened:\n%s", logs.String())
	}
	secretsIn(t, "portenvd's log", logs.String())
}

// TestABoxBeingCreatedIsListedAndDoesntOpen: an unfinished creation shows
// in ListBoxes as creating, can't be opened, and can be discarded.
func TestABoxBeingCreatedIsListedAndDoesntOpen(t *testing.T) {
	useRestic(t)
	s, logs := loggingServer(t)
	ctx := context.Background()
	stop := errors.New("stopped for the test")
	req := local.CreateRequest{Name: "acme-api", BoxID: testBoxID, Image: "img",
		Keys: map[string][]byte{testBoxID: testRepoKey(testBoxID)}, RecoveryPassword: []byte(vectorDerived)}
	if _, err := s.env.CreateBox(ctx, req, local.CreateOptions{Fault: func(step string) error {
		if step == "D4" {
			return stop
		}
		return nil
	}}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	list, err := s.ListBoxes(ctx, &daemonv1.ListBoxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetBoxes()) != 1 || !list.GetBoxes()[0].GetCreating() || list.GetBoxes()[0].GetBox().GetName() != "acme-api" {
		t.Fatalf("boxes %v", list.GetBoxes())
	}
	if _, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: "acme-api"}); err == nil {
		t.Fatal("a box being created opened")
	}
	if _, err := s.CreateBox(ctx, createBoxRequest("acme-api", "box-0123456789ac")); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("its name for another box: %v, want AlreadyExists", err)
	}
	if _, err := s.DiscardCreation(ctx, &daemonv1.DiscardCreationRequest{BoxId: testBoxID}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListBoxes(ctx, &daemonv1.ListBoxesRequest{})
	if len(list.GetBoxes()) != 0 {
		t.Fatalf("after the discard: %v", list.GetBoxes())
	}
	if _, err := s.DiscardCreation(ctx, &daemonv1.DiscardCreationRequest{BoxId: "../../etc"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a bad box ID: %v", err)
	}
	secretsIn(t, "portenvd's log", logs.String())
}

// TestCreateBoxErrorsAreOnePlainLine: each failure has its code, and its
// message never holds a key.
func TestCreateBoxErrorsAreOnePlainLine(t *testing.T) {
	useRestic(t)
	s, logs := loggingServer(t)
	ctx := context.Background()
	if _, err := s.CreateBox(ctx, createBoxRequest("acme-api", testBoxID)); err != nil {
		t.Fatal(err)
	}
	bad := createBoxRequest("acme-web", "box-1")
	bad.Keys = map[string][]byte{"box-1": []byte("k")}
	recoveryKeyItself := createBoxRequest("acme-web", "box-0123456789ac")
	recoveryKeyItself.RecoveryPassword = []byte(vectorGrouped)
	sftp := createBoxRequest("acme-web", "box-0123456789ac")
	sftp.Storage = "sftp:portenv-storage@host:/storage"
	sftp.StorageHostKey = "ssh-ed25519 AAAA"
	sftp.Keys["box-0123456789ac-storage"] = []byte("k")
	otherSettings := createBoxRequest("acme-api", testBoxID)
	otherSettings.Storage = t.TempDir()
	cases := []struct {
		name string
		req  *daemonv1.CreateBoxRequest
		code codes.Code
		msg  string
	}{
		{"name taken", createBoxRequest("acme-api", "box-0123456789ac"), codes.AlreadyExists, "A box named acme-api already exists."},
		{"bad box ID", bad, codes.InvalidArgument, ""},
		{"the recovery key itself", recoveryKeyItself, codes.InvalidArgument, ""},
		{"SFTP storage", sftp, codes.Unimplemented, ""},
		{"same box ID, other settings", otherSettings, codes.FailedPrecondition, ""},
		{"drive not there", func() *daemonv1.CreateBoxRequest {
			r := createBoxRequest("acme-web", "box-0123456789ac")
			r.Storage = "/Volumes/portenv-test-not-mounted"
			return r
		}(), codes.Unavailable, ""},
	}
	for _, c := range cases {
		_, err := s.CreateBox(ctx, c.req)
		if status.Code(err) != c.code {
			t.Fatalf("%s: %v, want %v", c.name, err, c.code)
		}
		msg := status.Convert(err).Message()
		if c.msg != "" && msg != c.msg {
			t.Fatalf("%s: message %q", c.name, msg)
		}
		if strings.Contains(msg, "\n") {
			t.Fatalf("%s: more than one line: %q", c.name, msg)
		}
		secretsIn(t, c.name+"'s error", msg)
	}
	secretsIn(t, "portenvd's log", logs.String())
}

// TestKeyCallsOnlyFromThisUser (ADR 0019, test 8): the calls that carry
// keys have ProvideKeys' protection: a caller running as another user is
// refused before any call.
func TestKeyCallsOnlyFromThisUser(t *testing.T) {
	_, other := servingWith(t, func(s *Server) {
		s.peerUID = func(int) (int, error) { return os.Getuid() + 1, nil }
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calls := map[string]func() error{
		"ProvideKeys": func() error {
			_, err := other.ProvideKeys(ctx, &daemonv1.ProvideKeysRequest{Name: "acme-api"})
			return err
		},
		"CreateBox": func() error {
			_, err := other.CreateBox(ctx, createBoxRequest("acme-api", testBoxID))
			return err
		},
		"RecoverBox": func() error {
			_, err := other.RecoverBox(ctx, &daemonv1.RecoverBoxRequest{Name: "acme-api", BoxId: testBoxID})
			return err
		},
		"DiscardCreation": func() error {
			_, err := other.DiscardCreation(ctx, &daemonv1.DiscardCreationRequest{BoxId: testBoxID})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); status.Code(err) != codes.Unavailable {
			t.Errorf("%s from another user: %v, want the connection refused (Unavailable)", name, err)
		}
	}
}

// TestNoMessageCanCarryTheRecoveryKey (ADR 0019, test 8): the only fields
// about the recovery key carry the derived password (bytes) or a flag.
func TestNoMessageCanCarryTheRecoveryKey(t *testing.T) {
	fd := daemonv1.File_portenv_daemon_v1_daemon_proto
	var walk func(protoreflect.MessageDescriptors)
	seen := 0
	walk = func(msgs protoreflect.MessageDescriptors) {
		for i := range msgs.Len() {
			m := msgs.Get(i)
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				if !strings.Contains(string(f.Name()), "recovery") && !strings.Contains(string(f.Name()), "recover") {
					continue
				}
				switch {
				case f.Name() == "recovery_password" && f.Kind() == protoreflect.BytesKind:
					seen++
				case f.Name() == "already_recovered" && f.Kind() == protoreflect.BoolKind:
				default:
					t.Errorf("%s.%s could carry the recovery key", m.Name(), f.Name())
				}
			}
			walk(m.Messages())
		}
	}
	walk(fd.Messages())
	if seen != 2 {
		t.Fatalf("found %d recovery_password fields, want CreateBox's and RecoverBox's", seen)
	}
}
