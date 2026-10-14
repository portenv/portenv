// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// grpcServer is the daemon's gRPC server. A panic in any request is logged
// with its stack and answered with an internal error: one bad request never
// stops portenvd (or the runner) or the boxes it holds open.
//
// Each call's peer carries its caller (callerFrom), attached by ownerOnly
// when the connection was accepted.
func (s *Server) grpcServer(opts ...grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(append([]grpc.ServerOption{
		grpc.Creds(callerCreds{}),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
			defer s.recoverRequest(info.FullMethod, &err)
			return handler(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
			defer s.recoverRequest(info.FullMethod, &err)
			return handler(srv, ss)
		}),
	}, opts...)...)
}

func (s *Server) recoverRequest(method string, err *error) {
	if r := recover(); r != nil {
		if s.log != nil {
			s.log.Error("request panicked; the daemon keeps running", "method", method, "panic", r, "stack", string(debug.Stack()))
		}
		*err = status.Errorf(codes.Internal, "internal error in %s (logged)", method)
	}
}
