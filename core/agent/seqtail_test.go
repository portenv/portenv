// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestSeqTailKeepsAnUnfinishedSequence(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain", ""},
		{"a\x1b", "\x1b"},
		{"a\x1b[3", "\x1b[3"},
		{"a\x1b[31m", ""},
		{"a\x1b[38;2;1", "\x1b[38;2;1"},
		{"\x1b]133;C", "\x1b]133;C"},
		{"\x1b]133;C\x1b", "\x1b]133;C\x1b"},
		{"\x1b]133;C\x1b\\", ""},
		{"\x1b]0;t\x07", ""},
		{"\x1b]0;t\x1b[1", "\x1b[1"}, // an ESC in a string starts a sequence
		{"\x1b(", "\x1b("},
		{"\x1b(B", ""},
		{"\x1b[1\x18", ""}, // cancelled
		{"é"[:1], "é"[:1]},
		{"é", ""},
		{"€"[:2], "€"[:2]},
		{"😀"[:3], "😀"[:3]},
		{"😀", ""},
		{"\xe2\x82x", ""}, // broken, then text
		{"\xe2\x82\x1b[", "\x1b["},
		{"\x1bP" + strings.Repeat("x", maxTail+10), ""}, // too long: dropped
		{"\x1bP" + strings.Repeat("x", maxTail+10) + "\x1b\\a\x1b[", "\x1b["},
	} {
		var s seqTail
		s.feed([]byte(c.in))
		if got := s.pending(); !bytes.Equal(got, []byte(c.want)) {
			t.Errorf("%q: pending %q, want %q", c.in, got, c.want)
		}
		// Byte by byte, the same.
		var s2 seqTail
		for i := range len(c.in) {
			s2.feed([]byte{c.in[i]})
		}
		if got := s2.pending(); !bytes.Equal(got, []byte(c.want)) {
			t.Errorf("%q byte by byte: pending %q, want %q", c.in, got, c.want)
		}
	}
}
