// SPDX-License-Identifier: Apache-2.0

package agent

import "sync"

// The tmux reader never waits on a parser or a stream (R-0178): it decodes
// each line and hands a pane's events to that pane's lane, a goroutine that
// parses its output and broadcasts its events in order. A lane holds at
// most laneBytes of output waiting; past that, its waiting output is
// dropped, its parser starts afresh, and its tab terminals reseed with
// skipped set (ADR 0020, decision 11), so one pane's flood never stalls
// the others or gets the control client dropped by tmux.

// paneLaneBytes is how much of a pane's output may wait for its parser.
const paneLaneBytes = 4 << 20

// laneItem is one thing a lane does, in order: output to parse (and its
// size, which counts against the lane's limit) or another of the pane's
// events (a layout, a pause, a seed's reply, its window closing).
type laneItem struct {
	output bool
	size   int
	run    func()
	// last ends the lane after it runs (the pane's window closed).
	last bool
}

type paneLane struct {
	pane    string
	max     int
	mu      sync.Mutex
	q       []laneItem
	qBytes  int
	dropped bool // output was dropped: restart the parser first
	quiet   bool // ... and don't announce it (the caller does)
	kick    chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

// push queues an item. Output past the lane's limit drops all the output
// waiting, this item's too.
func (l *paneLane) push(it laneItem) {
	l.mu.Lock()
	if it.output && l.qBytes+it.size > l.max {
		l.dropLocked(false)
	} else {
		l.q = append(l.q, it)
		if it.output {
			l.qBytes += it.size
		}
	}
	l.mu.Unlock()
	l.wake()
}

// drop drops the output waiting and restarts the parser before anything
// else; quiet leaves telling the subscribers to the caller.
func (l *paneLane) drop(quiet bool) {
	l.mu.Lock()
	l.dropLocked(quiet)
	l.mu.Unlock()
	l.wake()
}

func (l *paneLane) dropLocked(quiet bool) {
	keep := make([]laneItem, 0, len(l.q))
	for _, q := range l.q {
		if !q.output {
			keep = append(keep, q)
		}
	}
	if !l.dropped {
		l.quiet = quiet
	} else {
		l.quiet = l.quiet && quiet
	}
	l.q, l.qBytes, l.dropped = keep, 0, true
}

func (l *paneLane) wake() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// lane returns a pane's lane, starting it on first use.
func (c *ctlClient) lane(pane string) *paneLane {
	c.lmu.Lock()
	defer c.lmu.Unlock()
	if l := c.lanes[pane]; l != nil {
		return l
	}
	l := &paneLane{pane: pane, max: c.laneMax, kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	c.lanes[pane] = l
	go c.runLane(l)
	return l
}

func (c *ctlClient) runLane(l *paneLane) {
	defer close(l.done)
	for {
		select {
		case <-l.stop:
			return
		case <-l.kick:
		}
		// One item at a time, so a drop while it parses also drops the
		// output still waiting behind it.
		for {
			l.mu.Lock()
			dropped, quiet := l.dropped, l.quiet
			l.dropped, l.quiet = false, false
			var it laneItem
			ok := len(l.q) > 0
			if ok {
				it = l.q[0]
				l.q = l.q[1:]
				if it.output {
					l.qBytes -= it.size
				}
			}
			l.mu.Unlock()
			if dropped {
				c.restartParser(l.pane)
				if !quiet {
					c.broadcast(ctlEvent{kind: ctlPause, pane: l.pane})
				}
			}
			if !ok {
				break
			}
			it.run()
			if it.last {
				c.lmu.Lock()
				if c.lanes[l.pane] == l {
					delete(c.lanes, l.pane)
				}
				c.lmu.Unlock()
				return
			}
			select {
			case <-l.stop:
				return
			default:
			}
		}
	}
}

// stopLanes ends every lane and waits for them, so nothing uses a parser
// after it.
func (c *ctlClient) stopLanes() {
	c.lmu.Lock()
	lanes := c.lanes
	c.lanes = map[string]*paneLane{}
	c.lmu.Unlock()
	for _, l := range lanes {
		close(l.stop)
	}
	for _, l := range lanes {
		<-l.done
	}
}

// dropAll drops every lane's waiting output quietly (a line was lost: the
// caller broadcasts ctlOverflow).
func (c *ctlClient) dropAll() {
	c.lmu.Lock()
	defer c.lmu.Unlock()
	for _, l := range c.lanes {
		l.drop(true)
	}
}
