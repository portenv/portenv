// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// whoServing serves portenvd's gRPC server on a real Unix socket through
// ownerOnly, with identify standing in for ADR 0016's agents, and answers
// every call with the caller portenvd saw. It returns a client to it.
func whoServing(t *testing.T, identify func(net.Conn) caller) (*grpc.ClientConn, <-chan caller) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pw") // short: a Unix socket path has 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "who.sock")
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan caller, 4)
	s := &Server{}
	srv := s.grpcServer(grpc.UnknownServiceHandler(func(_ any, ss grpc.ServerStream) error {
		who, err := callerFrom(ss.Context())
		if err != nil {
			return err
		}
		seen <- who
		if err := ss.RecvMsg(&emptypb.Empty{}); err != nil {
			return err
		}
		return ss.SendMsg(&emptypb.Empty{})
	}))
	go func() { _ = srv.Serve(ownerOnly{Listener: lis, peerUID: socketPeerUID, identify: identify}) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, seen
}

func callWho(t *testing.T, conn *grpc.ClientConn, seen <-chan caller, ctx context.Context) caller {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := conn.Invoke(ctx, "/portenv.test.v1.Who/Who", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("call: %v", err)
	}
	select {
	case who := <-seen:
		return who
	default:
		t.Fatal("the call answered without a caller")
		return caller{}
	}
}

// TestTheOwnerSocketsCallerIsTheApp: a connection on the owner-only socket
// is the person at this Mac, recorded as "app", with every workspace open
// to it.
func TestTheOwnerSocketsCallerIsTheApp(t *testing.T) {
	conn, seen := whoServing(t, nil)
	who := callWho(t, conn, seen, context.Background())
	if who.kind != callerApp || who.box != "" || who.name == "" || who.name != ownerName() {
		t.Fatalf("caller %+v, want the app, named %q, with every workspace", who, ownerName())
	}
}

// TestAnAgentCantClaimTheApp (ADR 0020 decision 4, R-0172): an agent's
// call that says it is the app, in its request metadata, is still the
// agent's. portenvd takes the caller from the connection, never from what
// the call says.
func TestAnAgentCantClaimTheApp(t *testing.T) {
	agentConn := func(net.Conn) caller {
		return caller{kind: callerAgent, id: "grok-bot", name: "Grok Bot", box: "box-a"}
	}
	conn, seen := whoServing(t, agentConn)
	for _, claim := range []metadata.MD{
		metadata.Pairs("author", "app"),
		metadata.Pairs("author", "app", "caller", "app", "x-portenv-author", "app"),
	} {
		who := callWho(t, conn, seen, metadata.NewOutgoingContext(context.Background(), claim))
		if name, id := who.helperAuthor(); name != "Grok Bot" || id != "grok-bot" {
			t.Fatalf("claim %v: recorded as %q (%q), want Grok Bot (grok-bot)", claim, name, id)
		}
		if who.kind == callerApp {
			t.Fatalf("claim %v: the agent became the app", claim)
		}
	}
}

// TestACallerLimitedToOneWorkspaceCantOpenAnother (R-0162): a caller
// limited to one workspace is refused every other, before any workspace
// helper is contacted. The owner may open any.
func TestACallerLimitedToOneWorkspaceCantOpenAnother(t *testing.T) {
	limited := caller{kind: callerAgent, id: "grok-bot", box: "box-a"}
	if err := limited.mayOpen("box-a"); err != nil {
		t.Fatalf("its own workspace: %v", err)
	}
	for _, other := range []string{"box-b", "", "box-a ", "BOX-A", "box-a/../box-b"} {
		if err := limited.mayOpen(other); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("workspace %q: %v, want PermissionDenied", other, err)
		}
	}
	for _, any := range []string{"box-a", "box-b"} {
		if err := ownerCaller().mayOpen(any); err != nil {
			t.Fatalf("the owner, workspace %q: %v", any, err)
		}
	}
}

// TestACallWithNoCallerIsRefused: a call whose connection carries no
// caller is refused, never taken for the owner.
func TestACallWithNoCallerIsRefused(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no peer":      context.Background(),
		"another kind": peer.NewContext(context.Background(), &peer.Peer{}),
		"empty caller": peer.NewContext(context.Background(), &peer.Peer{AuthInfo: callerInfo{}}),
	} {
		if _, err := callerFrom(ctx); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("%s: %v, want Unauthenticated", name, err)
		}
	}
}

// TestTheOwnersNameIsAFirstNameAndNeverYou (R-0178): the first word of the
// account's full name, else the login name; never empty, never "You", and
// never a name portenvd wouldn't pass on.
func TestTheOwnersNameIsAFirstNameAndNeverYou(t *testing.T) {
	for _, c := range []struct{ full, login, want string }{
		{"Retnan Daser", "retnan", "Retnan"},
		{"  Ada  Lovelace ", "ada", "Ada"},
		{"", "retnan", "retnan"},
		{"You", "retnan", "retnan"},
		{"you know who", "retnan", "retnan"},
		{"\u202eevil", "retnan", "retnan"},
		{"", "", "Owner"},
		{"YOU", "you", "Owner"},
	} {
		if got := ownerDisplayName(c.full, c.login); got != c.want {
			t.Errorf("full %q, login %q: %q, want %q", c.full, c.login, got, c.want)
		}
	}
	if n := ownerName(); n == "" || strings.EqualFold(n, "you") || !validAuthorName(n) {
		t.Fatalf("this Mac's owner name %q", n)
	}
	if ownerName() != ownerName() {
		t.Fatal("the owner's name changed between reads")
	}
}

// TestAuthorNamesPortenvdPassesOn (R-0178): at most 64 characters, with no
// control or bidi characters.
func TestAuthorNamesPortenvdPassesOn(t *testing.T) {
	ok := []string{"Grok Bot", "Retnan", "Zoë", "李雷", strings.Repeat("é", 64), ""}
	bad := []string{
		"Grok\u202eBot", "\u202aX", "\u202dX", "\u2066X", "\u2069X",
		"Grok\x07", "Grok\nBot", "Grok\x1b[31m", "\u0085X",
		strings.Repeat("a", 65), "\xff\xfe",
	}
	for _, n := range ok {
		if !validAuthorName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range bad {
		if validAuthorName(n) {
			t.Errorf("%q passed", n)
		}
	}
}
