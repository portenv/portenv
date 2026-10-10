// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Tabs (PLAN.md 1.2, GUIDELINES.md §3, §4.1): one tab per tmux window of
// the box's session. The app owns names and order; programs in the box
// can't rename a tab, and every window in the session is a tab, so a window
// a program creates itself still shows. How the agent talks to tmux (plain
// commands now, perhaps control mode after the spike) stays in this file.

// tmuxFunc runs one tmux command (as the main user, inside a box) and
// returns its output.
type tmuxFunc func(ctx context.Context, args ...string) (string, error)

// tabs works on one session's tabs, as seen by one viewer.
type tabs struct {
	tmux    tmuxFunc
	session string
	// viewer, when set, has its own grouped tmux session over the
	// session's windows (view), so its current tab is its own: one viewer
	// choosing a tab never moves another's.
	viewer string
}

// viewerRE is a viewer's name: short, lower case, plain.
var viewerRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)

// shells are what runs in a tab with nothing else in the foreground.
var shells = map[string]bool{"bash": true, "zsh": true, "sh": true, "dash": true, "fish": true, "ksh": true, "tcsh": true, "csh": true}

// firstTabName names the tab a new session starts with.
const firstTabName = "shell"

// tmuxHidden are tmux commands chained after new-session: no status bar, no
// prefix key (§4.1).
var tmuxHidden = []string{
	";", "set-option", "-g", "status", "off",
	";", "set-option", "-g", "prefix", "None",
	";", "set-option", "-g", "prefix2", "None",
	";", "unbind-key", "C-b",
}

// tmuxOwned are tmux options chained after new-session that keep tab names
// the app's: no automatic rename after the running command, and no rename
// by escape sequence from a program.
var tmuxOwned = []string{
	";", "set-option", "-g", "allow-rename", "off",
	";", "set-option", "-g", "automatic-rename", "off",
}

// windowIDRE is a tmux window ID, the only form of tab ID accepted.
var windowIDRE = regexp.MustCompile(`^@[0-9]{1,9}$`)

// tabsPoll is how often a watch looks at the session's windows.
var tabsPoll = 300 * time.Millisecond

// checkTabName: 1 to 32 characters, no control characters, no surrounding
// spaces.
func checkTabName(name string) error {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 32 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return status.Error(codes.InvalidArgument, "tab names are 1 to 32 characters, without spaces at either end")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return status.Error(codes.InvalidArgument, "tab names can't contain control characters")
	}
	return nil
}

// view is the tmux session this viewer looks through: its own grouped
// session, or the box's session when there's no viewer.
func (t tabs) view() string {
	if t.viewer == "" {
		return t.session
	}
	return t.session + "-viewer-" + t.viewer
}

// target is the viewer's session as an exact tmux target.
func (t tabs) target() string { return "=" + t.view() + ":" }

// ensure creates the box's session, detached, if it doesn't exist yet, and
// the viewer's grouped session over it. A session created at the same moment
// by a terminal is fine.
func (t tabs) ensure(ctx context.Context) error {
	if t.viewer != "" && !viewerRE.MatchString(t.viewer) {
		return status.Error(codes.InvalidArgument, "viewer names are 1 to 16 lower-case letters, digits or -")
	}
	base := append([]string{"new-session", "-d", "-s", t.session, "-n", firstTabName}, tmuxHidden...)
	if err := t.create(ctx, t.session, append(base, tmuxOwned...)); err != nil {
		return err
	}
	if t.viewer == "" {
		return nil
	}
	return t.create(ctx, t.view(), []string{"new-session", "-d", "-s", t.view(), "-t", "=" + t.session})
}

// create runs args (a new-session) unless the session exists.
func (t tabs) create(ctx context.Context, session string, args []string) error {
	if _, err := t.tmux(ctx, "has-session", "-t", "="+session); err == nil {
		return nil
	}
	if _, err := t.tmux(ctx, args...); err != nil {
		if _, again := t.tmux(ctx, "has-session", "-t", "="+session); again == nil {
			return nil
		}
		return status.Errorf(codes.Unavailable, "the terminal session couldn't start: %v", err)
	}
	return nil
}

// cleanup ends the viewer's grouped session once no client is attached to
// it (its terminal ended). The box's session and its tabs stay.
func (t tabs) cleanup(ctx context.Context) {
	if t.viewer == "" || !viewerRE.MatchString(t.viewer) {
		return
	}
	out, err := t.tmux(ctx, "list-clients", "-t", "="+t.view(), "-F", "#{client_name}")
	if err != nil || strings.TrimSpace(out) != "" {
		return
	}
	_, _ = t.tmux(ctx, "kill-session", "-t", "="+t.view())
}

// list returns the tabs in order.
func (t tabs) list(ctx context.Context) ([]*typesv1.Tab, error) {
	if err := t.ensure(ctx); err != nil {
		return nil, err
	}
	out, err := t.tmux(ctx, "list-windows", "-t", t.target(), "-F", "#{window_id}\t#{window_active}\t#{pane_current_command}\t#{window_name}")
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "list tabs: %v", err)
	}
	var ts []*typesv1.Tab
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 || !windowIDRE.MatchString(f[0]) {
			continue
		}
		program := f[2]
		if shells[program] {
			program = ""
		}
		ts = append(ts, &typesv1.Tab{Id: f[0], Name: f[3], Active: f[1] == "1", Program: program})
	}
	return ts, nil
}

// find checks that id is a window ID of this session.
func (t tabs) find(ctx context.Context, id string) ([]*typesv1.Tab, error) {
	if !windowIDRE.MatchString(id) {
		return nil, status.Error(codes.InvalidArgument, "tab IDs look like @3")
	}
	ts, err := t.list(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(ts, func(x *typesv1.Tab) bool { return x.GetId() == id }) {
		return nil, status.Errorf(codes.NotFound, "no tab %s in this box", id)
	}
	return ts, nil
}

// open appends a tab and shows it.
func (t tabs) open(ctx context.Context, name string) (*typesv1.Tab, error) {
	if err := checkTabName(name); err != nil {
		return nil, err
	}
	if err := t.ensure(ctx); err != nil {
		return nil, err
	}
	out, err := t.tmux(ctx, "new-window", "-t", t.target(), "-P", "-F", "#{window_id}", "-n", name)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "new tab: %v", err)
	}
	id := strings.TrimSpace(out)
	if !windowIDRE.MatchString(id) {
		return nil, status.Errorf(codes.Internal, "new tab: unexpected window ID %q", id)
	}
	return &typesv1.Tab{Id: id, Name: name, Active: true}, nil
}

// close ends a tab, unless it's the last one.
func (t tabs) close(ctx context.Context, id string) error {
	ts, err := t.find(ctx, id)
	if err != nil {
		return err
	}
	if len(ts) == 1 {
		return status.Error(codes.FailedPrecondition, "a box keeps at least one tab")
	}
	if _, err := t.tmux(ctx, "kill-window", "-t", id); err != nil {
		return status.Errorf(codes.Unavailable, "close tab: %v", err)
	}
	return nil
}

// rename renames a tab.
func (t tabs) rename(ctx context.Context, id, name string) error {
	if err := checkTabName(name); err != nil {
		return err
	}
	if _, err := t.find(ctx, id); err != nil {
		return err
	}
	if _, err := t.tmux(ctx, "rename-window", "-t", id, "--", name); err != nil {
		return status.Errorf(codes.Unavailable, "rename tab: %v", err)
	}
	return nil
}

// selectTab shows a tab in the session's terminal.
func (t tabs) selectTab(ctx context.Context, id string) error {
	if _, err := t.find(ctx, id); err != nil {
		return err
	}
	if _, err := t.tmux(ctx, "select-window", "-t", t.target()+id); err != nil {
		return status.Errorf(codes.Unavailable, "select tab: %v", err)
	}
	return nil
}

// watch sends the tabs at once and again whenever they change, until ctx
// ends or send fails.
func (t tabs) watch(ctx context.Context, every time.Duration, send func([]*typesv1.Tab) error) error {
	var last []*typesv1.Tab
	for first := true; ; first = false {
		ts, err := t.list(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if first || !sameTabs(ts, last) {
			if err := send(ts); err != nil {
				return err
			}
			last = ts
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}

func sameTabs(a, b []*typesv1.Tab) bool {
	return slices.EqualFunc(a, b, func(x, y *typesv1.Tab) bool { return proto.Equal(x, y) })
}
