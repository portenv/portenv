// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// keepaliveMinTime is the shortest gap between a client's keepalive pings
// that the daemon accepts (R-0294). The app pings every 4 s with nothing
// read, so a frozen portenvd turns into "Reconnecting…" instead of a
// terminal that silently stops (DaemonClient.Liveness). gRPC's default, 5
// minutes with no pings outside a call, would answer the app's pings with
// GOAWAY (too_many_pings) and close the connection every few seconds.
const keepaliveMinTime = time.Second

// keepalivePolicy lets clients ping as often as keepaliveMinTime, with or
// without a call open: one PING frame, answered by the HTTP/2 transport.
var keepalivePolicy = keepalive.EnforcementPolicy{MinTime: keepaliveMinTime, PermitWithoutStream: true}

// grpcServer is the daemon's gRPC server. A panic in any request is logged
// with its stack and answered with an internal error: one bad request never
// stops portenvd (or the runner) or the boxes it holds open.
//
// Each call's peer carries its caller (callerFrom), attached by ownerOnly
// when the connection was accepted. Clients may send keepalive pings
// (keepalivePolicy).
func (s *Server) grpcServer(opts ...grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(append([]grpc.ServerOption{
		grpc.Creds(callerCreds{}),
		grpc.KeepaliveEnforcementPolicy(keepalivePolicy),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
			defer s.recoverRequest(info.FullMethod, &err)
			return handler(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
			defer s.recoverRequest(info.FullMethod, &err)
			return handler(srv, ss)
		}),
	}, append(ownerOnlyInterceptors(), opts...)...)...)
}

func (s *Server) recoverRequest(method string, err *error) {
	if r := recover(); r != nil {
		if s.log != nil {
			s.log.Error("request panicked; the daemon keeps running", "method", method, "panic", r, "stack", string(debug.Stack()))
		}
		*err = status.Errorf(codes.Internal, "internal error in %s (logged)", method)
	}
}
