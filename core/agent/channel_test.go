// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"net"
	"strings"
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
	srv, err := newChannelServer(DefaultConfig(), sec)
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
	if err := call(t, addr, sec.CertPEM, sec.Token[:len(sec.Token)-1]+"0"); status.Code(err) != codes.Unauthenticated {
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

// TestChannelSecretsComeOnStdin: the driver writes one JSON line on the
// box's stdin; the API process reads exactly that, and nothing else.
func TestChannelSecretsComeOnStdin(t *testing.T) {
	sec, _ := NewChannelSecrets()
	line, err := sec.Line()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadChannelSecrets(bytes.NewReader(append(line, []byte("trailing input is ignored\n")...)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != sec.Token || !bytes.Equal(got.CertPEM, sec.CertPEM) || !bytes.Equal(got.KeyPEM, sec.KeyPEM) {
		t.Fatal("secrets changed on the way")
	}
	if _, err := newChannelServer(DefaultConfig(), got); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "not json\n", `{"Token":"short"}` + "\n"} {
		s, err := ReadChannelSecrets(strings.NewReader(bad))
		if err == nil {
			_, err = newChannelServer(DefaultConfig(), s)
		}
		if err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// TestInitWaitsForTheChannel: init starts nothing else until the API
// process listens, which it does only after reading the secrets.
func TestInitWaitsForTheChannel(t *testing.T) {
	if err := WaitChannelListening(1, 100*time.Millisecond); err == nil {
		t.Fatal("no error with nothing listening")
	}
}
