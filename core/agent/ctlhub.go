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
	"sync"
	"time"
)

// The helper's control-mode clients (ADR 0020, decision 2): one `tmux -C`
// client per session, shared by everything that follows the session (the tab
// watch now; the waiting detector and the app's tab terminals in 1.2c's next
// PRs). It starts with the first subscriber and ends with the last, runs in a
// UTF-8 locale (envFor), and is started again if it exits; subscribers then
// get ctlRestart, since events in between were never seen.

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

// ctlSubCap is how many events a subscriber may fall behind by. Past it, its
// queue is dropped and it gets ctlOverflow instead, so a slow subscriber
// never blocks the client or grows memory.
const ctlSubCap = 4096

// ctlSub is one subscriber's queue of events.
type ctlSub struct {
	mu       sync.Mutex
	q        []ctlEvent
	overflow bool
	kick     chan struct{}
}

func (s *ctlSub) push(e ctlEvent) {
	s.mu.Lock()
	if len(s.q) >= ctlSubCap {
		s.q, s.overflow = nil, true
	} else if !s.overflow {
		s.q = append(s.q, e)
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
	if s.overflow {
		s.q, s.overflow = nil, false
		return []ctlEvent{{kind: ctlOverflow}}
	}
	q := s.q
	s.q = nil
	return q
}

// ctlClient is one session's control-mode client and its subscribers.
type ctlClient struct {
	mu     sync.Mutex
	subs   map[*ctlSub]bool
	cancel context.CancelFunc
	done   chan struct{}
}

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
}

func newCtlHub(run tmuxFunc, cmd tmuxCmdFunc) *ctlHub {
	return &ctlHub{clients: map[string]*ctlClient{}, run: run, cmd: cmd,
		restartAfter: 100 * time.Millisecond, restartMax: 5 * time.Second, logf: log.Printf}
}

// subscribe follows a session (which must exist, tabs.ensure) and returns
// the subscriber and a function that ends the subscription. It refuses a
// tmux older than minTmux, so the caller falls back.
func (h *ctlHub) subscribe(ctx context.Context, session string) (*ctlSub, func(), error) {
	if err := checkTmuxVersion(ctx, h.run); err != nil {
		return nil, nil, err
	}
	sub := &ctlSub{kick: make(chan struct{}, 1)}
	h.mu.Lock()
	c := h.clients[session]
	if c == nil {
		cctx, cancel := context.WithCancel(context.Background())
		c = &ctlClient{subs: map[*ctlSub]bool{}, cancel: cancel, done: make(chan struct{})}
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
		if started {
			c.broadcast(ctlEvent{kind: ctlRestart})
		}
		began := time.Now()
		err := h.once(ctx, session, c)
		if ctx.Err() != nil {
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
func (h *ctlHub) once(ctx context.Context, session string, c *ctlClient) error {
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
	overflowed := false
	readErr := readControlLines(stdout, maxCtlLine, func(l []byte, err error) {
		if err != nil {
			if !overflowed {
				h.logf("portenv-agent: tmux control client for %s: %v; dropped", session, err)
				overflowed = true
			}
			c.broadcast(ctlEvent{kind: ctlOverflow})
			return
		}
		if e := parseControlLine(string(l)); e.kind != ctlOther {
			c.broadcast(e)
		}
	})
	waitErr := cmd.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	return waitErr
}
