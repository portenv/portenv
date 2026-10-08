// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestASecondCloseFindsTheBoxClosed: closing the last window closes the box,
// and the app quitting closes it again. The second close waited for the
// first and must succeed without touching the closed box (it once
// dereferenced its cleared agent connection and crashed portenvd).
func TestASecondCloseFindsTheBoxClosed(t *testing.T) {
	s := New(nil, nil)
	ob := &openBox{closed: true} // the first close finished while this one waited
	s.open["b"] = ob
	if err := s.closeBox(context.Background(), "b"); err != nil {
		t.Fatalf("second close: %v, want success", err)
	}
}

// panicky is the daemon with one handler that panics.
type panicky struct{ *Server }

func (panicky) GetVersion(context.Context, *daemonv1.GetVersionRequest) (*daemonv1.GetVersionResponse, error) {
	panic("boom: a bug in a handler")
}

func (panicky) ListBoxes(context.Context, *daemonv1.ListBoxesRequest) (*daemonv1.ListBoxesResponse, error) {
	return &daemonv1.ListBoxesResponse{}, nil
}

// TestAPanickingHandlerDoesNotStopTheDaemon: a panic in one request is
// logged with its stack and answered with an internal error; the daemon
// keeps serving, and the boxes it holds open stay open.
func TestAPanickingHandlerDoesNotStopTheDaemon(t *testing.T) {
	s := New(nil, nil)
	other := &openBox{}
	s.open["other"] = other
	srv := s.grpcServer()
	daemonv1.RegisterDaemonServiceServer(srv, panicky{s})
	dir, err := os.MkdirTemp("/tmp", "pd") // short: a socket path is at most 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	c := daemonv1.NewDaemonServiceClient(conn)

	for range 3 {
		if _, err := c.GetVersion(context.Background(), &daemonv1.GetVersionRequest{}); status.Code(err) != codes.Internal {
			t.Fatalf("panicking call: %v, want Internal", err)
		}
	}
	if _, err := c.ListBoxes(context.Background(), &daemonv1.ListBoxesRequest{}); err != nil {
		t.Fatalf("the next call after a panic: %v", err)
	}
	if s.open["other"] != other {
		t.Fatal("another box was affected by the panic")
	}
}
