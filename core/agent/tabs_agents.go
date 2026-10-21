// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Each agent's own tmux server (ADR 0022 §1): started as the agent's user,
// on a socket only that user reaches, with a control client of its own.
// Raw tmux from an agent reaches only this server; the owner's window shows
// its tabs, labelled with the agent, through these functions alone.

// tmuxPaneRE is a tmux pane id.
var tmuxPaneRE = regexp.MustCompile(`^%[0-9]{1,9}$`)

// agentSession is the session on an agent's server.
const agentSession = "main"

// agentServer is one running agent's tmux, as the agent, and its hub.
type agentServer struct {
	user AgentServerSpec
	tmux tmuxFunc
	hub  *ctlHub
	// mainPane is the runner's pane, the first in its main session: the
	// only pane a routine is typed into.
	mainPane string
	// idleInput matches the runner's screen when it waits at an empty
	// input (SetAgentIdleInput); nil refuses every routine.
	idleInput *regexp.Regexp
}

// agentServers are the running agents' servers, by id.
type agentServers struct {
	mu sync.Mutex
	by map[string]*agentServer
	// sel is the agent tab a viewer of the owner's window has chosen, by
	// session and viewer (viewKey): the agent's server can't hold the
	// owner's choice, as the owner's own tmux session holds it for the
	// owner's tabs.
	sel map[string]string
	// ch is closed when an agent's tabs may have changed (changed).
	ch chan struct{}
}

// changed is closed at the next start, stop or pane action. Changes an
// agent makes with its own tmux reach a watch at its next safety list.
func (a *agentServers) changed() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ch == nil {
		a.ch = make(chan struct{})
	}
	return a.ch
}

// touchLocked closes ch; a.mu is held.
func (a *agentServers) touchLocked() {
	if a.ch != nil {
		close(a.ch)
		a.ch = nil
	}
}

func (a *agentServers) touch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.touchLocked()
}

// viewKey is a viewer of a session.
func viewKey(session, viewer string) string { return session + "\x00" + viewer }

// mergeAgents adds the running agents' tabs, by agent id, after the
// owner's. The viewer's chosen agent tab is the active one; when it's gone
// the owner's current tab is again.
func (s *apiServer) mergeAgents(key string) func(ctx context.Context, own []*typesv1.Tab) []*typesv1.Tab {
	return func(ctx context.Context, own []*typesv1.Tab) []*typesv1.Tab {
		s.agents.mu.Lock()
		servers := make([]*agentServer, 0, len(s.agents.by))
		for _, a := range s.agents.by {
			servers = append(servers, a)
		}
		sel := s.agents.sel[key]
		s.agents.mu.Unlock()
		slices.SortFunc(servers, func(x, y *agentServer) int { return strings.Compare(x.user.ID, y.user.ID) })
		out, found := own, false
		for _, a := range servers {
			ts, err := a.tabs(ctx)
			if err != nil {
				continue // stopping
			}
			for _, t := range ts {
				t.Active = t.GetId() == sel
				found = found || t.Active
			}
			out = append(out, ts...)
		}
		if found {
			for _, t := range own {
				t.Active = false
			}
		} else if sel != "" {
			s.agents.mu.Lock()
			if s.agents.sel[key] == sel {
				delete(s.agents.sel, key)
			}
			s.agents.mu.Unlock()
		}
		return out
	}
}

// agentTab finds an agent's tab from an app tab id ("builder:@3"): its
// server and tmux window id. ok is false for the owner's tabs.
func (s *apiServer) agentTab(ctx context.Context, id string) (a *agentServer, window string, ok bool, err error) {
	agent, window, valid := SplitTabID(id)
	if agent == "" {
		return nil, "", false, nil
	}
	if !valid {
		return nil, "", true, status.Error(codes.InvalidArgument, "an agent's tab ids look like builder:@3")
	}
	a, running := s.agents.get(agent)
	if !running {
		return nil, "", true, status.Errorf(codes.NotFound, "%s isn't running", agent)
	}
	ts, err := a.tabs(ctx)
	if err != nil {
		return nil, "", true, err
	}
	if !slices.ContainsFunc(ts, func(t *typesv1.Tab) bool { return t.GetId() == id }) {
		return nil, "", true, status.Errorf(codes.NotFound, "%s has no tab %s", agent, window)
	}
	return a, window, true, nil
}

// selectAgentTab makes an agent's tab the viewer's chosen one, or (id
// empty) goes back to the owner's.
func (s *apiServer) selectAgentTab(key, id string) {
	s.agents.mu.Lock()
	defer s.agents.mu.Unlock()
	if id == "" {
		if _, ok := s.agents.sel[key]; !ok {
			return
		}
		delete(s.agents.sel, key)
	} else {
		if s.agents.sel == nil {
			s.agents.sel = map[string]string{}
		}
		s.agents.sel[key] = id
	}
	s.agents.touchLocked()
}

// closeAgentTab closes an agent's tab, unless it's the agent's last: that
// one closes when the agent stops.
func (a *agentServer) closeTab(ctx context.Context, window string) error {
	ts, err := a.tabs(ctx)
	if err != nil {
		return err
	}
	if len(ts) == 1 {
		return status.Errorf(codes.FailedPrecondition, "%s's last tab closes when %s stops", a.user.ID, a.user.ID)
	}
	if _, err := a.tmux(ctx, "kill-window", "-t", window); err != nil {
		return status.Errorf(codes.Unavailable, "close tab: %v", err)
	}
	return nil
}

// renameTab renames an agent's tab.
func (a *agentServer) renameTab(ctx context.Context, window, name string) error {
	if err := checkTabName(name); err != nil {
		return err
	}
	escaped, err := tmuxName(name)
	if err != nil {
		return err
	}
	if _, err := a.tmux(ctx, "rename-window", "-t", window, "--", escaped); err != nil {
		return status.Errorf(codes.Unavailable, "rename tab: %v", err)
	}
	return nil
}

func (a *agentServers) get(id string) (*agentServer, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.by[id]
	return s, ok
}

// AgentTabID is an agent's tab id as the app sees it: unique across
// servers, the agent's id before tmux's window id ("builder:@3").
func AgentTabID(agent, window string) string { return agent + ":" + window }

// SplitTabID splits an app tab id into the agent (empty for the owner's)
// and tmux's window id. It reports false for anything else.
func SplitTabID(id string) (agent, window string, ok bool) {
	agent, window, found := strings.Cut(id, ":")
	if !found {
		return "", id, windowIDRE.MatchString(id)
	}
	return agent, window, agentIDRE.MatchString(agent) && windowIDRE.MatchString(window)
}

// checkArgv refuses an empty program or an argument with a control
// character, and returns the argv escaped for tmux (tmuxArgv).
func checkArgv(argv []string) ([]string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, status.Error(codes.InvalidArgument, "a program to run is needed")
	}
	for _, a := range argv {
		if strings.IndexFunc(a, isControl) >= 0 {
			return nil, status.Error(codes.InvalidArgument, "a program's arguments can't hold control characters")
		}
	}
	return tmuxArgv(argv)
}

// agentTmuxFunc makes an agent's tmux calls and control client: in the
// box, through the helper's switch to the agent's user (agentAs); in
// tests, plain tmux on the spec's socket.
type agentTmuxFunc func(spec AgentServerSpec) (tmuxFunc, tmuxCmdFunc, error)

// tmuxArg escapes one argument for a tmux command line. tmux ends a
// command at an argument ending in ";", so a program's "a;" would start a
// tmux command of the caller's choosing; "a\;" is tmux's escape for it.
// What tmux makes of backslashes before a final ";" differs between
// versions, so an argument ending in "\;" is refused.
func tmuxArg(a string) (string, error) {
	if strings.HasSuffix(a, `\;`) {
		return "", status.Error(codes.InvalidArgument, `an argument can't end in \;`)
	}
	if strings.HasSuffix(a, ";") {
		a = a[:len(a)-1] + `\;`
	}
	return a, nil
}

// tmuxArgv is tmuxArg over a program and its arguments.
func tmuxArgv(argv []string) ([]string, error) {
	out := make([]string, len(argv))
	for i, a := range argv {
		e, err := tmuxArg(a)
		if err != nil {
			return nil, err
		}
		out[i] = e
	}
	return out, nil
}

// tmuxName escapes a tab's name: tmux expands formats in a new window's
// name ("#{pane_id}", "#(command)"), and "##" is a plain "#".
func tmuxName(name string) (string, error) {
	return tmuxArg(strings.ReplaceAll(name, "#", "##"))
}

// StartAgentTerminals starts an agent's tmux server as its user, with the
// runner in its first tab, and its control client: the start function
// lane C's StartTeamAgent calls (startAgentServer). It returns the
// agent's tabs. Starting a running agent again returns its tabs as they
// are.
func (s *apiServer) StartAgentTerminals(ctx context.Context, spec AgentServerSpec) ([]*typesv1.Tab, error) {
	if !agentIDRE.MatchString(spec.ID) {
		return nil, status.Errorf(codes.InvalidArgument, "%q isn't a valid agent id", spec.ID)
	}
	argv, err := checkArgv(spec.Argv)
	if err != nil {
		return nil, err
	}
	if s.agentTmux == nil {
		return nil, status.Error(codes.FailedPrecondition, "agents' terminals aren't available here")
	}
	// Held through the start, so two starts of one agent make one server.
	s.agents.mu.Lock()
	defer s.agents.mu.Unlock()
	if a, ok := s.agents.by[spec.ID]; ok {
		return a.tabs(ctx)
	}
	run, cmd, err := s.agentTmux(spec)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%s's terminal: %v", spec.ID, err)
	}
	// The session, sealed as the owner's is (tmuxHidden, tmuxOwned,
	// tmuxSealed), with the runner in its first tab, run with no shell.
	args := []string{"new-session", "-d", "-s", agentSession, "-n", spec.ID, "-P", "-F", "#{pane_id}"}
	if spec.WorkDir != "" {
		args = append(args, "-c", spec.WorkDir)
	}
	args = append(append(args, "--"), argv...)
	args = append(args, tmuxHidden...)
	args = append(args, tmuxOwned...)
	args = append(args, tmuxSealed...)
	out, err := run(ctx, args...)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%s's terminal couldn't start: %v", spec.ID, err)
	}
	mainPane := strings.TrimSpace(out)
	if !tmuxPaneRE.MatchString(mainPane) {
		_, _ = run(ctx, "kill-server")
		return nil, status.Errorf(codes.Internal, "%s's terminal: tmux answered %q", spec.ID, mainPane)
	}
	a := &agentServer{user: spec, tmux: run, hub: newCtlHub(run, cmd), mainPane: mainPane}
	if s.agents.by == nil {
		s.agents.by = map[string]*agentServer{}
	}
	s.agents.by[spec.ID] = a
	s.agents.touchLocked()
	return a.tabs(ctx)
}

// StopAgentTerminals ends an agent's tmux server, and its control client
// with it: the stop function lane C's StopTeamAgent calls
// (stopAgentServer) before it ends the rest of the agent's processes.
// Stopping an agent that isn't running is no error.
func (s *apiServer) StopAgentTerminals(ctx context.Context, id string) error {
	s.agents.mu.Lock()
	a, ok := s.agents.by[id]
	delete(s.agents.by, id)
	s.agents.touchLocked()
	s.agents.mu.Unlock()
	if !ok {
		return nil
	}
	if _, err := a.tmux(ctx, "kill-server"); err != nil && !strings.Contains(err.Error(), "no server running") {
		return status.Errorf(codes.Unavailable, "%s's terminal didn't stop: %v", id, err)
	}
	return nil
}

// tabs lists the agent's tabs, ids prefixed, with agent_id and pane_ids.
func (a *agentServer) tabs(ctx context.Context) ([]*typesv1.Tab, error) {
	out, err := a.tmux(ctx, "list-panes", "-s", "-t", "="+agentSession, "-F", "#{window_id}\t#{pane_id}\t#{window_name}\t#{window_active}")
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%s's tabs: %v", a.user.ID, err)
	}
	var tabs []*typesv1.Tab
	byWindow := map[string]*typesv1.Tab{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 || !windowIDRE.MatchString(f[0]) || !tmuxPaneRE.MatchString(f[1]) {
			continue
		}
		t := byWindow[f[0]]
		if t == nil {
			t = &typesv1.Tab{Id: AgentTabID(a.user.ID, f[0]), Name: f[2], Active: f[3] == "1", AgentId: a.user.ID}
			byWindow[f[0]] = t
			tabs = append(tabs, t)
		}
		t.PaneIds = append(t.PaneIds, f[1])
	}
	return tabs, nil
}

// AgentPaneAction opens, focuses or closes a pane on the agent's own
// server, as that agent (portenvd calls it after its grant check). It
// returns the pane made or acted on.
func (s *apiServer) AgentPaneAction(ctx context.Context, req *agentv1.AgentPaneActionRequest) (*agentv1.AgentPaneActionResponse, error) {
	a, ok := s.agents.get(req.GetAgentId())
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "%s isn't running", req.GetAgentId())
	}
	var pane *agentv1.PaneRef
	var err error
	switch act := req.GetAction().(type) {
	case *agentv1.AgentPaneActionRequest_Split:
		pane, err = a.split(ctx, act.Split)
	case *agentv1.AgentPaneActionRequest_Tab:
		pane, err = a.newTab(ctx, act.Tab)
	case *agentv1.AgentPaneActionRequest_Focus:
		pane, err = a.focus(ctx, act.Focus)
	case *agentv1.AgentPaneActionRequest_Close:
		pane, err = a.closePane(ctx, act.Close)
	default:
		return nil, status.Error(codes.InvalidArgument, "no pane action")
	}
	if err != nil {
		return nil, err
	}
	s.agents.touch()
	return &agentv1.AgentPaneActionResponse{Pane: pane}, nil
}

// made reads tmux's "#{window_id} #{pane_id}" for a pane just made.
func (a *agentServer) made(out string) (*agentv1.PaneRef, error) {
	f := strings.Fields(out)
	if len(f) != 2 || !windowIDRE.MatchString(f[0]) || !tmuxPaneRE.MatchString(f[1]) {
		return nil, status.Errorf(codes.Internal, "tmux answered %q", strings.TrimSpace(out))
	}
	return &agentv1.PaneRef{PaneId: f[1], TabId: AgentTabID(a.user.ID, f[0])}, nil
}

// ownPane checks a pane is on this agent's server, and returns its tab.
func (a *agentServer) ownPane(ctx context.Context, pane string) (*agentv1.PaneRef, error) {
	if !tmuxPaneRE.MatchString(pane) {
		return nil, status.Error(codes.InvalidArgument, "a pane id is % and a number")
	}
	out, err := a.tmux(ctx, "list-panes", "-s", "-t", "="+agentSession, "-F", "#{window_id} #{pane_id}")
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%s's panes: %v", a.user.ID, err)
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == pane {
			return &agentv1.PaneRef{PaneId: pane, TabId: AgentTabID(a.user.ID, f[0])}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "%s has no pane %s", a.user.ID, pane)
}

func (a *agentServer) split(ctx context.Context, sp *agentv1.Split) (*agentv1.PaneRef, error) {
	argv, err := checkArgv(sp.GetArgv())
	if err != nil {
		return nil, err
	}
	if _, err := a.ownPane(ctx, sp.GetPaneId()); err != nil {
		return nil, err
	}
	dir := "-h" // right
	if sp.GetDown() {
		dir = "-v"
	}
	args := append([]string{"split-window", dir, "-d", "-t", sp.GetPaneId(), "-P", "-F", "#{window_id} #{pane_id}", "--"}, argv...)
	out, err := a.tmux(ctx, args...)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "split: %v", err)
	}
	return a.made(out)
}

func (a *agentServer) newTab(ctx context.Context, t *agentv1.NewTeamTab) (*agentv1.PaneRef, error) {
	argv, err := checkArgv(t.GetArgv())
	if err != nil {
		return nil, err
	}
	name := t.GetName()
	if name == "" {
		name = a.user.ID
	}
	if err := checkTabName(name); err != nil {
		return nil, err
	}
	if name, err = tmuxName(name); err != nil {
		return nil, err
	}
	args := append([]string{"new-window", "-d", "-t", "=" + agentSession + ":", "-n", name, "-P", "-F", "#{window_id} #{pane_id}", "--"}, argv...)
	out, err := a.tmux(ctx, args...)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "new tab: %v", err)
	}
	return a.made(out)
}

func (a *agentServer) focus(ctx context.Context, p *agentv1.PaneRef) (*agentv1.PaneRef, error) {
	ref, err := a.ownPane(ctx, p.GetPaneId())
	if err != nil {
		return nil, err
	}
	_, window, _ := SplitTabID(ref.GetTabId())
	if _, err := a.tmux(ctx, "select-window", "-t", window, ";", "select-pane", "-t", ref.GetPaneId()); err != nil {
		return nil, status.Errorf(codes.Unavailable, "focus: %v", err)
	}
	return ref, nil
}

func (a *agentServer) closePane(ctx context.Context, p *agentv1.PaneRef) (*agentv1.PaneRef, error) {
	ref, err := a.ownPane(ctx, p.GetPaneId())
	if err != nil {
		return nil, err
	}
	if _, err := a.tmux(ctx, "kill-pane", "-t", ref.GetPaneId()); err != nil {
		return nil, status.Errorf(codes.Unavailable, "close: %v", err)
	}
	return ref, nil
}

// String names the agent's server, for logs.
func (a *agentServer) String() string { return fmt.Sprintf("agent %s's tmux", a.user.ID) }

// agentSocket is the agent's tmux socket, in its own 0700 folder.
func agentSocket(spec AgentServerSpec) string { return filepath.Join(spec.SocketDir, "default") }

// agentTmuxPath is the only program the switch to an agent runs: by its
// absolute path, never through PATH (an agent's PATH starts with its own
// ~/.local/bin, where it could put a "tmux" of its own whose output the
// helper would trust; R-0265).
const agentTmuxPath = "/usr/bin/tmux"

// checkTrustedFile allows a program only when it and every folder above
// it belong to owner (root, outside tests) and no group or other user can
// write to them: a regular file, not a link.
func checkTrustedFile(path string, owner uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%q isn't a clean absolute path", path)
	}
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if err := checkTrustedEntry(p, fi, owner, p == path); err != nil {
			return err
		}
		if p == "/" {
			return nil
		}
	}
}

// checkTrustedEntry checks one step of checkTrustedFile: the program a
// plain file, each folder above it a folder (no links), all owner's, none
// writable by group or others.
func checkTrustedEntry(p string, fi os.FileInfo, owner uint32, program bool) error {
	if program && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a plain file", p)
	}
	if !program && !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder", p)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != owner {
		return fmt.Errorf("%s isn't owned by uid %d", p, owner)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s can be written by its group or others", p)
	}
	return nil
}

// checkAgentSpec allows a start for an agent only: its uid and group, its
// home /home/<id> and its tmux folder TeamRunDir/<id>/tmux.
func checkAgentSpec(spec AgentServerSpec) error {
	if !agentIDRE.MatchString(spec.ID) || spec.ID == "work" {
		return fmt.Errorf("%q isn't an agent's id", spec.ID)
	}
	if spec.Home != "/home/"+spec.ID {
		return fmt.Errorf("%q isn't %s's home", spec.Home, spec.ID)
	}
	if spec.SocketDir != filepath.Join(TeamRunDir, spec.ID, "tmux") {
		return fmt.Errorf("%q isn't %s's tmux folder", spec.SocketDir, spec.ID)
	}
	if w := spec.WorkDir; w != "" && (filepath.Dir(w) != spec.Home || filepath.Clean(w) != w || strings.HasPrefix(filepath.Base(w), ".")) {
		return fmt.Errorf("%q isn't a folder in %s's home", w, spec.ID)
	}
	_, err := checkAsAgent(spec.UID, spec.GID, spec.Home)
	return err
}

// checkAsAgent allows the switch to an agent's user only: an agent's uid,
// its own group, and a home under /home. Root, the helper, work and every
// other uid are refused. It returns the agent's id, from its home.
func checkAsAgent(uid, gid int, home string) (string, error) {
	if uid < AgentUIDFirst || uid > AgentUIDLast {
		return "", fmt.Errorf("uid %d isn't an agent's", uid)
	}
	if err := CheckSwitch(uid, ForUser); err != nil {
		return "", err
	}
	if gid != uid {
		return "", fmt.Errorf("gid %d isn't uid %d's own group", gid, uid)
	}
	id := strings.TrimPrefix(home, "/home/")
	if filepath.Clean(home) != home || filepath.Dir(home) != "/home" || !agentIDRE.MatchString(id) || id == "work" {
		return "", fmt.Errorf("%q isn't an agent's home", home)
	}
	return id, nil
}

// asAgentTeamIO are the team-io operations as-agent runs as an agent:
// its own clone's (ADR 0022 §2a).
var asAgentTeamIO = map[string]bool{"clone-own": true, "bundle-own": true, "lens-grant": true, "migrate-copy": true, "fetch-lens": true}

// parseAsAgent reads "UID GID HOME -- tmux -S SOCKET ARGS…" or "UID GID
// HOME -- team-io OP ARGS…" and checks it: the switch is to an agent
// only; the program is tmux, on that agent's own socket
// (TeamRunDir/<id>/tmux/default), or the plain binary's team-io, for one
// of asAgentTeamIO's operations.
func parseAsAgent(args []string) (uid, gid int, home string, argv []string, err error) {
	teamIO := len(args) >= 6 && args[3] == "--" && args[4] == "team-io" && asAgentTeamIO[args[5]]
	if !teamIO && (len(args) < 7 || args[3] != "--" || args[4] != "tmux" || args[5] != "-S") {
		return 0, 0, "", nil, errors.New("usage: portenv-agent as-agent UID GID HOME -- tmux -S SOCKET ARGS | team-io OP ARGS")
	}
	if uid, err = strconv.Atoi(args[0]); err != nil {
		return 0, 0, "", nil, err
	}
	if gid, err = strconv.Atoi(args[1]); err != nil {
		return 0, 0, "", nil, err
	}
	id, err := checkAsAgent(uid, gid, args[2])
	if err != nil {
		return 0, 0, "", nil, err
	}
	if teamIO {
		return uid, gid, args[2], args[4:], nil
	}
	if want := filepath.Join(TeamRunDir, id, "tmux", "default"); args[6] != want {
		return 0, 0, "", nil, fmt.Errorf("%q isn't %s's tmux socket", args[6], id)
	}
	return uid, gid, args[2], args[4:], nil
}

// withUTF8 is env with LANG=C.UTF-8 unless it sets LANG: without a UTF-8
// locale tmux prints a format's tab separator as "_".
func withUTF8(env []string) []string {
	out := append([]string{}, env...)
	for _, e := range env {
		if strings.HasPrefix(e, "LANG=") {
			return out
		}
	}
	return append(out, "LANG=C.UTF-8")
}

// routinePrefix starts every routine typed to an agent (R-0258), so the
// agent and anyone reading its terminal know where it came from.
const routinePrefix = "[Portenv routine] "

// maxRoutine is the longest routine line, in bytes, prefix included.
const maxRoutine = 4096

// routineQuiet is how long the agent's window must have printed nothing
// before a routine is typed (tmux counts activity in whole seconds).
var routineQuiet = 5 * time.Second

// maxIdleInput is the longest idle-input pattern.
const maxIdleInput = 512

// SetAgentIdleInput sets the pattern that says a running agent's runner
// waits at an empty input: a regular expression (RE2, so its matching
// time is linear), matched against the main pane's last lines, one per
// line ((?m)). The kit supplies it per runner (lane C); a runner without
// one gets no routines.
func (s *apiServer) SetAgentIdleInput(id, pattern string) error {
	if len(pattern) > maxIdleInput {
		return status.Errorf(codes.InvalidArgument, "an idle-input pattern is at most %d bytes", maxIdleInput)
	}
	var re *regexp.Regexp
	if pattern != "" {
		var err error
		if re, err = regexp.Compile("(?m)" + pattern); err != nil {
			return status.Errorf(codes.InvalidArgument, "the idle-input pattern: %v", err)
		}
	}
	s.agents.mu.Lock()
	defer s.agents.mu.Unlock()
	a, ok := s.agents.by[id]
	if !ok {
		return status.Errorf(codes.FailedPrecondition, "%s isn't running", id)
	}
	a.idleInput = re
	return nil
}

// readyForRoutine says why the main pane can't take a routine now, or ""
// when it can (R-0258, R-0260). The helper can't read an agent's /proc or
// terminal modes (another user), so the waiting detector's verdict rests
// on the screen and the quiet time here.
//
//  1. It isn't asking something (the detector's Waiting): a routine typed
//     there would answer the question.
//  2. The window has printed nothing for routineQuiet.
//  3. It isn't a full-screen program (the alternate screen).
//  4. The runner's idle-input pattern matches: it waits at an empty input.
//
// And whatever the four say, nothing is typed while any of the last lines
// reads as a secret prompt (ADR 0016's rule: "Password:", a passphrase, a
// token asked for), even when the runner's pattern matches (R-0261).
func (a *agentServer) readyForRoutine(ctx context.Context, idleInput *regexp.Regexp) (string, error) {
	out, err := a.tmux(ctx, "display-message", "-p", "-t", a.mainPane, "#{alternate_on}\t#{window_activity}")
	if err != nil {
		return "", err
	}
	f := strings.Split(strings.TrimSpace(out), "\t")
	if len(f) != 2 {
		return "", fmt.Errorf("tmux answered %q", out)
	}
	activity, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("tmux answered %q", out)
	}
	screen, err := a.tmux(ctx, "capture-pane", "-p", "-t", a.mainPane)
	if err != nil {
		return "", err
	}
	lines := lastLines(screen, screenLines)
	sig := waitSignals{
		Shell:        shellUnknown,
		Quiet:        time.Since(time.Unix(activity, 0)),
		AltScreen:    f[0] == "1",
		ScreenPrompt: screenAsks(lines),
		LastLines:    lines,
	}
	switch {
	case sig.Secret():
		// Never the prompt's text (ADR 0016 point 8).
		return "busy: " + a.user.ID + "'s terminal asks for a secret", nil
	case sig.Waiting():
		return "busy: " + a.user.ID + " is asking something", nil
	case sig.Quiet < routineQuiet:
		return fmt.Sprintf("busy: %s printed something in the last %s", a.user.ID, routineQuiet), nil
	case sig.AltScreen:
		return "busy: " + a.user.ID + " is in a full-screen program", nil
	case idleInput == nil:
		return "busy: " + a.user.ID + "'s runner has no idle-input pattern, so it never takes routines", nil
	case !idleInput.MatchString(strings.Join(lines, "\n")):
		return "busy: " + a.user.ID + " isn't waiting at its input", nil
	}
	return "", nil
}

// deliverRoutine types "[Portenv routine] <text>" and Enter into the
// agent's main pane (its runner's, in its own main session, on its own
// server), as the agent: one line of plain text, in one tmux command so
// nothing lands between the line and its Enter. Any other pane, a pane
// that's gone, and text that isn't one plain line are refused; so is a
// pane not ready for it (readyForRoutine: "busy", portenvd tries again).
func (s *apiServer) deliverRoutine(ctx context.Context, agentID, text string) error {
	a, ok := s.agents.get(agentID)
	if !ok {
		return status.Errorf(codes.FailedPrecondition, "%s isn't running", agentID)
	}
	line := routinePrefix + text
	switch {
	case strings.TrimSpace(text) == "":
		return status.Error(codes.InvalidArgument, "a routine needs text")
	case !routineLine(text):
		return status.Error(codes.InvalidArgument, "a routine is one line of plain text")
	case len(line) > maxRoutine:
		return status.Errorf(codes.InvalidArgument, "a routine is at most %d bytes", maxRoutine-len(routinePrefix))
	}
	return s.typeWhenIdle(ctx, a, line, "routine")
}

// typeWhenIdle types line and Enter into a's main pane, in one tmux
// command, only when readyForRoutine passes: the one typing path for
// routines and wake-ups (CC-0498), so both keep every check. what names
// the call in errors.
func (s *apiServer) typeWhenIdle(ctx context.Context, a *agentServer, line, what string) error {
	agentID := a.user.ID
	escaped, err := tmuxArg(line)
	if err != nil {
		return err
	}
	if _, err := a.ownPane(ctx, a.mainPane); err != nil {
		return status.Errorf(codes.FailedPrecondition, "%s's main pane is gone", agentID)
	}
	s.agents.mu.Lock()
	idleInput := a.idleInput
	s.agents.mu.Unlock()
	why, err := a.readyForRoutine(ctx, idleInput)
	if err != nil {
		return status.Errorf(codes.Unavailable, "%s to %s: %v", what, agentID, err)
	}
	if why != "" {
		return status.Error(codes.FailedPrecondition, why)
	}
	if _, err := a.tmux(ctx, "send-keys", "-t", a.mainPane, "-l", "--", escaped, ";", "send-keys", "-t", a.mainPane, "Enter"); err != nil {
		return status.Errorf(codes.Unavailable, "%s to %s: %v", what, agentID, err)
	}
	return nil
}

// routineLine is the board's plain-text rule for one line (plainText(s,
// false) in teamio.go, #173): valid UTF-8, no control characters and no
// bidi controls, which could make a routine read differently from what is
// typed. It becomes a call to plainText once #173 is in.
func routineLine(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F || r == 0x061C {
			return false
		}
	}
	return true
}
