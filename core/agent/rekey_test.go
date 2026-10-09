// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// syncBuffer is a log the agent's goroutines write while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startRekeyable serves a channel whose stdin the test writes to.
func startRekeyable(t *testing.T, logTo io.Writer) (ChannelSecrets, string, *io.PipeWriter) {
	t.Helper()
	first, err := NewChannelSecrets()
	if err != nil {
		t.Fatal(err)
	}
	line, _ := first.Line()
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(logTo, nil))
	go func() {
		_ = serveStdinChannel(ctx, DefaultConfig(), r, func() (net.Listener, error) { return lis, nil }, log)
	}()
	if _, err := w.Write(line); err != nil {
		t.Fatal(err)
	}
	return first, lis.Addr().String(), w
}

// works reports whether a call with these secrets reaches the API (which
// refuses the path outside /home on its own terms: InvalidArgument).
func works(t *testing.T, addr string, s ChannelSecrets) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := DialChannel(dialer(addr), s.CertPEM, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = agentv1.NewAgentServiceClient(conn).GetPathInfo(ctx, &agentv1.GetPathInfoRequest{Path: "/etc"})
	return status.Code(err) == codes.InvalidArgument
}

func eventually(t *testing.T, cond func() bool) bool {
	t.Helper()
	for range 100 {
		if cond() {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

// TestARekeySwapsTheSecrets: a new line on stdin replaces the token and
// the certificate together; the old ones stop working at once and a
// connection made with them is closed (ADR 0014).
func TestARekeySwapsTheSecrets(t *testing.T) {
	var logs syncBuffer
	first, addr, stdin := startRekeyable(t, &logs)
	if !eventually(t, func() bool { return works(t, addr, first) }) {
		t.Fatal("the first secrets never worked")
	}
	// An open connection, made with the first secrets.
	old, err := DialChannel(dialer(addr), first.CertPEM, first.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if _, err := agentv1.NewAgentServiceClient(old).GetVersion(context.Background(), &agentv1.GetVersionRequest{}); err != nil {
		t.Fatal(err)
	}

	second, _ := NewChannelSecrets()
	line, _ := second.Line()
	if _, err := stdin.Write(line); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool { return works(t, addr, second) }) {
		t.Fatal("the new secrets never worked")
	}
	if works(t, addr, first) {
		t.Fatal("the old secrets still work after a re-key")
	}
	// Mixed: the new certificate with the old token, and the reverse.
	if works(t, addr, ChannelSecrets{CertPEM: second.CertPEM, Token: first.Token}) || works(t, addr, ChannelSecrets{CertPEM: first.CertPEM, Token: second.Token}) {
		t.Fatal("half of the old secrets still work")
	}
	// The connection made before the re-key is closed, not just refused
	// call by call: reconnecting needs the old certificate, which is gone.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := agentv1.NewAgentServiceClient(old).GetVersion(ctx, &agentv1.GetVersionRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("a connection made with the old secrets: %v, want it closed (Unavailable)", err)
	}
	for _, secret := range []string{first.Token, second.Token, string(second.KeyPEM)} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("the log holds part of a secrets line")
		}
	}
}

// TestABadLineChangesNothing: a malformed, oversized or unknown-field line
// is dropped whole and the old secrets keep working.
func TestABadLineChangesNothing(t *testing.T) {
	var logs syncBuffer
	first, addr, stdin := startRekeyable(t, &logs)
	if !eventually(t, func() bool { return works(t, addr, first) }) {
		t.Fatal("the first secrets never worked")
	}
	other, _ := NewChannelSecrets()
	good, _ := other.Line()
	unknown := strings.Replace(string(good), `{"Token"`, `{"Extra":1,"Token"`, 1)
	short := strings.Replace(string(good), other.Token, "abc", 1)
	for _, bad := range []string{
		"not json\n",
		`{"Token":"` + strings.Repeat("a", 64) + `"}` + "\n", // no certificate
		unknown,
		short,
		strings.Repeat("x", maxSecretsLine+10) + string(good), // oversized, a valid line at its end: dropped whole
	} {
		if _, err := stdin.Write([]byte(bad)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if !works(t, addr, first) {
		t.Fatal("a bad line broke the old secrets")
	}
	if works(t, addr, other) {
		t.Fatal("a bad line was applied")
	}
	if strings.Contains(logs.String(), other.Token) || strings.Contains(logs.String(), "not json") {
		t.Fatal("the log echoes a line")
	}
}
