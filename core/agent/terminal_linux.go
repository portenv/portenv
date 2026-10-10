// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// runTerminal attaches a PTY to tmux new-session -A as the main user, so
// reopening lands in the same session, and relays it over the stream.
func runTerminal(stream agentv1.AgentService_TerminalServer, cfg Config, open *agentv1.TerminalOpen) error {
	if err := CheckSwitch(cfg.UID, ForUser); err != nil {
		return err
	}
	groups, err := groupsOf(cfg.User)
	if err != nil {
		return err
	}
	// tmux is plumbing (GUIDELINES.md §4.1): its status bar is always off
	// and its prefix key never surfaces, so Ctrl-B reaches the shell. Set
	// after the user's own tmux config, on every attach.
	// The app owns tab names (tmuxOwned); a new session starts with one tab.
	args := append([]string{"new-session", "-A", "-s", open.GetSession(), "-n", firstTabName}, tmuxHidden...)
	args = append(args, tmuxOwned...)
	if v := open.GetViewer(); v != "" {
		// A viewer (the app, an agent) attaches to its own grouped session
		// over the box's windows, so its current tab is its own; the
		// session goes when no client is left on it.
		run, err := tmuxAs(cfg)
		if err != nil {
			return err
		}
		tb := tabs{tmux: run, session: open.GetSession(), viewer: v}
		if err := tb.ensure(stream.Context()); err != nil {
			return err
		}
		defer tb.cleanup(context.Background())
		args = append([]string{"attach-session", "-t", "=" + tb.view()}, tmuxHidden...)
		args = append(args, tmuxOwned...)
	}
	cmd := exec.CommandContext(stream.Context(), "tmux", args...) // #nosec G204 -- session name validated; fixed options
	cmd.Dir = cfg.Home()
	cmd.Env = envFor(cfg)
	uid := uint32(cfg.UID) // #nosec G115 -- 1000
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: groups}}
	return ServeTerminal(stream, cmd, uint16(open.GetSize().GetCols()), uint16(open.GetSize().GetRows()), open.GetInputPiped()) // #nosec G115 -- terminal sizes
}

// groupsOf lists a user's supplementary groups (id -G).
func groupsOf(user string) ([]uint32, error) {
	out, err := exec.Command("id", "-G", user).Output() // #nosec G204 -- fixed user name from config
	if err != nil {
		return nil, err
	}
	var gs []uint32
	for _, f := range strings.Fields(string(out)) {
		g, err := strconv.ParseUint(f, 10, 32)
		if err != nil {
			return nil, err
		}
		gs = append(gs, uint32(g))
	}
	return gs, nil
}

// envFor is the environment of a terminal as the main user.
func envFor(cfg Config) []string {
	return []string{
		"HOME=" + cfg.Home(), "USER=" + cfg.User, "LOGNAME=" + cfg.User,
		"SHELL=/bin/bash", "TERM=xterm-256color", "LANG=C.UTF-8", "COLORTERM=truecolor",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
}

// inputQueueReq asks a terminal how much input its program hasn't read.
const inputQueueReq = unix.TIOCINQ
