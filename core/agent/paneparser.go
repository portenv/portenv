// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// The helper's per-pane terminal parser (ADR 0020, decision 6; R-0142): it
// reads a pane's own output, as control mode gives it, to know where each
// OSC 133 mark is: the line a mark is on, counted from the pane's first line
// ever, the same counting a tab terminal's seed uses. It's
// charmbracelet/x/vt, pinned (v0.1.0), and it reads bytes a workspace
// program wrote, so:
//   - it only reads: what the emulator would reply (to DA, DSR and the like)
//     is drained and dropped, never written anywhere; the app's filter stays
//     the boundary (decision 9);
//   - each pane's parser runs under recover: a panic resets that pane's
//     parser, logs once and never takes the helper down;
//   - its memory is bounded: the size is capped, and the scrollback, which
//     the helper only counts (tmux keeps the history), is emptied after
//     every chunk.

// Limits for a pane's parser.
const (
	maxPaneCols = 1000
	maxPaneRows = 500
	// countScrollback holds the lines one chunk can push into the
	// scrollback; feed splits its input so a chunk never pushes more, and
	// empties it after each, so lines are counted exactly in bounded memory.
	countScrollback = 4096
	// maxPaneMarks is how many marks a pane keeps (the oldest go first).
	maxPaneMarks = 10000
)

// paneMarkKind is an OSC 133 mark: A a prompt, B input, C a command
// started, D;exit it finished.
type paneMarkKind byte

const (
	markPrompt  paneMarkKind = 'A'
	markInput   paneMarkKind = 'B'
	markCommand paneMarkKind = 'C'
	markDone    paneMarkKind = 'D'
)

// paneMark is a mark and the line it's on.
type paneMark struct {
	kind paneMarkKind
	line uint64
	// epoch counts the full-screen erases and width changes before the
	// mark (eraseDisplay); lines compare only within one.
	epoch int
	exit  int // for markDone; -1 when the shell didn't say
}

// paneParser follows one pane.
type paneParser struct {
	mu       sync.Mutex
	emu      *vt.Emulator
	cols     int
	rows     int
	scrolled uint64 // lines scrolled off the main screen, ever
	epoch    int
	marks    []paneMark
	fresh    []paneMark // marks seen in the current feed
	alt      bool
	logged   bool
	logf     func(string, ...any)
	// resets counts how often the parser was reset after a panic.
	resets int
	// beforeWrite, in tests only, runs before each write (to make one panic).
	beforeWrite func([]byte)
}

func newPaneParser(cols, rows int, logf func(string, ...any)) *paneParser {
	p := &paneParser{logf: logf}
	p.start(cols, rows)
	return p
}

func clampSize(cols, rows int) (int, int) {
	return min(max(cols, 1), maxPaneCols), min(max(rows, 1), maxPaneRows)
}

// start makes a fresh emulator. The line count and marks are kept.
func (p *paneParser) start(cols, rows int) {
	p.close()
	p.cols, p.rows = clampSize(cols, rows)
	e := vt.NewEmulator(p.cols, p.rows)
	e.SetScrollbackSize(countScrollback)
	// Replies go to a pipe nobody else reads: drain and drop them, or the
	// emulator would block on its first query.
	go func() { _, _ = io.Copy(io.Discard, e) }()
	e.RegisterOscHandler(133, func(data []byte) bool {
		p.mark(e, data)
		return true
	})
	// Erase display, seen before vt handles it (returning false lets it),
	// to count lines as tmux does.
	e.RegisterCsiHandler('J', func(params ansi.Params) bool {
		n, _, _ := params.Param(0, 0)
		p.eraseDisplay(e, n)
		return false
	})
	p.alt = false
	e.SetCallbacks(vt.Callbacks{AltScreen: func(on bool) { p.alt = on }})
	p.emu = e
}

// eraseDisplay ends an epoch when a program erases the whole screen (ADR
// 0020, decision 6). The line count can no longer be trusted to match
// tmux's across it:
//   - ED 2: tmux (scroll-on-clear, on by default) scrolls every line up to
//     the last one a program wrote into its history, a line of written
//     spaces included; vt keeps no record of which blank cells were written,
//     so the two can differ by a few lines (measured on tmux 3.4).
//   - ED 3: tmux empties its history; marks above it are gone from both.
//     vt empties its scrollback too, which holds this chunk's lines not yet
//     counted: they're counted first.
//
// Lines within an epoch are counted exactly; a tab terminal's seed (PR 3)
// aligns each epoch's marks with tmux on its own, and drops them if it
// can't (never a mark on the wrong line).
func (p *paneParser) eraseDisplay(e *vt.Emulator, n int) {
	if p.alt || (n != 2 && n != 3) {
		return
	}
	if n == 3 {
		p.scrolled += uint64(e.ScrollbackLen()) // #nosec G115 -- a length
		e.ClearScrollback()
	}
	p.epoch++
}

// stop ends the parser when its pane goes.
func (p *paneParser) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.close()
}

// close ends the emulator's reply pipe, which ends its drain. Not
// Emulator.Close: it sets a field the drain's Read reads unguarded (a data
// race in vt v0.1.0); closing the pipe's writer is safe from any goroutine.
func (p *paneParser) close() {
	if p.emu == nil {
		return
	}
	if pw, ok := p.emu.InputPipe().(*io.PipeWriter); ok {
		_ = pw.Close()
	}
}

// mark records an OSC 133 mark on the main screen, at the cursor's line.
// Marks on a full-screen program's own screen belong to no line.
func (p *paneParser) mark(e *vt.Emulator, data []byte) {
	// vt hands over the whole payload, "133;A".
	_, s, ok := strings.Cut(string(data), ";")
	if p.alt || !ok || s == "" {
		return
	}
	k := paneMarkKind(s[0])
	switch k {
	case markPrompt, markInput, markCommand, markDone:
	default:
		return
	}
	y := e.CursorPosition().Y
	m := paneMark{kind: k, epoch: p.epoch, line: p.scrolled + uint64(e.ScrollbackLen()) + uint64(max(y, 0)), exit: -1} // #nosec G115 -- non-negative
	if k == markDone {
		if _, code, ok := strings.Cut(s, ";"); ok {
			if n, err := strconv.Atoi(code); err == nil {
				m.exit = n
			}
		}
	}
	p.fresh = append(p.fresh, m)
}

// feed reads a chunk of the pane's output and returns the marks in it.
// After a panic the parser is started afresh (lost is true): the line
// count from then on no longer matches tmux's, so marks from before are
// dropped and the caller resets whoever follows the pane.
func (p *paneParser) feed(b []byte) (marks []paneMark, lost bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fresh = nil
	step := p.chunk()
	for len(b) > 0 {
		n := min(len(b), step)
		if !p.write(b[:n]) {
			p.marks, p.fresh = nil, nil
			return nil, true
		}
		b = b[n:]
	}
	p.keep(p.fresh)
	return p.fresh, false
}

// chunk is how many bytes one write may take so that it can't push more
// than countScrollback lines: a byte pushes at most one line, and a
// sequence of at least three bytes (CSI n S, CSI 2 J) at most a screen.
func (p *paneParser) chunk() int {
	return max(1, 3*countScrollback/(p.rows+3))
}

// write feeds one chunk under recover, and counts the lines it scrolled.
func (p *paneParser) write(b []byte) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if !p.logged {
				p.logf("portenv-agent: a pane's terminal parser failed (%v); started afresh", r)
				p.logged = true
			}
			p.resets++
			p.start(p.cols, p.rows)
			ok = false
		}
	}()
	if p.beforeWrite != nil {
		p.beforeWrite(b)
	}
	_, _ = p.emu.Write(b)
	p.scrolled += uint64(p.emu.ScrollbackLen()) // #nosec G115 -- a length
	p.emu.ClearScrollback()
	return true
}

func (p *paneParser) keep(ms []paneMark) {
	p.marks = append(p.marks, ms...)
	if over := len(p.marks) - maxPaneMarks; over > 0 {
		p.marks = append(p.marks[:0], p.marks[over:]...)
	}
}

// resize follows the pane's size. A height change pushes lines off the top
// (counted). A width change starts an epoch: tmux reflows wrapped lines in
// its history and vt doesn't, so lines before it no longer match.
func (p *paneParser) resize(cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cols, rows = clampSize(cols, rows)
	if cols == p.cols && rows == p.rows {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			p.logf("portenv-agent: a pane's terminal parser failed on resize (%v); started afresh", r)
			p.resets++
			p.marks = nil
			p.start(cols, rows)
		}
	}()
	if cols != p.cols {
		p.epoch++
	}
	p.emu.Resize(cols, rows)
	p.cols, p.rows = cols, rows
	p.scrolled += uint64(p.emu.ScrollbackLen()) // #nosec G115 -- a length
	p.emu.ClearScrollback()
}

// cursorLine is the cursor's line, counted as marks are.
func (p *paneParser) cursorLine() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	y := p.emu.CursorPosition().Y
	return p.scrolled + uint64(max(y, 0)) // #nosec G115 -- non-negative
}

// allMarks returns the marks the pane keeps, oldest first.
func (p *paneParser) allMarks() []paneMark {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]paneMark(nil), p.marks...)
}

func (m paneMark) String() string {
	if m.kind == markDone {
		return fmt.Sprintf("D;%d@%d.%d", m.exit, m.epoch, m.line)
	}
	return fmt.Sprintf("%c@%d.%d", m.kind, m.epoch, m.line)
}
