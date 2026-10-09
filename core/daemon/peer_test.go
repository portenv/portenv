// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestOnlyThisUserReachesPortenvd: every connection's peer uid is checked
// (LOCAL_PEERCRED on a Mac, SO_PEERCRED on Linux), and a caller running as
// anyone else is refused before any call. The socket is 0600 in a 0700
// directory besides.
func TestOnlyThisUserReachesPortenvd(t *testing.T) {
	_, c := serving(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.GetVersion(ctx, &daemonv1.GetVersionRequest{}); err != nil {
		t.Fatalf("this user: %v", err)
	}

	// Another user: the check sees a different uid.
	_, other := servingWith(t, func(s *Server) {
		s.peerUID = func(int) (int, error) { return os.Getuid() + 1, nil }
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if _, err := other.GetVersion(ctx2, &daemonv1.GetVersionRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("another user: %v, want the connection refused (Unavailable)", err)
	}
}

// TestThePortenvDirectoryIsPrivate: portenvd refuses a directory owned by
// someone else and makes its own 0700 if others could read it.
func TestThePortenvDirectoryIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o, want 0700", fi.Mode().Perm())
	}
	if err := privateDir("/"); err == nil && os.Getuid() != 0 {
		t.Fatal("served from a directory owned by root")
	}
}
