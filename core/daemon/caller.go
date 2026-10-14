// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"net"
	"os"
	"os/user"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// caller is who is on the other end of a connection to portenvd. It is
// fixed when the connection is accepted, from the socket alone, and never
// read from a request: a request can claim anything (ADR 0020 decision 4).
//
// Its name, and its id for an agent, are the author portenvd hands the
// workspace helper for the caller's stream (ADR 0020, R-0178).
type caller struct {
	kind callerKind
	// id names an agent (ADR 0016's identity). Empty for the app.
	id string
	// name is the caller as people read it: the owner's first name, or the
	// agent's grant name ("Grok Bot"). Never "You": is_you makes that.
	name string
	// box limits the caller to one workspace. Empty: every workspace on
	// this machine, which only the owner has.
	box string
}

type callerKind int

const (
	// callerApp is the person at this Mac: anything reaching the owner-only
	// socket (ADR 0014).
	//
	// Assumption, for 2.4 to revisit: the app and the CLI share the owner's
	// uid, so both are callerApp. That is right only while `portenv attach`
	// stays on the Terminal call, which records no author. Once the CLI, or
	// an agent's door (ADR 0016), can call TabTerminal, each needs its own
	// identity here, set from its credential, before it is let in.
	callerApp callerKind = iota + 1
	// callerAgent is an agent reaching portenvd through a grant (ADR 0016).
	// None can yet: it exists so the attribution rules are in place, and
	// tested, before any agent can call TabTerminal.
	callerAgent
)

// ownerCaller is the caller on the owner-only socket.
func ownerCaller() caller { return caller{kind: callerApp, name: ownerName()} }

// ownerName is the owner's name for attribution, read once (Serve reads it
// at start) and then the same for the daemon's life.
var ownerName = sync.OnceValue(readOwnerName)

func readOwnerName() string {
	login := os.Getenv("USER")
	if u, err := user.Current(); err == nil && u.Username != "" {
		login = u.Username
	}
	return ownerDisplayName(accountFullName(), login)
}

// ownerDisplayName is the first word of the account's full name, or the
// login name when that isn't usable, and never empty or "You".
func ownerDisplayName(full, login string) string {
	for _, n := range []string{firstWord(full), login} {
		if n != "" && !strings.EqualFold(n, "you") && validAuthorName(n) {
			return n
		}
	}
	return "Owner"
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// helperAuthor is what the helper's open records for the caller's commands.
func (c caller) helperAuthor() (name, agentID string) {
	if c.kind == callerAgent {
		return c.name, c.id
	}
	return c.name, ""
}

// maxAuthorName bounds an author's name, in characters.
const maxAuthorName = 64

// validAuthorName is a name portenvd passes on: at most 64 characters of
// valid UTF-8, with no control characters and no bidi controls
// (U+202A–202E, U+2066–2069), which could make a name read as another.
// The helper lives in the workspace, so its authors are checked, and a
// bad one is dropped, never repaired (R-0178).
func validAuthorName(n string) bool {
	if !utf8.ValidString(n) || utf8.RuneCountInString(n) > maxAuthorName {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			return false
		}
	}
	return true
}

// mayOpen refuses a workspace the caller isn't limited to. It runs before
// any workspace helper is contacted, so a refused call reaches no one.
func (c caller) mayOpen(box string) error {
	if c.box != "" && c.box != box {
		return status.Error(codes.PermissionDenied, "this caller may reach only its own workspace")
	}
	return nil
}

// callerFrom is the caller of the call in ctx. A call with none (it didn't
// come through Serve's listener) is refused, never treated as the owner.
func callerFrom(ctx context.Context) (caller, error) {
	if p, ok := peer.FromContext(ctx); ok {
		if info, ok := p.AuthInfo.(callerInfo); ok && info.who.kind != 0 {
			return info.who, nil
		}
	}
	return caller{}, status.Error(codes.Unauthenticated, "portenvd couldn't tell who is calling")
}

// identifiedConn is an accepted connection with its caller.
type identifiedConn struct {
	net.Conn
	who caller
}

// callerInfo carries the caller into each call's peer.
type callerInfo struct {
	credentials.CommonAuthInfo
	who caller
}

func (callerInfo) AuthType() string { return "portenv-caller" }

// callerCreds hands gRPC the caller ownerOnly attached to the connection.
// It does no handshake: the socket is the authentication (ADR 0014).
type callerCreds struct{}

func (callerCreds) ServerHandshake(c net.Conn) (net.Conn, credentials.AuthInfo, error) {
	info := callerInfo{CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.PrivacyAndIntegrity}}
	if ic, ok := c.(identifiedConn); ok {
		info.who = ic.who
	}
	return c, info, nil
}

func (callerCreds) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, status.Error(codes.Unimplemented, "server-side only")
}

func (callerCreds) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "portenv-caller"}
}

func (callerCreds) Clone() credentials.TransportCredentials { return callerCreds{} }

func (callerCreds) OverrideServerName(string) error { return nil }
