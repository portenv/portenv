// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// startChannel serves a channel with fresh secrets on a loopback port.
func startChannel(t *testing.T) (ChannelSecrets, string) {
	t.Helper()
	sec, err := NewChannelSecrets()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, data := range map[string][]byte{TokenFile: []byte(sec.Token), CertFile: sec.CertPEM, KeyFile: sec.KeyPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := newChannelServer(DefaultConfig(), dir)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = serveOn(ctx, srv, lis) }()
	return sec, lis.Addr().String()
}

func dialer(addr string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) { return (&net.Dialer{}).DialContext(ctx, "tcp", addr) }
}

func call(t *testing.T, addr string, cert []byte, token string) error {
	t.Helper()
	conn, err := DialChannel(dialer(addr), cert, token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = agentv1.NewAgentServiceClient(conn).GetPathInfo(context.Background(), &agentv1.GetPathInfoRequest{Path: "/etc"})
	return err
}

func TestChannelNeedsTheTokenAndThePinnedCertificate(t *testing.T) {
	sec, addr := startChannel(t)

	// The right token and certificate reach the API (which then refuses a
	// path outside /home on its own terms).
	if err := call(t, addr, sec.CertPEM, sec.Token); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("with token and pinned certificate: %v, want InvalidArgument from the API", err)
	}
	if err := call(t, addr, sec.CertPEM, ""); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("without token: %v, want Unauthenticated", err)
	}
	// One character changed, never to itself (the token is hex).
	last := "0"
	if strings.HasSuffix(sec.Token, "0") {
		last = "1"
	}
	if err := call(t, addr, sec.CertPEM, sec.Token[:len(sec.Token)-1]+last); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong token: %v, want Unauthenticated", err)
	}
}

// TestChannelRefusesAnImpostor: a server with another certificate on the
// port (for example after the box stopped) never receives the token.
func TestChannelRefusesAnImpostor(t *testing.T) {
	_, impostor := startChannel(t)
	mine, _ := NewChannelSecrets()
	err := call(t, impostor, mine.CertPEM, mine.Token)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("against an impostor: %v, want the TLS handshake to fail (Unavailable)", err)
	}
}

// TestChannelFilesAreGoneOnceLoaded: the API process removes the key,
// certificate, token and their directory as soon as it has read them.
func TestChannelFilesAreGoneOnceLoaded(t *testing.T) {
	sec, _ := NewChannelSecrets()
	dir := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{TokenFile: []byte(sec.Token), CertFile: sec.CertPEM, KeyFile: sec.KeyPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newChannelServer(DefaultConfig(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("channel directory still there after loading (%v)", err)
	}
	if err := WaitChannelLoaded(dir, time.Second); err != nil {
		t.Fatalf("init's wait: %v", err)
	}
}

// TestInitRemovesChannelFilesTheAPIDidNotLoad: if the API process never
// loads them, init removes them before starting anything else.
func TestInitRemovesChannelFilesTheAPIDidNotLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TokenFile), []byte("secret-token-secret-token-secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WaitChannelLoaded(dir, 100*time.Millisecond); err == nil {
		t.Fatal("no error when the channel never loaded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("init left the channel files in place")
	}
}
