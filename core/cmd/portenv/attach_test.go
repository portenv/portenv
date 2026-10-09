// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestALostDaemonIsSaidPlainly: when portenvd goes away (crash, update,
// relaunch), attach never prints gRPC's text ("error reading from server:
// EOF"). In the app's terminal it ends without a word (the window's state
// line says what's happening); at a person's terminal it says one plain line.
func TestALostDaemonIsSaidPlainly(t *testing.T) {
	lost := status.Error(codes.Unavailable, "error reading from server: EOF")
	if err := attachEnd(lost, true); !errors.As(err, new(quietError)) {
		t.Fatalf("from the app: %v, want a quiet error", err)
	}
	if err := attachEnd(lost, false); err == nil || err.Error() != "lost the connection to portenvd" {
		t.Fatalf("at a terminal: %v", err)
	}
	if err := attachEnd(io.EOF, true); err != nil {
		t.Fatalf("the session ended: %v, want nil", err)
	}
	other := status.Error(codes.NotFound, "box acme-api is not open")
	if err := attachEnd(other, true); err == nil || err.Error() != "box acme-api is not open" {
		t.Fatalf("another error: %v", err)
	}
}
