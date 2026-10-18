// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// paneHarness follows one tmux pane through the session's control-mode
// client into a pane parser, as the helper does (ADR 0020, decision 6).
type paneHarness struct {
	t      *testing.T
	raw    func(args ...string) string
	tmux   tmuxFunc
	pane   string
	parser *paneParser
	mu     sync.Mutex
	marks  []paneMark
}

func newPaneHarness(t *testing.T, shell string) *paneHarness {
	t.Helper()
	tb, raw, cmdf := testTabsCmd(t)
	cmd := newShellCommand(t, shell)
	// Registered after the shell's TempDir, so it runs before that dir is
	// removed (Cleanup is last in, first out).
	t.Cleanup(func() { stopTmuxServer(t, tb.tmux) })
	raw("new-session", "-d", "-s", "boot", "sleep 600")
	raw("set-option", "-g", "default-command", cmd)
	raw("set-option", "-g", "history-limit", "100000")
	if err := tb.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw("kill-session", "-t", "=boot")
	h := &paneHarness{t: t, raw: raw, tmux: tb.tmux}
	h.pane = raw("display-message", "-p", "-t", "=main:", "#{pane_id}")
	w, _ := strconv.Atoi(raw("display-message", "-p", "-t", "=main:", "#{pane_width}"))
	hgt, _ := strconv.Atoi(raw("display-message", "-p", "-t", "=main:", "#{pane_height}"))
	h.parser = newPaneParser(w, hgt, t.Logf)
	hub := newCtlHub(tb.tmux, cmdf)
	ctx, cancel := context.WithCancel(context.Background())
	sub, stop, err := hub.subscribe(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); cancel() })
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.kick:
			}
			for _, e := range sub.take() {
				if e.kind != ctlOutput || e.pane != h.pane {
					continue
				}
				ms, lost := h.parser.feed(e.data)
				if lost {
					t.Errorf("the parser was reset")
				}
				h.mu.Lock()
				h.marks = append(h.marks, ms...)
				h.mu.Unlock()
			}
		}
	}()
	// The client attaches after the first prompt: a command sees it start.
	time.Sleep(300 * time.Millisecond)
	return h
}

func (h *paneHarness) countA() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.marks {
		if m.kind == markPrompt {
			n++
		}
	}
	return n
}

// run types a command and waits for its next prompt.
func (h *paneHarness) run(cmd string) {
	h.t.Helper()
	before := h.countA()
	h.raw("send-keys", "-t", "=main:", "-l", cmd)
	h.raw("send-keys", "-t", "=main:", "Enter")
	for end := time.Now().Add(10 * time.Second); h.countA() == before; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			h.t.Fatalf("no prompt after %q", cmd)
		}
	}
	time.Sleep(50 * time.Millisecond)
}

// stopTmuxServer kills the test's tmux server and waits, for a bounded
// time, for the server and its shells to exit and its socket to go dead
// (tmux leaves the file behind; it is removed here). A shell still exiting
// writes into its home (bash saves its history on SIGHUP), which raced
// TempDir's RemoveAll: "directory not empty" (R-0269).
func stopTmuxServer(t *testing.T, tmux tmuxFunc) {
	t.Helper()
	ctx := context.Background()
	out, err := tmux(ctx, "list-panes", "-a", "-F", "#{socket_path} #{pid} #{pane_pid}")
	if err != nil {
		return // no server is running
	}
	var sock string
	pids := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		sock = f[0]
		for _, s := range f[1:] {
			if pid, err := strconv.Atoi(s); err == nil && pid > 0 {
				pids[pid] = true
			}
		}
	}
	_, _ = tmux(ctx, "kill-server")
	const limit = 10 * time.Second
	deadline := time.Now().Add(limit)
	for {
		var alive []int
		for pid := range pids {
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				alive = append(alive, pid)
			}
		}
		sockGone := sock == ""
		if !sockGone {
			c, err := net.Dial("unix", sock)
			if err == nil {
				_ = c.Close()
			}
			sockGone = err != nil
		}
		if len(alive) == 0 && sockGone {
			if sock != "" {
				_ = os.Remove(sock)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("tmux server still not gone %v after kill-server: processes %v alive, socket %q dead: %v", limit, alive, sock, sockGone)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// check compares every prompt mark with tmux's own history: aligned at the
// cursor, each mark's line is a prompt line in tmux's capture, and no two
// marks share a line.
func (h *paneHarness) check(promptPrefix string) (checked int) {
	h.t.Helper()
	time.Sleep(200 * time.Millisecond)
	hist, _ := strconv.ParseUint(h.raw("display-message", "-p", "-t", "=main:", "#{history_size}"), 10, 64)
	cy, _ := strconv.ParseUint(h.raw("display-message", "-p", "-t", "=main:", "#{cursor_y}"), 10, 64)
	// Untrimmed: raw() trims, which would drop a blank first line and
	// shift every index.
	out, err := h.tmux(context.Background(), "capture-pane", "-p", "-S", "-", "-E", "-", "-t", "=main:")
	if err != nil {
		h.t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	// Aligned at the cursor: the client may attach after the first prompt
	// (or tmux may clear its history), so the two counts differ by a
	// constant within an epoch.
	offset := int64(h.parser.cursorLine()) - int64(hist+cy) // #nosec G115 -- line counts
	seen := map[int64]bool{}
	marks := h.parser.allMarks()
	epoch := marks[len(marks)-1].epoch
	for _, m := range marks {
		if m.kind != markPrompt || m.epoch != epoch {
			continue // lines compare only within the current epoch
		}
		i := int64(m.line) - offset // #nosec G115 -- a line count
		if seen[i] {
			h.t.Errorf("two prompt marks on line %d", i)
		}
		seen[i] = true
		if i < 0 || i >= int64(len(lines)) || !strings.HasPrefix(lines[i]+" ", promptPrefix) { // capture strips a trailing space
			got := ""
			if i >= 0 && i < int64(len(lines)) {
				got = lines[i]
			}
			h.t.Errorf("mark %v maps to tmux line %d %q, not a prompt", m, i, got)
		}
		checked++
	}
	return checked
}

// The parser counts lines as tmux does, through scrolling, long output, a
// pager on the alternate screen, an erase without a history clear, and
// identical prompts that a text match can't tell apart (R-0133 item 6).
func TestPaneParserCountsLinesAsTmuxDoes(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			h := newPaneHarness(t, shell)
			prompt := map[string]string{"bash": "$ ", "zsh": "% "}[shell]
			for i := range 20 {
				h.run(fmt.Sprintf("seq 1 3 | sed s/^/out-%d-/", i))
			}
			h.run("seq 1 300")
			h.run("printf '\\033[?1049h'; seq 1 5; printf '\\033[?1049l'")
			h.run("printf '\\033[30S\\033[3T'")
			for range 20 {
				h.run("true")
			}
			if n := h.check(prompt); n < 40 {
				t.Errorf("only %d prompt marks checked before the erase", n)
			}
			// A full-screen erase starts an epoch; within the new one, lines
			// match tmux again.
			before := h.parser.allMarks()
			h.run("printf '\\033[2J\\033[H'")
			after := h.parser.allMarks()
			if after[len(after)-1].epoch != before[len(before)-1].epoch+1 {
				t.Errorf("the erase didn't start an epoch: %v", after[len(after)-3:])
			}
			for range 20 {
				h.run("true")
			}
			if n := h.check(prompt); n < 20 {
				t.Errorf("only %d prompt marks checked after the erase", n)
			}
		})
	}
}

// The parser only reads: a query's reply is drained and dropped, so a
// program asking for one never blocks it, however many it asks.
func TestPaneParserNeverBlocksOnAQuery(t *testing.T) {
	p := newPaneParser(80, 24, t.Logf)
	done := make(chan struct{})
	go func() {
		for range 2000 {
			p.feed([]byte("\x1b[c\x1b[6n\x1b[>c\x1b]11;?\x07\x1b[?2004$p"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("feeding queries blocked")
	}
}

// A panic in one pane's parser resets that parser, is logged once, and
// never reaches the helper; the parser works again afterwards.
func TestPaneParserRecoversFromAPanic(t *testing.T) {
	var logs []string
	p := newPaneParser(80, 24, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	p.feed([]byte("\x1b]133;A\x07$ "))
	panics := 2
	p.beforeWrite = func([]byte) {
		if panics > 0 {
			panics--
			panic("planted")
		}
	}
	for range 2 {
		if _, lost := p.feed([]byte("x")); !lost {
			t.Fatal("a panic wasn't reported as lost")
		}
	}
	if len(logs) != 1 || p.resets != 2 {
		t.Errorf("logs %q, resets %d; want one log line and two resets", logs, p.resets)
	}
	if len(p.allMarks()) != 0 {
		t.Error("marks from before the reset were kept")
	}
	ms, lost := p.feed([]byte("\x1b]133;A\x07$ "))
	if lost || len(ms) != 1 {
		t.Errorf("after the reset: %v, lost %v", ms, lost)
	}
}

// Memory stays bounded: the scrollback is emptied after every chunk, the
// size is capped, and a pane keeps at most maxPaneMarks marks; lines are
// still counted exactly.
func TestPaneParserLimits(t *testing.T) {
	p := newPaneParser(5000, 5000, t.Logf)
	if p.cols != maxPaneCols || p.rows != maxPaneRows {
		t.Errorf("size %dx%d, want capped to %dx%d", p.cols, p.rows, maxPaneCols, maxPaneRows)
	}
	q := newPaneParser(80, 24, t.Logf)
	lines := strings.Repeat("y\r\n", 100_000)
	q.feed([]byte(lines))
	if q.emu.ScrollbackLen() != 0 {
		t.Errorf("scrollback holds %d lines after a chunk", q.emu.ScrollbackLen())
	}
	if got := q.cursorLine(); got != 100_000 {
		t.Errorf("cursor line %d after 100,000 lines, want 100,000", got)
	}
	// Scrolls far larger than the screen, in one chunk.
	r := newPaneParser(80, 24, t.Logf)
	r.feed([]byte(strings.Repeat("\x1b[9999S", 50) + strings.Repeat("\n", 30)))
	if r.emu.ScrollbackLen() != 0 {
		t.Errorf("scrollback holds %d lines", r.emu.ScrollbackLen())
	}
	m := newPaneParser(80, 24, t.Logf)
	m.feed([]byte(strings.Repeat("\x1b]133;A\x07", maxPaneMarks+500)))
	if n := len(m.allMarks()); n != maxPaneMarks {
		t.Errorf("%d marks kept, want %d", n, maxPaneMarks)
	}
}

// A width change starts an epoch (tmux reflows, vt doesn't); a height
// change doesn't.
func TestPaneParserEpochs(t *testing.T) {
	p := newPaneParser(80, 24, t.Logf)
	p.resize(80, 30)
	p.feed([]byte("\x1b]133;A\x07"))
	p.resize(100, 30)
	p.feed([]byte("\x1b]133;A\x07\x1b[2J\x1b]133;A\x07\x1b[3J\x1b]133;A\x07\x1b[J\x1b]133;A\x07"))
	var epochs []int
	for _, m := range p.allMarks() {
		epochs = append(epochs, m.epoch)
	}
	if fmt.Sprint(epochs) != "[0 1 2 3 3]" {
		t.Errorf("epochs %v, want [0 1 2 3 3]", epochs)
	}
}

// A pane's parser leaves nothing running once it's stopped.
func TestPaneParserStopsCleanly(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 200 {
		p := newPaneParser(80, 24, t.Logf)
		p.feed([]byte("\x1b[c hello\r\n"))
		p.stop()
	}
	for end := time.Now().Add(3 * time.Second); runtime.NumGoroutine() > before+5 && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+5 {
		t.Errorf("%d goroutines after 200 parsers came and went (from %d)", n, before)
	}
}

// A mark after lines that scrolled in the same chunk is counted on its own
// line: the chunk's scrolled lines are counted before the mark is placed.
func TestPaneParserMarksAfterScrollingInOneChunk(t *testing.T) {
	p := newPaneParser(80, 24, t.Logf)
	ms, _ := p.feed([]byte(strings.Repeat("a\r\n", 30) + "\x1b]133;A\x07$ \r\n" + strings.Repeat("b\r\n", 5) + "\x1b]133;A\x07"))
	if len(ms) != 2 || ms[0].line != 30 || ms[1].line != 36 {
		t.Errorf("marks %v, want A on lines 30 and 36", ms)
	}
}

// drainGoroutines counts the parsers' reply-draining goroutines.
func drainGoroutines() int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	return bytes.Count(buf, []byte("agent.(*paneParser).start.func1"))
}

// Each parser has one reply-draining goroutine: a panic's restart ends the
// old one, and stop ends the last (R-0176).
func TestPaneParserDrainGoroutinesEnd(t *testing.T) {
	waitFor := func(want int, what string) {
		t.Helper()
		end := time.Now().Add(3 * time.Second)
		for drainGoroutines() != want && time.Now().Before(end) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := drainGoroutines(); n != want {
			t.Fatalf("%s: %d drain goroutines, want %d", what, n, want)
		}
	}
	before := drainGoroutines()
	var ps []*paneParser
	for range 20 {
		p := newPaneParser(80, 24, t.Logf)
		p.beforeWrite = func(b []byte) {
			if bytes.Equal(b, []byte("boom")) {
				panic("planted")
			}
		}
		for range 3 {
			p.feed([]byte("boom"))
		}
		p.feed([]byte("\x1b[c hello"))
		ps = append(ps, p)
	}
	waitFor(before+20, "after restarts")
	for _, p := range ps {
		p.restart()
	}
	waitFor(before+20, "after restarting each")
	for _, p := range ps {
		p.stop()
	}
	waitFor(before, "after stopping each")
}

// A full reset (RIS) or a soft reset (DECSTR) clears the pen and bracketed
// paste (R-0178).
func TestPaneParserResetsClearThePen(t *testing.T) {
	for _, reset := range []string{"\x1bc", "\x1b[!p"} {
		p := newPaneParser(80, 24, t.Logf)
		p.feed([]byte("\x1b[?2004h\x1b[31mred"))
		if s := p.snapshot(); s.pen != "31" || !s.bracketedPaste {
			t.Fatalf("before %q: pen %q, bracketed paste %v", reset, s.pen, s.bracketedPaste)
		}
		p.feed([]byte(reset))
		if s := p.snapshot(); s.pen != "" || s.bracketedPaste {
			t.Errorf("after %q: pen %q, bracketed paste %v", reset, s.pen, s.bracketedPaste)
		}
		p.stop()
	}
}
