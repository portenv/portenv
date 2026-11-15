// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// A workspace's team across a box restart (CC-0747): the Team sidebar
// shows what really runs in the box, never the last state recorded, and
// a team that was running when the workspace closed starts again when it
// opens, through the same start as Start Team and only under approvals
// the owner has already given.

// teamRunningFile is portenvd's record, in a workspace's state folder, of
// the agents the owner's starts left running.
const teamRunningFile = "team-running.json"

// boxCheckTimeout bounds boxStarted's look at the box, so an open never
// waits long on it.
const boxCheckTimeout = 15 * time.Second

// notRestartedLine is an agent's line when it was running as the
// workspace closed but the board holds no approved first start, so
// Portenv doesn't start it on its own.
const notRestartedLine = "It was running when this workspace closed. Click Start Team to start it again; Portenv asks you first."

type teamRunning struct {
	Running []string `json:"running"`
}

func (e *teamEngine) wantedPath(name string) string {
	if e.wantedFile == nil {
		return ""
	}
	return e.wantedFile(name)
}

// wantedLocked is the agents the owner's starts left running, read from
// portenvd's record on first use.
func (e *teamEngine) wantedLocked(name string, b *teamBox) map[string]bool {
	if b.wantedLoaded {
		return b.wanted
	}
	b.wantedLoaded, b.wanted = true, map[string]bool{}
	p := e.wantedPath(name)
	if p == "" {
		return b.wanted
	}
	data, err := os.ReadFile(p) // #nosec G304 -- portenvd's own state folder
	if err != nil {
		if !os.IsNotExist(err) {
			e.logWarn("read which agents were running", name, err)
		}
		return b.wanted
	}
	var r teamRunning
	if err := json.Unmarshal(data, &r); err != nil {
		e.logWarn("read which agents were running", name, err)
		return b.wanted
	}
	for _, id := range r.Running {
		b.wanted[id] = true
	}
	return b.wanted
}

// setWantedLocked records that the owner's start left an agent running
// (on) or the owner stopped it.
func (e *teamEngine) setWantedLocked(name string, b *teamBox, agentID string, on bool) {
	w := e.wantedLocked(name, b)
	if w[agentID] == on {
		return
	}
	if on {
		w[agentID] = true
	} else {
		delete(w, agentID)
	}
	p := e.wantedPath(name)
	if p == "" {
		return
	}
	ids := make([]string, 0, len(w))
	for id := range w {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if err := writePrivateFile(p, teamRunning{Running: ids}); err != nil {
		e.logWarn("record which agents are running", name, err)
	}
}

// writePrivateFile writes v as JSON to p, owner-only, by a rename.
func writePrivateFile(p string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (e *teamEngine) logWarn(what, box string, err error) {
	if e.log != nil {
		e.log.Warn(what, "box", box, "err", err)
	}
}

// liveAgents is each agent's tabs as the box's helper lists them now: the
// agents whose terminals really run. A helper that can't say lists none
// (a box that just started runs no agent).
func (e *teamEngine) liveAgents(ctx context.Context, name string) map[string][]*typesv1.Tab {
	live := map[string][]*typesv1.Tab{}
	h, err := e.helper(ctx, name)
	if err != nil {
		e.logWarn("list the team's tabs after the workspace started", name, err)
		return live
	}
	r, err := h.ListTabs(ctx, &agentv1.ListTabsRequest{Session: defaultSession})
	if err != nil {
		e.logWarn("list the team's tabs after the workspace started", name, err)
		return live
	}
	for _, t := range r.GetTabs() {
		if id := t.GetAgentId(); id != "" {
			live[id] = append(live[id], t)
		}
	}
	return live
}

// boxStarted brings a workspace's team to the box's real state once the
// box has started, or been taken over still running: an agent whose
// terminal runs shows as running with its tabs, and every other agent as
// stopped, whatever was recorded before. Then the agents the owner's
// starts left running start again through startAgentAs, the path Start
// Team takes, but only when the board holds the team's approved first
// start: without it nothing starts and nothing is asked, and each shows
// as stopped with a line saying how to start it. It says whether any
// agent runs or is starting.
func (e *teamEngine) boxStarted(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, boxCheckTimeout)
	defer cancel()
	live := e.liveAgents(ctx, name)

	// Nothing ran, runs, or was left running: nothing to read or say.
	b := e.box(name)
	b.mu.Lock()
	quiet := len(live) == 0 && b.activeLocked("") == 0 && len(e.wantedLocked(name, b)) == 0
	b.mu.Unlock()
	if quiet {
		return false
	}

	team, err := e.loadTeam(ctx, name)
	if err != nil {
		e.logWarn("read the team after the workspace started", name, err)
	}
	b, err = e.lockBox(ctx, name)
	if err != nil {
		// The board can't be read: say what runs, start nothing.
		e.logWarn("read the board after the workspace started", name, err)
		b = e.box(name)
		b.mu.Lock()
		e.reconcileLocked(b, &team, live)
		b.mu.Unlock()
		return len(live) > 0
	}
	defer b.mu.Unlock()
	e.reconcileLocked(b, &team, live)

	var again []string
	for id := range e.wantedLocked(name, b) {
		if a := team.agent(id); a != nil && !a.Remote && live[id] == nil {
			again = append(again, id)
		}
	}
	slices.Sort(again)
	if len(again) == 0 {
		return len(live) > 0
	}
	if !b.teamStartApprovedLocked() {
		for _, id := range again {
			b.startErr[id] = notRestartedLine
			b.events = append(b.events, teamEvent{agent: e.infoLocked(b, team.agent(id))})
		}
		b.changedLocked()
		return len(live) > 0
	}
	starting := e.startInBackgroundLocked(ctx, name, b, &team, again, "")
	return len(live) > 0 || len(starting) > 0
}

// reconcileLocked sets each agent's state from what runs in the box.
func (e *teamEngine) reconcileLocked(b *teamBox, team *Team, live map[string][]*typesv1.Tab) {
	ids := map[string]bool{}
	for id := range b.states {
		ids[id] = true
	}
	for id := range live {
		ids[id] = true
	}
	changed := false
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		a := team.agent(id)
		running := b.states[id] == typesv1.AgentState_AGENT_STATE_RUNNING || b.states[id] == typesv1.AgentState_AGENT_STATE_STARTING
		switch tabs := live[id]; {
		case tabs != nil && a != nil && b.states[id] == typesv1.AgentState_AGENT_STATE_RUNNING:
			// Already shown running: its tabs as they are now; its
			// routines keep their times and pauses.
			b.tabs[id], b.panes[id] = nil, nil
			for _, t := range tabs {
				b.tabs[id] = append(b.tabs[id], t.GetId())
				b.panes[id] = append(b.panes[id], t.GetPaneIds()...)
			}
		case tabs != nil && a != nil:
			e.runningLocked(b, a, tabs)
		case tabs != nil:
			// Runs, but its file is gone: shown nowhere, kept as running.
			b.states[id] = typesv1.AgentState_AGENT_STATE_RUNNING
		case running:
			b.stoppedLocked(id)
		default:
			continue
		}
		changed = true
		if a != nil {
			b.events = append(b.events, teamEvent{agent: e.infoLocked(b, a)})
		}
	}
	if changed {
		b.changedLocked()
	}
}
