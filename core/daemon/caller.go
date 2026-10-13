// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"net"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// caller is who is on the other end of a connection to portenvd. It is
// fixed when the connection is accepted, from the socket alone, and never
// read from a request: a request can claim anything (ADR 0020 decision 4).
//
// Its label (callerLabel) is portenvd's internal stream identity, the
// author it hands the workspace helper. The app is shown CommandAuthor,
// never these labels.
type caller struct {
	kind callerKind
	// id names an agent (ADR 0016's identity). Empty for the app.
	id string
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
var ownerCaller = caller{kind: callerApp}

// callerLabel is the author portenvd records for the caller's input.
func (c caller) callerLabel() string {
	if c.kind == callerAgent {
		return "agent:" + c.id
	}
	return "app"
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
