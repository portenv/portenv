// SPDX-License-Identifier: Apache-2.0

package agent

import (
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
	ctlOutput                // %output %P data
	ctlExit                  // %exit: the client is detached
)

type ctlEvent struct {
	kind   ctlKind
	window string // @N
	pane   string // %N, for output
	name   string // for a rename
	data   []byte // for output, decoded
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
	case "%output":
		pane, data, _ := strings.Cut(rest, " ")
		return ctlEvent{kind: ctlOutput, pane: pane, data: decodeControlOutput(data)}
	case "%exit":
		return ctlEvent{kind: ctlExit}
	}
	return ctlEvent{}
}

// decodeControlOutput undoes control mode's escaping of %output: bytes
// below 32 and the backslash are written as \ooo (three octal digits).
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
