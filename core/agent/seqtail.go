// SPDX-License-Identifier: Apache-2.0

package agent

// seqTail follows where a pane's output stands between sequences: when it
// ends inside an escape sequence or a UTF-8 character, a seed taken there
// ends with that sequence's start, so the output after the seed completes
// it rather than showing as text (R-0133 item 5).
type seqTail struct {
	buf   []byte
	state tailState
	need  int  // UTF-8 continuation bytes still to come
	lost  bool // the sequence outgrew maxTail: not replayed
}

type tailState byte

const (
	tailGround    tailState = iota
	tailEsc                 // after ESC
	tailEscInter            // ESC and intermediates
	tailCSI                 // ESC [
	tailString              // OSC, DCS, APC, PM, SOS: until BEL or ST
	tailStringEsc           // ESC inside a string: ST, or a new sequence
	tailUTF8                // a character's lead byte and some continuations
)

// maxTail is the longest unfinished sequence a seed carries; past it (a
// huge OSC) the sequence is dropped, as tmux would show nothing of it.
const maxTail = 4096

func (s *seqTail) reset() { *s = seqTail{buf: s.buf[:0]} }

func (s *seqTail) feed(b []byte) {
	for _, c := range b {
		s.byte(c)
	}
}

func (s *seqTail) begin(state tailState, c byte) {
	s.state, s.lost = state, false
	s.buf = append(s.buf[:0], c)
}

func (s *seqTail) add(c byte) {
	if s.lost {
		return
	}
	if len(s.buf) >= maxTail {
		s.lost, s.buf = true, s.buf[:0]
		return
	}
	s.buf = append(s.buf, c)
}

func (s *seqTail) done() { s.state, s.lost, s.buf = tailGround, false, s.buf[:0] }

func (s *seqTail) byte(c byte) {
	switch s.state {
	case tailGround:
	case tailUTF8:
		if c >= 0x80 && c <= 0xbf {
			s.add(c)
			if s.need--; s.need == 0 {
				s.done()
			}
			return
		}
		s.done() // a broken character: start again from this byte
	case tailEsc:
		s.add(c)
		switch {
		case c == '[':
			s.state = tailCSI
		case c == ']' || c == 'P' || c == '_' || c == '^' || c == 'X':
			s.state = tailString
		case c >= 0x20 && c <= 0x2f:
			s.state = tailEscInter
		case c == 0x1b:
			s.begin(tailEsc, c)
		default:
			s.done()
		}
		return
	case tailEscInter:
		s.add(c)
		if c < 0x20 || c > 0x2f {
			s.done()
		}
		return
	case tailCSI:
		switch {
		case c == 0x1b:
			s.begin(tailEsc, c)
		case c == 0x18 || c == 0x1a:
			s.done()
		case c >= 0x40 && c <= 0x7e:
			s.done()
		default:
			s.add(c)
		}
		return
	case tailString:
		switch c {
		case 0x07, 0x18, 0x1a:
			s.done()
		case 0x1b:
			s.add(c)
			s.state = tailStringEsc
		default:
			s.add(c)
		}
		return
	case tailStringEsc:
		if c == '\\' {
			s.done()
			return
		}
		// The ESC ended the string and starts a sequence of its own.
		s.begin(tailEsc, 0x1b)
		s.byte(c)
		return
	}
	switch {
	case c == 0x1b:
		s.begin(tailEsc, c)
	case c >= 0xc2 && c <= 0xdf:
		s.begin(tailUTF8, c)
		s.need = 1
	case c >= 0xe0 && c <= 0xef:
		s.begin(tailUTF8, c)
		s.need = 2
	case c >= 0xf0 && c <= 0xf4:
		s.begin(tailUTF8, c)
		s.need = 3
	}
}

// pending is the unfinished sequence's bytes so far, if any and not lost.
func (s *seqTail) pending() []byte {
	if s.state == tailGround || s.lost {
		return nil
	}
	return append([]byte(nil), s.buf...)
}
