// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The helper's control-mode clients (ADR 0020, decision 2): one `tmux -C`
// client per session, shared by everything that follows the session (the tab
// watch now; the waiting detector and the app's tab terminals in 1.2c's next
// PRs). It starts with the first subscriber and ends with the last, runs in a
// UTF-8 locale (envFor), and is started again if it exits; subscribers then
// get ctlRestart, since events in between were never seen. Both come once
// the client takes commands (ctlReady the first time).

// tmuxCmdFunc builds a tmux command as the main user, for a long-running
// client (control mode) rather than one call.
type tmuxCmdFunc func(ctx context.Context, args ...string) *exec.Cmd

// minTmux is the oldest tmux control mode is used with: the version ADR 0020
// was measured on, and the one the toolbox image pins (decision 10).
var minTmux = [2]int{3, 4}

var tmuxVersionRE = regexp.MustCompile(`^tmux (?:next-)?(\d+)\.(\d+)`)

// errTmuxTooOld: the workspace's tmux is older than minTmux.
var errTmuxTooOld = errors.New("this workspace's tmux is too old for the app's terminal (needs 3.4 or later)")

// checkTmuxVersion reads `tmux -V` ("tmux 3.4", "tmux 3.3a", "tmux next-3.6")
// and refuses one older than minTmux.
func checkTmuxVersion(ctx context.Context, run tmuxFunc) error {
	out, err := run(ctx, "-V")
	if err != nil {
		return fmt.Errorf("tmux -V: %w", err)
	}
	m := tmuxVersionRE.FindStringSubmatch(out)
	if m == nil {
		return fmt.Errorf("tmux -V: unexpected %q", out)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minTmux[0] || (major == minTmux[0] && minor < minTmux[1]) {
		return errTmuxTooOld
	}
	return nil
}

// ctlSubCap is how many events, and ctlSubBytes how many bytes of output, a
// subscriber may fall behind by. Past either, its queue is dropped and it
// gets ctlOverflow instead, so a slow subscriber never blocks the client or
// grows memory (ADR 0020, decision 11: a tab terminal then resets).
const (
	ctlSubCap   = 4096
	ctlSubBytes = 4 << 20
)

// ctlSub is one subscriber's queue of events.
type ctlSub struct {
	mu       sync.Mutex
	q        []ctlEvent
	qBytes   int
	maxBytes int // ctlSubBytes, or less in a test
	overflow bool
	kick     chan struct{}
}

// keepsThroughOverflow is what a subscriber that fell behind still gets:
// replies (a stream waits for its own) and the client's comings and goings,
// which a stream must act on after its reset. Each is small and rare.
func keepsThroughOverflow(k ctlKind) bool {
	switch k {
	case ctlReply, ctlReady, ctlRestart, ctlSessionGone, ctlExit:
		return true
	}
	return false
}

func (s *ctlSub) push(e ctlEvent) {
	s.mu.Lock()
	switch {
	case s.overflow:
		// Dropped until it's taken, except what keepsThroughOverflow.
		if keepsThroughOverflow(e.kind) {
			s.q = append(s.q, e)
		}
	case len(s.q) >= ctlSubCap || s.qBytes+len(e.data) > s.maxBytes:
		var keep []ctlEvent
		for _, q := range s.q {
			if keepsThroughOverflow(q.kind) {
				keep = append(keep, q)
			}
		}
		s.q, s.qBytes, s.overflow = keep, 0, true
		if keepsThroughOverflow(e.kind) {
			s.q = append(s.q, e)
		}
	default:
		s.q = append(s.q, e)
		s.qBytes += len(e.data)
	}
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// take returns the events queued since the last take; if some were dropped,
// it returns only ctlOverflow.
func (s *ctlSub) take() []ctlEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.q
	s.q, s.qBytes = nil, 0
	if s.overflow {
		s.overflow = false
		return append([]ctlEvent{{kind: ctlOverflow}}, q...) // q holds only what's kept
	}
	return q
}

// ctlClient is one session's control-mode client and its subscribers.
type ctlClient struct {
	mu     sync.Mutex
	subs   map[*ctlSub]bool
	cancel context.CancelFunc
	done   chan struct{}

	// The running client's input, and the commands sent on it whose
	// replies haven't come, in order (tmux answers in order).
	wmu     sync.Mutex
	stdin   io.Writer
	pending []ctlPending
	nextID  uint64

	// Each pane's parser and size, and who last sent Enter in it (ADR
	// 0020, decision 4), as the reader goroutine keeps them.
	pmu     sync.Mutex
	parsers map[string]*paneParser
	sizes   map[string][2]int
	windows map[string]string // @N -> %P
	enter   map[string]recordedAuthor
	authors map[string][]recordedAuthor
	logf    func(string, ...any)
}

// ctlPending is a command whose reply hasn't come.
type ctlPending struct {
	id uint64
	// last is the ID of the last command in its line: tmux skips the rest
	// of a line after an error, so they get no reply.
	last     uint64
	snapPane string               // take this pane's parser snapshot with the reply
	internal func(lines []string) // a reply the client handles itself
}

// maxAuthors is how many recorded authors a pane keeps.
const maxAuthors = 10000

func (c *ctlClient) broadcast(e ctlEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for s := range c.subs {
		s.push(e)
	}
}

// ctlHub keeps one client per session.
type ctlHub struct {
	mu      sync.Mutex
	clients map[string]*ctlClient
	run     tmuxFunc
	cmd     tmuxCmdFunc
	// restartAfter is how long the client waits before starting again, and
	// at most how long that wait grows to.
	restartAfter, restartMax time.Duration
	logf                     func(format string, args ...any)
	subBytes                 int // each subscriber's ctlSubBytes
}

func newCtlHub(run tmuxFunc, cmd tmuxCmdFunc) *ctlHub {
	return &ctlHub{clients: map[string]*ctlClient{}, run: run, cmd: cmd,
		restartAfter: 100 * time.Millisecond, restartMax: 5 * time.Second, logf: log.Printf, subBytes: ctlSubBytes}
}

// subscribe follows a session (which must exist, tabs.ensure) and returns
// the subscriber and a function that ends the subscription. It refuses a
// tmux older than minTmux, so the caller falls back.
func (h *ctlHub) subscribe(ctx context.Context, session string) (*ctlSub, func(), error) {
	if err := checkTmuxVersion(ctx, h.run); err != nil {
		return nil, nil, err
	}
	sub := &ctlSub{kick: make(chan struct{}, 1), maxBytes: h.subBytes}
	h.mu.Lock()
	c := h.clients[session]
	if c == nil {
		cctx, cancel := context.WithCancel(context.Background())
		c = &ctlClient{subs: map[*ctlSub]bool{}, cancel: cancel, done: make(chan struct{}), logf: h.logf}
		h.clients[session] = c
		go h.serve(cctx, session, c)
	}
	c.mu.Lock()
	c.subs[sub] = true
	c.mu.Unlock()
	h.mu.Unlock()
	stop := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		c.mu.Lock()
		delete(c.subs, sub)
		last := len(c.subs) == 0
		c.mu.Unlock()
		if last && h.clients[session] == c {
			delete(h.clients, session)
			c.cancel()
		}
	}
	return sub, stop, nil
}

// serve runs the session's client until ctx ends, starting it again when it
// exits.
func (h *ctlHub) serve(ctx context.Context, session string, c *ctlClient) {
	defer close(c.done)
	wait := h.restartAfter
	for started := false; ; started = true {
		began := time.Now()
		err := h.once(ctx, session, c, started)
		if ctx.Err() != nil {
			return
		}
		// The session is gone (its last tab closed, or the server ended):
		// stop instead of trying again every few seconds for good (R-0167).
		// Its subscribers are told; the next one starts a client again.
		if _, herr := h.run(ctx, "has-session", "-t", "="+session); herr != nil && ctx.Err() == nil {
			h.logf("portenv-agent: tmux session %s is gone; its control client stops", session)
			h.mu.Lock()
			if h.clients[session] == c {
				delete(h.clients, session)
			}
			h.mu.Unlock()
			c.broadcast(ctlEvent{kind: ctlSessionGone})
			return
		}
		if time.Since(began) > 10*time.Second {
			wait = h.restartAfter
		}
		h.logf("portenv-agent: tmux control client for %s ended (%v); starting it again in %s", session, err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, h.restartMax)
	}
}

// once runs the client until it exits or ctx ends. tmux's clipboard and
// passthrough stay off (tmuxSealed): asserted again at every start, since a
// program in the workspace can change them.
func (h *ctlHub) once(ctx context.Context, session string, c *ctlClient, restarted bool) error {
	if _, err := h.run(ctx, "set-option", "-g", "set-clipboard", "off", ";", "set-option", "-g", "allow-passthrough", "off"); err != nil {
		return err
	}
	cmd := h.cmd(ctx, "-C", "attach-session", "-t", "="+session)
	// Control mode detaches when its input closes: keep it open until ctx ends.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = stdin.Close() }()
	c.reset(stdin)
	defer c.reset(nil)
	// Past 10 s behind, tmux pauses a pane's output for this client instead
	// of queueing it (ADR 0020, decision 11); each pane's size and window
	// first, for its parser.
	_, _ = c.send("refresh-client -f pause-after=10", "", nil)
	_, _ = c.send("list-panes -s -F '#{window_id} #{pane_id} #{pane_width} #{pane_height}'", "", c.paneList)
	if restarted {
		c.broadcast(ctlEvent{kind: ctlRestart})
	} else {
		c.broadcast(ctlEvent{kind: ctlReady})
	}
	overflowed := false
	var reply *ctlEvent
	readErr := readControlLines(stdout, maxCtlLine, func(l []byte, err error) {
		if err != nil {
			if !overflowed {
				h.logf("portenv-agent: tmux control client for %s: %v; dropped", session, err)
				overflowed = true
			}
			c.restartParsers()
			c.broadcast(ctlEvent{kind: ctlOverflow})
			return
		}
		line := string(l)
		// A command's reply: %begin <time> <number> <flags>, its lines, then
		// %end or %error. Flags 1 marks a command this client sent; others
		// (the attach itself) aren't replies anyone waits for.
		if reply != nil {
			if strings.HasPrefix(line, "%end ") || strings.HasPrefix(line, "%error ") {
				reply.failed = strings.HasPrefix(line, "%error ")
				c.replied(*reply)
				reply = nil
			} else {
				reply.lines = append(reply.lines, line)
			}
			return
		}
		if strings.HasPrefix(line, "%begin ") {
			if f := strings.Fields(line); len(f) == 4 && f[3] == "1" {
				reply = &ctlEvent{kind: ctlReply}
			} else {
				reply = &ctlEvent{kind: ctlOther}
			}
			return
		}
		e := parseControlLine(line)
		switch e.kind {
		case ctlOther:
			return
		case ctlOutput:
			e.marks, e.authors, e.lost = c.feed(e.pane, e.data)
		case ctlLayout:
			c.layout(e)
		case ctlPause:
			// Output was skipped: let it come again, and start the pane's
			// parser afresh (its screen no longer matches).
			_, _ = c.send("refresh-client -A '"+e.pane+":continue'", "", nil)
			c.restartParser(e.pane)
		case ctlWindowClose:
			c.closeWindow(e.window)
		}
		c.broadcast(e)
	})
	waitErr := cmd.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	return waitErr
}

// reset starts a run of the client with its input, or ends it (nil). The
// parsers start empty with each run: output between two runs was never
// seen.
func (c *ctlClient) reset(stdin io.Writer) {
	c.wmu.Lock()
	c.stdin, c.pending = stdin, nil
	c.wmu.Unlock()
	c.pmu.Lock()
	for _, p := range c.parsers {
		p.stop()
	}
	c.parsers, c.sizes, c.windows = map[string]*paneParser{}, map[string][2]int{}, map[string]string{}
	c.enter, c.authors = map[string]recordedAuthor{}, map[string][]recordedAuthor{}
	c.pmu.Unlock()
}

var errNoClient = errors.New("the tmux control client isn't running")

// send writes a command line to the client. Each command in it (separated by
// " ; ") gets its own reply, in order; the last one's comes as a ctlReply
// with the returned ID, with a snapshot of snapPane's parser when asked.
// internal handles the reply in the client instead.
func (c *ctlClient) send(line, snapPane string, internal func([]string)) (uint64, error) {
	if strings.ContainsAny(line, "\n\r") {
		return 0, errors.New("a control-mode command can't hold a line break")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.stdin == nil {
		return 0, errNoClient
	}
	n := strings.Count(line, " ; ") + 1
	var id uint64
	last := c.nextID + uint64(n) // #nosec G115 -- a small count
	for i := range n {
		c.nextID++
		id = c.nextID
		p := ctlPending{id: id, last: last}
		if i == n-1 {
			p.snapPane, p.internal = snapPane, internal
		}
		c.pending = append(c.pending, p)
	}
	if _, err := io.WriteString(c.stdin, line+"\n"); err != nil {
		c.pending = c.pending[:len(c.pending)-n]
		return 0, err
	}
	return id, nil
}

// replied matches a reply to the oldest command waiting for one.
func (c *ctlClient) replied(e ctlEvent) {
	if e.kind != ctlReply {
		return
	}
	c.wmu.Lock()
	if len(c.pending) == 0 {
		c.wmu.Unlock()
		return
	}
	p := c.pending[0]
	c.pending = c.pending[1:]
	// After an error, the line's later commands never run: each is
	// answered here as failed, so the queue stays matched to tmux's replies.
	var skipped []ctlPending
	for e.failed && len(c.pending) > 0 && c.pending[0].id <= p.last {
		skipped = append(skipped, c.pending[0])
		c.pending = c.pending[1:]
	}
	c.wmu.Unlock()
	c.answer(p, e)
	for _, q := range skipped {
		c.answer(q, ctlEvent{kind: ctlReply, failed: true, lines: []string{"not run: an earlier command in its line failed"}})
	}
}

// answer hands a reply to whoever waits for it.
func (c *ctlClient) answer(p ctlPending, e ctlEvent) {
	if p.internal != nil {
		p.internal(e.lines)
		return
	}
	e.id = p.id
	if p.snapPane != "" {
		c.pmu.Lock()
		if pp := c.parsers[p.snapPane]; pp != nil {
			e.snap = pp.snapshot()
		}
		e.authors = append([]recordedAuthor(nil), c.authors[p.snapPane]...)
		c.pmu.Unlock()
	}
	c.broadcast(e)
}

// paneList takes the reply to list-panes: each pane's window and size.
func (c *ctlClient) paneList(lines []string) {
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 4 {
			continue
		}
		cols, _ := strconv.Atoi(f[2])
		rows, _ := strconv.Atoi(f[3])
		c.layout(ctlEvent{window: f[0], pane: f[1], cols: cols, rows: rows})
	}
}

// layout records a pane's window and size, and resizes its parser.
func (c *ctlClient) layout(e ctlEvent) {
	if e.pane == "" || e.cols <= 0 || e.rows <= 0 {
		return
	}
	c.pmu.Lock()
	defer c.pmu.Unlock()
	c.windows[e.window] = e.pane
	c.sizes[e.pane] = [2]int{e.cols, e.rows}
	if p := c.parsers[e.pane]; p != nil {
		p.resize(e.cols, e.rows)
	}
}

// closeWindow drops a closed window's pane: its parser, size and authors.
func (c *ctlClient) closeWindow(window string) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	pane := c.windows[window]
	delete(c.windows, window)
	if pane == "" {
		return
	}
	if p := c.parsers[pane]; p != nil {
		p.stop()
	}
	delete(c.parsers, pane)
	delete(c.sizes, pane)
	delete(c.enter, pane)
	delete(c.authors, pane)
}

// feed runs a pane's output through its parser (made on first output, at
// the pane's size if known) and records who started each command: the
// author of the latest Enter in that pane, if any (ADR 0020, decision 4).
func (c *ctlClient) feed(pane string, data []byte) ([]paneMark, []recordedAuthor, bool) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	p := c.parsers[pane]
	if p == nil {
		size, ok := c.sizes[pane]
		if !ok {
			size = [2]int{80, 24}
		}
		p = newPaneParser(size[0], size[1], c.logf)
		c.parsers[pane] = p
	}
	marks, lost := p.feed(data)
	if lost {
		delete(c.authors, pane)
		delete(c.enter, pane)
	}
	var started []recordedAuthor
	for _, m := range marks {
		if m.kind != markCommand {
			continue
		}
		a, ok := c.enter[pane]
		if !ok {
			continue // no recorded author: no message at all
		}
		delete(c.enter, pane)
		a.line, a.epoch = m.line, m.epoch
		started = append(started, a)
		rs := append(c.authors[pane], a)
		if over := len(rs) - maxAuthors; over > 0 {
			rs = append(rs[:0], rs[over:]...)
		}
		c.authors[pane] = rs
	}
	return marks, started, lost
}

// typed records who sent an Enter in a pane, for the next command that
// starts there. An Enter with no author clears it.
func (c *ctlClient) typed(pane, name, agentID string) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	if name == "" {
		delete(c.enter, pane)
		return
	}
	c.enter[pane] = recordedAuthor{name: name, agentID: agentID}
}

// restartParser starts one pane's parser afresh (a pause skipped output).
func (c *ctlClient) restartParser(pane string) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	if p := c.parsers[pane]; p != nil {
		p.restart()
	}
	delete(c.authors, pane)
	delete(c.enter, pane)
}

// restartParsers starts every pane's parser afresh (a line was dropped).
func (c *ctlClient) restartParsers() {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	for _, p := range c.parsers {
		p.restart()
	}
	c.authors, c.enter = map[string][]recordedAuthor{}, map[string]recordedAuthor{}
}

// client returns a session's running client, if any.
func (h *ctlHub) client(session string) *ctlClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[session]
}
