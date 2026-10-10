// SPDX-License-Identifier: Apache-2.0

package agent

import "testing"

// OSC 133 (shell integration) marks a tab's shell: A prompt start, B prompt
// end (the person types), C command start (output follows), D;<exit> command
// done. The tracker follows them across split writes and both terminators.
func TestOSC133TracksPromptCommandAndExit(t *testing.T) {
	var s osc133
	if s.State() != shellUnknown {
		t.Fatalf("new tracker: %v", s.State())
	}
	s.Feed([]byte("hello\x1b]133;A\x07work@box:~$ \x1b]133;B\x07"))
	if s.State() != shellPrompt {
		t.Fatalf("after A,B: %v", s.State())
	}
	s.Feed([]byte("ls\r\n\x1b]133;C\x07a b c\r\n"))
	if s.State() != shellRunning {
		t.Fatalf("after C: %v", s.State())
	}
	// D split across two writes, with the ST terminator.
	s.Feed([]byte("\x1b]133;D;"))
	s.Feed([]byte("2\x1b\\"))
	if s.State() != shellDone {
		t.Fatalf("after D: %v", s.State())
	}
	if code, ok := s.LastExit(); !ok || code != 2 {
		t.Fatalf("exit: %d %v", code, ok)
	}
	s.Feed([]byte("\x1b]133;A\x07$ \x1b]133;B\x07"))
	if s.State() != shellPrompt {
		t.Fatalf("next prompt: %v", s.State())
	}
	if s.Commands() != 1 {
		t.Fatalf("commands: %d", s.Commands())
	}
}

// Other OSC sequences (titles, hyperlinks) and stray bytes never move the
// state, and a D without an exit code is a finished command with no code.
func TestOSC133IgnoresOtherSequences(t *testing.T) {
	var s osc133
	s.Feed([]byte("\x1b]0;title\x07\x1b]8;;https://x\x1b\\link\x1b]8;;\x1b\\\x1b]1337;foo\x07"))
	if s.State() != shellUnknown {
		t.Fatalf("other OSCs moved the state: %v", s.State())
	}
	s.Feed([]byte("\x1b]133;C\x07\x1b]133;D\x07"))
	if s.State() != shellDone {
		t.Fatalf("D without code: %v", s.State())
	}
	if _, ok := s.LastExit(); ok {
		t.Fatal("D without a code reported one")
	}
	// An unterminated, overlong OSC is dropped, not buffered forever.
	big := make([]byte, 0, 10000)
	big = append(big, "\x1b]133;"...)
	for range 9000 {
		big = append(big, 'x')
	}
	s.Feed(big)
	if len(s.buf) > maxOSC {
		t.Fatalf("an unterminated OSC grew the buffer to %d bytes (limit %d)", len(s.buf), maxOSC)
	}
	s.Feed([]byte("\x1b]133;A\x07"))
	if s.State() != shellPrompt {
		t.Fatalf("after an overlong OSC: %v", s.State())
	}
}
