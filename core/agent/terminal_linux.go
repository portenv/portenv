// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/creack/pty"

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
	cmd := exec.CommandContext(stream.Context(), "tmux", "new-session", "-A", "-s", open.GetSession()) // #nosec G204 -- session name validated
	cmd.Dir = cfg.Home()
	cmd.Env = envFor(cfg)
	uid := uint32(cfg.UID) // #nosec G115 -- 1000
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: groups}}
	size := &pty.Winsize{Cols: uint16(open.GetSize().GetCols()), Rows: uint16(open.GetSize().GetRows())} // #nosec G115 -- terminal sizes
	if size.Cols == 0 || size.Rows == 0 {
		size = &pty.Winsize{Cols: 80, Rows: 24}
	}
	f, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// Keystrokes and resizes from the client.
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				_ = f.Close()
				return
			}
			switch m := req.GetMsg().(type) {
			case *agentv1.TerminalRequest_Input:
				if _, err := f.Write(m.Input); err != nil {
					return
				}
			case *agentv1.TerminalRequest_Resize:
				_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(m.Resize.GetCols()), Rows: uint16(m.Resize.GetRows())}) // #nosec G115 -- terminal sizes
			}
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if err := stream.Send(&agentv1.TerminalResponse{Msg: &agentv1.TerminalResponse_Output{Output: append([]byte(nil), buf[:n]...)}}); err != nil {
				_ = cmd.Process.Kill()
				break
			}
		}
		if rerr != nil {
			if !isClosed(rerr) {
				return rerr
			}
			break
		}
	}
	code := 0
	var exit *exec.ExitError
	if err := cmd.Wait(); errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	return stream.Send(&agentv1.TerminalResponse{Msg: &agentv1.TerminalResponse_ExitCode{ExitCode: int32(code)}}) // #nosec G115 -- exit codes fit
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

// isClosed reports whether err only says the other side went away.
func isClosed(err error) bool {
	return err == nil || errors.Is(err, os.ErrClosed) || strings.Contains(err.Error(), "input/output error")
}
