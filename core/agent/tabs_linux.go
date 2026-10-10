// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// tmuxAs runs tmux as the main user, on the same server as their terminals
// (the same uid, environment and default socket).
func tmuxAs(cfg Config) (tmuxFunc, error) {
	if err := CheckSwitch(cfg.UID, ForUser); err != nil {
		return nil, err
	}
	groups, err := groupsOf(cfg.User)
	if err != nil {
		return nil, err
	}
	uid := uint32(cfg.UID) // #nosec G115 -- 1000
	return func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "tmux", args...) // #nosec G204 -- fixed commands; session, IDs and names validated
		cmd.Dir = cfg.Home()
		cmd.Env = envFor(cfg)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: groups}}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}, nil
}
