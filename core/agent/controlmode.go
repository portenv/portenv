// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// tmux control mode (tmux -C): the server writes one line per notification
// to the client. The agent reads the ones that matter for tabs and their
// output; everything else (command replies between %begin and %end, other
// sessions' events) is skipped. A tab is a tmux window, so window IDs (@N)
// are the tab IDs the tab interface already uses.
type ctlKind int

const (
	ctlOther         ctlKind = iota
	ctlWindowAdd             // %window-add @N
	ctlWindowClose           // %window-close @N, %unlinked-window-close @N
	ctlWindowRenamed         // %window-renamed @N name
	ctlOutput                // %output %P data, %extended-output %P age … : data
	ctlExit                  // %exit: the client is detached
	// ctlTabsChanged: something else that can change the tab list as a
	// viewer sees it (another session's current window, a window linked
	// into or renamed in another session, sessions coming and going).
	ctlTabsChanged
	// ctlOverflow: a line longer than maxCtlLine was dropped, so output may
	// be missing; whoever tracks pane state starts again (ADR 0020, §11).
	ctlOverflow
	// ctlRestart: the control client exited and was started again; events
	// between the two were never seen (ADR 0020, decision 2). It comes once
	// the new client is attached and has answered its first commands.
	ctlRestart
	// ctlReady: the session's first client is attached and has answered its
	// first commands; a subscriber that couldn't send one before it can now.
	ctlReady
	// ctlSessionGone: the session no longer exists, so its client stopped
	// for good (R-0167); the next subscriber starts one again.
	ctlSessionGone
	// ctlLayout: a window's layout changed (%layout-change); for a tab's
	// single pane, its size.
	ctlLayout
	// ctlPause: tmux paused a pane's output for this client, which falls
	// too far behind (pause-after); output was skipped (ADR 0020, §11).
	ctlPause
	// ctlReply: the reply to a command this client sent (send).
	ctlReply
)

// maxCtlLine is the longest control-mode line the helper buffers (R-0162).
// tmux writes one %output line per read from a pane, at most a few KB of
// output escaped up to four times; anything longer is dropped whole and
// reported as ctlOverflow, so a hostile or broken stream can't grow memory.
const maxCtlLine = 1 << 20

type ctlEvent struct {
	kind   ctlKind
	window string // @N
	pane   string // %N, for output, a layout or a pause
	name   string // for a rename
	data   []byte // for output, decoded
	// For ctlLayout: the pane's size.
	cols, rows int
	// For ctlOutput, from the pane's parser: the marks in this chunk, the
	// authors of the commands they start, and whether the parser had to
	// start afresh (its marks are gone).
	marks   []paneMark
	authors []recordedAuthor
	lost    bool
	// For ctlReply: the command's ID, its output lines, whether tmux said
	// %error, and the pane's parser as it stood when the reply came, if the
	// command asked for it (snapPane).
	id     uint64
	lines  []string
	failed bool
	snap   *paneSnapshot
}

func parseControlLine(line string) ctlEvent {
	verb, rest, _ := strings.Cut(line, " ")
	switch verb {
	case "%window-add":
		return ctlEvent{kind: ctlWindowAdd, window: rest}
	case "%window-close", "%unlinked-window-close":
		return ctlEvent{kind: ctlWindowClose, window: rest}
	case "%window-renamed":
		id, name, _ := strings.Cut(rest, " ")
		return ctlEvent{kind: ctlWindowRenamed, window: id, name: name}
	case "%unlinked-window-add", "%unlinked-window-renamed", "%session-window-changed", "%window-pane-changed",
		"%sessions-changed", "%session-changed", "%session-renamed":
		return ctlEvent{kind: ctlTabsChanged}
	case "%layout-change":
		// %layout-change @N <layout> <visible layout> <flags>; a one-pane
		// layout is "<checksum>,<cols>x<rows>,<x>,<y>,<pane number>".
		window, rest, _ := strings.Cut(rest, " ")
		layout, _, _ := strings.Cut(rest, " ")
		e := ctlEvent{kind: ctlLayout, window: window}
		if f := strings.Split(layout, ","); len(f) == 5 {
			if c, r, ok := strings.Cut(f[1], "x"); ok {
				e.cols, _ = strconv.Atoi(c)
				e.rows, _ = strconv.Atoi(r)
				e.pane = "%" + f[4]
			}
		}
		return e
	case "%pause":
		return ctlEvent{kind: ctlPause, pane: rest}
	case "%output":
		pane, data, _ := strings.Cut(rest, " ")
		return ctlEvent{kind: ctlOutput, pane: pane, data: decodeControlOutput(data)}
	case "%extended-output":
		// With pause-after: %extended-output %P <age> [more fields] : <data>.
		pane, more, _ := strings.Cut(rest, " ")
		_, data, ok := strings.Cut(more, " : ")
		if !ok {
			if !strings.HasSuffix(more, " :") {
				return ctlEvent{}
			}
			data = ""
		}
		return ctlEvent{kind: ctlOutput, pane: pane, data: decodeControlOutput(data)}
	case "%exit":
		return ctlEvent{kind: ctlExit}
	}
	return ctlEvent{}
}

// decodeControlOutput undoes control mode's escaping of %output: bytes
// below 32 and the backslash are written as \ooo (three octal digits). The
// data is bytes, not text: a UTF-8 character or an escape sequence split
// across two lines is decoded as it came, never joined or re-encoded, so
// whoever reads the pane keeps its own state across lines (R-0133 item 8).
func decodeControlOutput(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out = append(out, byte(n))
				i += 3
				continue
			}
		}
		out = append(out, s[i])
	}
	return out
}

// tabShells tracks each tab's shell from control-mode events: OSC 133 state
// per window, fed by the output of that window's pane.
type tabShells struct {
	paneWindow map[string]string // %P -> @N
	shells     map[string]*osc133
}

func newTabShells() *tabShells {
	return &tabShells{paneWindow: map[string]string{}, shells: map[string]*osc133{}}
}

// setPane records which window a pane belongs to (from list-panes).
func (t *tabShells) setPane(pane, window string) { t.paneWindow[pane] = window }

func (t *tabShells) shell(window string) *osc133 {
	s := t.shells[window]
	if s == nil {
		s = &osc133{}
		t.shells[window] = s
	}
	return s
}

// apply takes one event; output for a pane it can't place is reported, so
// the caller refreshes the pane map.
func (t *tabShells) apply(e ctlEvent) (unknownPane bool) {
	switch e.kind {
	case ctlWindowAdd:
		t.shell(e.window)
	case ctlWindowClose:
		delete(t.shells, e.window)
		for p, w := range t.paneWindow {
			if w == e.window {
				delete(t.paneWindow, p)
			}
		}
	case ctlOutput:
		w, ok := t.paneWindow[e.pane]
		if !ok {
			return true
		}
		t.shell(w).Feed(e.data)
	}
	return false
}

var errCtlLineTooLong = errors.New("control-mode line longer than maxCtlLine")

// readControlLines reads control-mode lines from r and calls line for each,
// without its newline (and a trailing CR), until r ends. A line longer than
// max is dropped whole and line gets nil with errCtlLineTooLong, so memory
// stays under max however long the line runs. It returns r's error, or nil
// at EOF.
func readControlLines(r io.Reader, max int, line func(l []byte, err error)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	dropping := false
	for {
		chunk, err := br.ReadSlice('\n')
		full := err == nil
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) {
				if len(chunk) > 0 && !dropping && len(buf)+len(chunk) <= max {
					line(bytes.TrimSuffix(append(buf, chunk...), []byte("\r")), nil)
				} else if len(chunk) > 0 || dropping {
					line(nil, errCtlLineTooLong)
				}
				return nil
			}
			return err
		}
		if !dropping {
			if len(buf)+len(chunk) > max+1 {
				dropping, buf = true, buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if !full {
			continue
		}
		if dropping {
			line(nil, errCtlLineTooLong)
			dropping = false
		} else {
			l := bytes.TrimSuffix(bytes.TrimSuffix(buf, []byte("\n")), []byte("\r"))
			line(l, nil)
		}
		buf = buf[:0]
	}
}
