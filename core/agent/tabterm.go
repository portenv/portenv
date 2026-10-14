// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// One tab's terminal (ADR 0020, decisions 3–6 and 11): the tab's own output
// from the session's control-mode client, with a reset first (the tab's
// history and screen, cursor, modes and pen, and the OSC 133 marks the
// helper knows put back on their lines), and input and size to it.

// Limits for a tab terminal.
const (
	// inputChunk is the most bytes one send-keys -H carries.
	inputChunk = 512
	// maxInput is the most bytes one input message may carry.
	maxInput = 1 << 20
	// maxAuthorName is the longest author name accepted.
	maxAuthorName = 64
	// seedTimeout bounds waiting for a seed's replies.
	seedTimeout = 30 * time.Second
	// seedLines is the most history a seed carries (the app keeps 10,000
	// lines of scrollback), and seedBytes the most bytes, under gRPC's 4 MB
	// message limit; older lines are left out.
	seedLines = 10000
	seedBytes = 3 << 20
)

// seedFormat is what a seed reads about a tab's pane, in one display, in
// the same command list as its captures (so no output lands between them).
const seedFormat = "#{pane_id} #{pane_width} #{pane_height} #{history_size} #{cursor_x} #{cursor_y} #{alternate_on} " +
	"#{scroll_region_upper} #{scroll_region_lower} #{origin_flag} #{wrap_flag} #{insert_flag} #{keypad_cursor_flag} " +
	"#{keypad_flag} #{mouse_standard_flag} #{mouse_button_flag} #{mouse_any_flag} #{mouse_sgr_flag} #{cursor_flag} " +
	"#{focus_flag}"

// TabTerminal serves one tab's terminal. The caller is portenvd (only it
// holds the channel's credentials), which sets the author from its own
// caller (R-0133 item 4, R-0175).
func (s *apiServer) TabTerminal(stream agentv1.AgentService_TabTerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "the first message must open a tab")
	}
	if !windowIDRE.MatchString(open.GetTab()) {
		return status.Error(codes.InvalidArgument, "a tab ID is @ and a number")
	}
	name, agentID := strings.TrimSpace(open.GetAuthor()), open.GetAuthorAgentId()
	if utf8.RuneCountInString(name) > maxAuthorName || strings.IndexFunc(name, isControl) >= 0 || !utf8.ValidString(name) {
		return status.Error(codes.InvalidArgument, "an author is a short name")
	}
	if len(agentID) > 128 || strings.IndexFunc(agentID, isControl) >= 0 {
		return status.Error(codes.InvalidArgument, "an agent ID is short and plain")
	}
	tb, err := s.tabsFor(open.GetSession(), "")
	if err != nil {
		return err
	}
	ctx := stream.Context()
	hub, err := s.ctlHub()
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "tab terminals: %v", err)
	}
	if err := tb.ensure(ctx); err != nil {
		return err
	}
	sub, stop, err := hub.subscribe(ctx, tb.session)
	if errors.Is(err, errTmuxTooOld) {
		return status.Error(codes.FailedPrecondition, "This workspace's tmux is too old for the app's terminal (needs 3.4 or later).")
	} else if err != nil {
		return status.Errorf(codes.Unavailable, "tab terminal: %v", err)
	}
	defer stop()
	t := &tabStream{inputIDs: map[uint64]int{}, hub: hub, session: tb.session, tab: open.GetTab(), sub: sub, stream: stream, author: name, agentID: agentID}
	if size := open.GetSize(); size.GetCols() > 0 && size.GetRows() > 0 {
		t.size = [2]int{int(size.GetCols()), int(size.GetRows())} // #nosec G115 -- checked below
	}
	return t.run(ctx)
}

func isControl(r rune) bool { return r < 0x20 || (r >= 0x7f && r < 0xa0) }

// tabStream is one open tab terminal.
type tabStream struct {
	hub     *ctlHub
	session string
	tab     string
	pane    string
	// backlog is the subscriber's events not yet handled.
	backlog []ctlEvent
	// inputIDs is the bytes of input in each send-keys not yet answered.
	inputIDs map[uint64]int
	sub      *ctlSub
	stream   agentv1.AgentService_TabTerminalServer
	author   string
	agentID  string
	size     [2]int // the size asked for, if any
	sent     [2]int // the size last told the viewer
	// lineBase turns the parser's line numbers into the viewer's (its
	// first line is the seed's first) since the last reset.
	lineBase int64
}

// tabInput is what the reader of the client's messages hands on.
type tabInput struct {
	data   []byte
	resize [2]int
	err    error
}

func (t *tabStream) client() (*ctlClient, error) {
	c := t.hub.client(t.session)
	if c == nil {
		return nil, errNoClient
	}
	return c, nil
}

// run sends the reset, then output, until the tab closes, the client goes
// away, or the stream ends.
func (t *tabStream) run(ctx context.Context) error {
	inputs := make(chan tabInput, 16)
	go func() {
		defer close(inputs)
		for {
			m, err := t.stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					inputs <- tabInput{err: err}
				}
				return
			}
			switch {
			case m.GetInput() != nil:
				if len(m.GetInput()) > maxInput {
					inputs <- tabInput{err: status.Error(codes.InvalidArgument, "input messages are at most 1 MB")}
					return
				}
				inputs <- tabInput{data: m.GetInput()}
			case m.GetResize() != nil:
				r := m.GetResize()
				inputs <- tabInput{resize: [2]int{int(r.GetCols()), int(r.GetRows())}} // #nosec G115 -- clamped when used
			}
		}
	}()
	if ended, err := t.reset(ctx, false); ended || err != nil {
		return err
	}
	for {
		if len(t.backlog) > 0 {
			e := t.backlog[0]
			t.backlog = t.backlog[1:]
			done, reseed, err := t.event(e)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			if reseed != nil {
				// The reset drops what's left of the backlog's output: it
				// came before the seed.
				if ended, err := t.reset(ctx, *reseed); ended || err != nil {
					return err
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case in, ok := <-inputs:
			if !ok {
				inputs = nil // the client stopped sending; keep streaming output
				continue
			}
			if in.err != nil {
				return in.err
			}
			if in.resize != [2]int{} {
				t.size = in.resize
				if err := t.resize(); err != nil {
					return err
				}
				continue
			}
			ids, err := t.input(in.data)
			if err != nil {
				return status.Errorf(codes.Unavailable, "%d bytes of input weren't delivered: %v", len(in.data), err)
			}
			for id, n := range ids {
				t.inputIDs[id] = n
			}
		case <-t.sub.kick:
			t.backlog = t.sub.take()
		}
	}
}

// event handles one event from the session's client. It ends the stream
// (done) when the tab is gone, and asks for a new reset after anything that
// lost output.
func (t *tabStream) event(e ctlEvent) (done bool, reseed *bool, err error) {
	yes, no := true, false
	switch e.kind {
	case ctlOutput:
		if e.pane != t.pane {
			return false, nil, nil
		}
		if e.lost {
			return false, &no, nil
		}
		// Each author goes before the chunk in which its command's mark
		// completes (the CommandAuthor contract, #110).
		for _, a := range e.authors {
			line := int64(a.line) - t.lineBase // #nosec G115 -- a line count
			if line < 0 {
				continue
			}
			if err := t.stream.Send(&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Author{Author: commandAuthor(a, uint64(line))}}); err != nil {
				return false, nil, err
			}
		}
		return false, nil, t.stream.Send(&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Output{Output: e.data}})
	case ctlWindowClose:
		if e.window != t.tab {
			return false, nil, nil
		}
		// Every %output of the pane came before its %window-close.
		return true, nil, t.end(agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED)
	case ctlLayout:
		if e.window == t.tab && [2]int{e.cols, e.rows} != t.sent {
			t.sent = [2]int{e.cols, e.rows}
			return false, nil, t.stream.Send(&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Resized{Resized: termSize(e.cols, e.rows)}})
		}
	case ctlPause:
		if e.pane == t.pane {
			return false, &yes, nil
		}
	case ctlOverflow:
		return false, &yes, nil
	case ctlRestart, ctlReady:
		return false, &no, nil
	case ctlSessionGone, ctlExit:
		return true, nil, t.end(agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED)
	case ctlReply:
		return false, nil, t.inputReply(e)
	}
	return false, nil, nil
}

// inputReply ends the stream if input wasn't delivered, saying how much.
func (t *tabStream) inputReply(e ctlEvent) error {
	n, ok := t.inputIDs[e.id]
	if !ok {
		return nil
	}
	delete(t.inputIDs, e.id)
	if !e.failed {
		return nil
	}
	undelivered := n
	for _, m := range t.inputIDs {
		undelivered += m
	}
	return status.Errorf(codes.Unavailable, "%d bytes of input weren't delivered: %s", undelivered, strings.Join(e.lines, " "))
}

func (t *tabStream) end(why agentv1.TabTerminalEnd) error {
	return t.stream.Send(&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_End{End: why}})
}

func termSize(cols, rows int) *agentv1.TerminalSize {
	return &agentv1.TerminalSize{Cols: uint32(max(cols, 0)), Rows: uint32(max(rows, 0))} // #nosec G115 -- non-negative
}

func commandAuthor(a recordedAuthor, line uint64) *agentv1.CommandAuthor {
	return &agentv1.CommandAuthor{Line: line, Name: a.name, AgentId: a.agentID}
}

// clampSizeOf keeps a requested size within what a pane's parser takes.
func clampSizeOf(s [2]int) [2]int {
	c, r := clampSize(s[0], s[1])
	return [2]int{c, r}
}

// resize asks for the tab's window at this view's size (per window, so
// other tabs keep theirs; the latest asked for wins, ADR 0020 decision 5).
func (t *tabStream) resize() error {
	if t.size == [2]int{} {
		return nil
	}
	c, err := t.client()
	if err != nil {
		return nil // the next reset asks again
	}
	s := clampSizeOf(t.size)
	_, err = c.send(fmt.Sprintf("refresh-client -C %s:%dx%d", t.tab, s[0], s[1]), "", nil)
	if err != nil && !errors.Is(err, errNoClient) {
		return status.Errorf(codes.Unavailable, "resize: %v", err)
	}
	return nil
}

// input sends bytes to the tab's pane as send-keys -H, at most inputChunk
// bytes a command, each after leaving any mode a program put the pane in
// (copy mode would swallow the keys; R-0133 item 8). The commands run in
// order on the client, so the bytes arrive in the order typed. An Enter
// records this stream's author for the command it starts.
func (t *tabStream) input(b []byte) (map[uint64]int, error) {
	c, err := t.client()
	if err != nil {
		return nil, err
	}
	ids := map[uint64]int{}
	for len(b) > 0 {
		n := min(len(b), inputChunk)
		chunk := b[:n]
		b = b[n:]
		if strings.ContainsAny(string(chunk), "\r\n") {
			c.typed(t.pane, t.author, t.agentID)
		}
		var sb strings.Builder
		sb.WriteString("copy-mode -q -t " + t.pane + " ; send-keys -t " + t.pane + " -H")
		h := hex.EncodeToString(chunk)
		for i := 0; i < len(h); i += 2 {
			sb.WriteString(" " + h[i:i+2])
		}
		id, err := c.send(sb.String(), "", nil)
		if err != nil {
			return nil, err
		}
		ids[id] = n
	}
	return ids, nil
}

// reset sends the tab's current state: its pane is found, its size set,
// then the seed is taken in one command list, so no output lands inside
// it; output for the pane until the seed's reply came is already in the
// seed and is dropped, and everything after follows (ADR 0020, decision 6).
var (
	// errReseed: the client started again while a reset waited for it.
	errReseed = errors.New("the control client started again")
	// errTabClosed: a reset found the tab gone and said so; the stream ends.
	errTabClosed = errors.New("the tab is gone")
)

// closed tells the viewer the tab is gone, for a reset to return.
func (t *tabStream) closed() error {
	if err := t.end(agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED); err != nil {
		return err
	}
	return errTabClosed
}

func (t *tabStream) reset(ctx context.Context, skipped bool) (ended bool, err error) {
	for {
		err := t.resetOnce(ctx, skipped)
		switch {
		case errors.Is(err, errReseed):
		case errors.Is(err, errTabClosed):
			return true, nil
		default:
			return false, err
		}
	}
}

// resetOnce sends a reset. If the client isn't taking commands, it sends
// nothing: its ctlReady or ctlRestart asks again, and until then the
// pane's output is dropped.
func (t *tabStream) resetOnce(ctx context.Context, skipped bool) error {
	t.pane = ""
	c, err := t.client()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, seedTimeout)
	defer cancel()
	// The tab's pane.
	id, err := c.send("display -p -t "+t.tab+" '#{pane_id}'", "", nil)
	if err != nil {
		return nil
	}
	r, ok, err := t.waitReply(ctx, id)
	if err != nil || !ok {
		return err
	}
	pane := strings.TrimSpace(strings.Join(r.lines, ""))
	if r.failed || !strings.HasPrefix(pane, "%") {
		return t.closed() // the tab is gone
	}
	t.pane = pane
	size := ""
	if t.size != [2]int{} {
		s := clampSizeOf(t.size)
		size = fmt.Sprintf("refresh-client -C %s:%dx%d ; ", t.tab, s[0], s[1])
	}
	line := size + "display -p -t " + pane + " '" + seedFormat + "' ; capture-pane -p -e -S -" + strconv.Itoa(seedLines) + " -E - -t " + pane +
		" ; capture-pane -a -q -p -e -t " + pane
	// Replies come in order: the display, the main screen, the alternate
	// screen; the last one carries the parser's snapshot.
	first, err := c.send(line, pane, nil)
	if err != nil {
		return nil
	}
	n := strings.Count(line, " ; ") + 1
	replies := make([]ctlEvent, 0, n)
	for i := uint64(0); i < uint64(n); i++ { // #nosec G115 -- small
		r, ok, err := t.waitReply(ctx, first-uint64(n-1)+i)
		if err != nil || !ok {
			return err
		}
		replies = append(replies, r)
	}
	disp, main, alt := replies[n-3], replies[n-2], replies[n-1]
	if disp.failed || main.failed {
		return t.closed()
	}
	seed, sz, authors, base, err := buildSeed(disp.lines, main.lines, alt.lines, alt.snap, alt.authors)
	if err != nil {
		return status.Errorf(codes.Internal, "seed: %v", err)
	}
	t.sent, t.lineBase = sz, base
	return t.stream.Send(&agentv1.TabTerminalResponse{Msg: &agentv1.TabTerminalResponse_Reset_{Reset_: &agentv1.TabTerminalReset{
		Size: termSize(sz[0], sz[1]), Seed: seed, Authors: authors, Skipped: skipped,
	}}})
}

// waitReply waits for a reply, dropping output for this tab's pane meanwhile
// (it's in the seed) and handling the events that end or redo the stream.
func (t *tabStream) waitReply(ctx context.Context, id uint64) (ctlEvent, bool, error) {
	for {
		for len(t.backlog) > 0 {
			e := t.backlog[0]
			t.backlog = t.backlog[1:]
			switch e.kind {
			case ctlReply:
				if e.id == id {
					return e, true, nil
				}
				if err := t.inputReply(e); err != nil {
					return ctlEvent{}, false, err
				}
			case ctlWindowClose:
				if e.window == t.tab {
					return ctlEvent{}, false, t.closed()
				}
			case ctlSessionGone, ctlExit:
				return ctlEvent{}, false, t.closed()
			case ctlRestart, ctlReady:
				return ctlEvent{}, false, errReseed
			}
		}
		select {
		case <-ctx.Done():
			return ctlEvent{}, false, status.Error(codes.DeadlineExceeded, "the tab's state didn't come")
		case <-t.sub.kick:
			t.backlog = t.sub.take()
		}
	}
}

// buildSeed makes a reset's seed from the display line and the captures:
// the main screen's history and screen, with each mark of the parser's
// current epoch put back at the start of its line; the alternate screen if
// one shows; then the scroll region, modes, pen and cursor.
//
// It returns the seed, the tab's size, the authors of the commands whose
// marks it carries (their lines numbered as the seed's), and the base that
// turns the parser's later line numbers into the viewer's.
func buildSeed(disp, main, alt []string, snap *paneSnapshot, authors []recordedAuthor) ([]byte, [2]int, []*agentv1.CommandAuthor, int64, error) {
	if len(disp) != 1 {
		return nil, [2]int{}, nil, 0, fmt.Errorf("display: %q", disp)
	}
	// A flag tmux hasn't set (focus_flag before any focus event) is empty.
	f := strings.Split(disp[0], " ")
	if len(f) != 20 {
		return nil, [2]int{}, nil, 0, fmt.Errorf("display: %q", disp[0])
	}
	n := make([]int, len(f))
	for i := 1; i < len(f); i++ {
		if f[i] == "" {
			continue
		}
		v, err := strconv.Atoi(f[i])
		if err != nil {
			return nil, [2]int{}, nil, 0, fmt.Errorf("display field %d: %q", i, f[i])
		}
		n[i] = v
	}
	cols, rows, cx, cy, altOn := n[1], n[2], n[4], n[5], n[6] == 1
	top, bottom := n[7], n[8]
	size := [2]int{cols, rows}
	if len(main) < rows {
		return nil, [2]int{}, nil, 0, fmt.Errorf("capture: %d lines for %d rows", len(main), rows)
	}
	// Under gRPC's limit: the oldest history lines go first, never the
	// screen.
	total := 0
	for _, l := range main {
		total += len(l) + 8
	}
	for total > seedBytes && len(main) > rows {
		total -= len(main[0]) + 8
		main = main[1:]
	}
	// The seed's line of the cursor (history above the seed was left out).
	cursorSeed := int64(len(main) - rows + cy) // #nosec G115 -- line counts

	// Marks of the current epoch, by capture line: aligned at the cursor.
	// Older epochs can't be aligned (a full-screen erase or a reflow came
	// between), so their marks aren't replayed: never on the wrong line.
	byLine := map[int][]paneMark{}
	var out []*agentv1.CommandAuthor
	var base int64
	if snap != nil {
		// The parser's line of the seed's first line.
		base = int64(snap.cursorLine) - cursorSeed // #nosec G115 -- a line count
		for _, m := range snap.marks {
			if m.epoch != snap.epoch {
				continue
			}
			i := int64(m.line) - base // #nosec G115 -- a line count
			if i >= 0 && i < int64(len(main)) {
				byLine[int(i)] = append(byLine[int(i)], m)
			}
		}
		for _, a := range authors {
			if a.epoch != snap.epoch {
				continue
			}
			i := int64(a.line) - base // #nosec G115 -- a line count
			if i >= 0 && i < int64(len(main)) && hasCommandMark(byLine[int(i)]) {
				out = append(out, commandAuthor(a, uint64(i))) // #nosec G115 -- non-negative
			}
		}
	}
	var b strings.Builder
	b.WriteString("\x1b[0m\x1b[H\x1b[2J")
	for i, l := range main {
		if i > 0 {
			b.WriteString("\x1b[0m\r\n")
		}
		for _, m := range byLine[i] {
			b.WriteString(markSequence(m))
		}
		b.WriteString(l)
	}
	if altOn {
		b.WriteString("\x1b[0m\x1b[?1049h\x1b[H\x1b[2J")
		for i, l := range alt {
			if i > 0 {
				b.WriteString("\x1b[0m\r\n")
			}
			b.WriteString(l)
		}
	}
	b.WriteString("\x1b[0m")
	if top != 0 || bottom != rows-1 {
		fmt.Fprintf(&b, "\x1b[%d;%dr", top+1, bottom+1)
	}
	modes := []struct {
		on  bool
		set string
	}{
		{n[10] == 0, "\x1b[?7l"}, {n[11] == 1, "\x1b[4h"}, {n[12] == 1, "\x1b[?1h"},
		{n[13] == 1, "\x1b="}, {n[14] == 1, "\x1b[?1000h"}, {n[15] == 1, "\x1b[?1002h"}, {n[16] == 1, "\x1b[?1003h"},
		{n[17] == 1, "\x1b[?1006h"}, {n[18] == 0, "\x1b[?25l"}, {n[19] == 1, "\x1b[?1004h"},
	}
	for _, m := range modes {
		if m.on {
			b.WriteString(m.set)
		}
	}
	if snap != nil {
		if snap.bracketedPaste {
			b.WriteString("\x1b[?2004h")
		}
		if snap.pen != "" {
			b.WriteString("\x1b[" + snap.pen + "m")
		}
	}
	// The cursor last: origin mode (set last, as it homes the cursor) counts
	// rows from the scroll region's top.
	if n[9] == 1 {
		b.WriteString("\x1b[?6h")
		fmt.Fprintf(&b, "\x1b[%d;%dH", cy-top+1, cx+1)
	} else {
		fmt.Fprintf(&b, "\x1b[%d;%dH", cy+1, cx+1)
	}
	return []byte(b.String()), size, out, base, nil
}

func hasCommandMark(ms []paneMark) bool {
	for _, m := range ms {
		if m.kind == markCommand {
			return true
		}
	}
	return false
}

func markSequence(m paneMark) string {
	if m.kind == markDone {
		if m.exit >= 0 {
			return fmt.Sprintf("\x1b]133;D;%d\x1b\\", m.exit)
		}
		return "\x1b]133;D\x1b\\"
	}
	return "\x1b]133;" + string(rune(m.kind)) + "\x1b\\"
}
