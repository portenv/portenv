// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

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
}

// agentServers are the running agents' servers, by id.
type agentServers struct {
	mu sync.Mutex
	by map[string]*agentServer
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
	args := append([]string{"new-session", "-d", "-s", agentSession, "-n", spec.ID, "--"}, argv...)
	args = append(args, tmuxHidden...)
	args = append(args, tmuxOwned...)
	args = append(args, tmuxSealed...)
	if _, err := run(ctx, args...); err != nil {
		return nil, status.Errorf(codes.Unavailable, "%s's terminal couldn't start: %v", spec.ID, err)
	}
	a := &agentServer{user: spec, tmux: run, hub: newCtlHub(run, cmd)}
	if s.agents.by == nil {
		s.agents.by = map[string]*agentServer{}
	}
	s.agents.by[spec.ID] = a
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

// checkAsAgent allows the switch to an agent's user only: an agent's uid,
// its own group, and a home under /home. Root, the helper, work and every
// other uid are refused.
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
	if filepath.Clean(home) != home || filepath.Dir(home) != "/home" || home == "/home/work" {
		return "", fmt.Errorf("%q isn't an agent's home", home)
	}
	return home, nil
}

// parseAsAgent reads "UID GID HOME -- tmux ARGS…" and checks it: the
// switch is to an agent only, and the program is tmux only.
func parseAsAgent(args []string) (uid, gid int, home string, argv []string, err error) {
	if len(args) < 5 || args[3] != "--" || args[4] != "tmux" {
		return 0, 0, "", nil, errors.New("usage: portenv-agent as-agent UID GID HOME -- tmux ARGS")
	}
	if uid, err = strconv.Atoi(args[0]); err != nil {
		return 0, 0, "", nil, err
	}
	if gid, err = strconv.Atoi(args[1]); err != nil {
		return 0, 0, "", nil, err
	}
	if home, err = checkAsAgent(uid, gid, args[2]); err != nil {
		return 0, 0, "", nil, err
	}
	return uid, gid, home, args[4:], nil
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
