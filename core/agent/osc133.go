// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strconv"
	"strings"
)

// shellState is what a tab's shell is doing, from its OSC 133 marks (shell
// integration; FinalTerm's semantic prompts): at its prompt, running a
// command, or done with one.
type shellState int

const (
	shellUnknown shellState = iota // no mark seen yet
	shellPrompt                    // A or B: the prompt is on screen
	shellRunning                   // C: a command runs
	shellDone                      // D: the command finished
)

func (s shellState) String() string {
	return [...]string{"unknown", "prompt", "running", "done"}[s]
}

// maxOSC bounds one OSC sequence; longer ones (or ones never terminated) are
// dropped rather than buffered.
const maxOSC = 4096

// osc133 follows a stream of terminal output and tracks OSC 133 marks. It
// keeps no output, only the state, so it can sit on every tab's stream.
type osc133 struct {
	mode     int // 0 text, 1 after ESC, 2 in OSC, 3 ESC inside OSC
	buf      []byte
	state    shellState
	exit     int
	hasExit  bool
	commands int
}

func (o *osc133) State() shellState { return o.state }

// LastExit is the last finished command's exit code, when its D mark had one.
func (o *osc133) LastExit() (int, bool) { return o.exit, o.hasExit }

// Commands counts the commands that finished.
func (o *osc133) Commands() int { return o.commands }

func (o *osc133) Feed(p []byte) {
	for _, c := range p {
		switch o.mode {
		case 0:
			if c == 0x1b {
				o.mode = 1
			}
		case 1:
			if c == ']' {
				o.mode, o.buf = 2, o.buf[:0]
			} else if c != 0x1b {
				o.mode = 0
			}
		case 2:
			switch {
			case c == 0x07:
				o.end()
			case c == 0x1b:
				o.mode = 3
			case len(o.buf) >= maxOSC:
				o.mode, o.buf = 0, o.buf[:0] // dropped
			default:
				o.buf = append(o.buf, c)
			}
		case 3:
			if c == '\\' {
				o.end()
			} else {
				// ESC not followed by '\': the OSC was cut short; this ESC
				// starts a new sequence.
				o.mode, o.buf = 1, o.buf[:0]
				if c == ']' {
					o.mode = 2
				} else if c != 0x1b {
					o.mode = 0
				}
			}
		}
	}
}

func (o *osc133) end() {
	s := string(o.buf)
	o.mode, o.buf = 0, o.buf[:0]
	rest, ok := strings.CutPrefix(s, "133;")
	if !ok || rest == "" {
		return
	}
	switch rest[0] {
	case 'A', 'B':
		o.state = shellPrompt
	case 'C':
		o.state = shellRunning
	case 'D':
		o.state = shellDone
		o.commands++
		o.hasExit = false
		if code, found := strings.CutPrefix(rest, "D;"); found {
			if i := strings.IndexByte(code, ';'); i >= 0 {
				code = code[:i]
			}
			if n, err := strconv.Atoi(code); err == nil {
				o.exit, o.hasExit = n, true
			}
		}
	}
}
