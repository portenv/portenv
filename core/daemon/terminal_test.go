// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"errors"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestTerminalEndIsHonest: a terminal whose box this daemon moved, closed
// or restarted ends quietly; the same error otherwise says the agent is
// unavailable (the honesty rule: no "restart the box" for a planned move).
func TestTerminalEndIsHonest(t *testing.T) {
	down := status.Error(codes.Unavailable, "connection reset")
	s := New(nil, nil)
	gen := s.boxGen("b")
	if err := s.terminalEnd("b", gen, down); status.Code(err) != codes.Unavailable {
		t.Fatalf("channel down, box not moved: %v, want Unavailable", err)
	}
	s.leaving("b") // Move To, Close or Restart Box
	if err := s.terminalEnd("b", gen, down); err != nil {
		t.Fatalf("box moved after the terminal opened: %v, want a quiet end", err)
	}
	if err := s.terminalEnd("b", s.boxGen("b"), down); status.Code(err) != codes.Unavailable {
		t.Fatalf("a terminal opened after the move, then the channel fails: %v, want Unavailable", err)
	}
	if err := s.terminalEnd("other", s.boxGen("other"), down); status.Code(err) != codes.Unavailable {
		t.Fatalf("another box moving says nothing about this one: %v", err)
	}
	if err := s.terminalEnd("b", s.boxGen("b"), io.EOF); err != nil {
		t.Fatalf("the shell ended: %v, want nil", err)
	}
	other := errors.New("boom")
	if err := s.terminalEnd("b", s.boxGen("b"), other); !errors.Is(err, other) {
		t.Fatalf("other errors pass through: %v", err)
	}
}
