// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// runAsMainUser runs the plain portenv-agent binary with args as the main
// user (work), with stdin, and returns its output. Outside the API process
// (tests, a box without the channel) it runs local in this process.
func runAsMainUser(ctx context.Context, cfg Config, args []string, stdin io.Reader, local func(io.Writer) error) ([]byte, error) {
	if _, err := os.Stat(ServeBinary); err != nil || os.Getuid() != ServeUID {
		var out bytes.Buffer
		err := local(&out)
		return out.Bytes(), err
	}
	if err := CheckSwitch(cfg.UID, ForUser); err != nil {
		return nil, err
	}
	groups, err := groupsOf(cfg.User)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, PlainBinary, args...) // #nosec G204 -- fixed binary; arguments made by the API process
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	uid := uint32(cfg.UID) // #nosec G115 -- 1000
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: groups}}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// runAsAgent runs the plain portenv-agent binary as an agent's uid (its
// own group only), for the Agent files view. Outside the API process it
// runs local in this process.
func runAsAgent(ctx context.Context, uid int, args []string, stdin io.Reader, local func(io.Writer) error) ([]byte, error) {
	return runAsAgentWith(ctx, uid, false, args, stdin, local)
}

// runAsAgentInTeam is runAsAgent with the team group too, as the agent's
// own sessions have it: for reading the project, which the team reads
// through its ACL (home_seed, ADR 0022 §2).
func runAsAgentInTeam(ctx context.Context, uid int, args []string, stdin io.Reader, local func(io.Writer) error) ([]byte, error) {
	return runAsAgentWith(ctx, uid, true, args, stdin, local)
}

func runAsAgentWith(ctx context.Context, uid int, team bool, args []string, stdin io.Reader, local func(io.Writer) error) ([]byte, error) {
	if _, err := os.Stat(ServeBinary); err != nil || os.Getuid() != ServeUID {
		var out bytes.Buffer
		err := local(&out)
		return out.Bytes(), err
	}
	if err := checkKillableUID(uid); err != nil {
		return nil, err
	}
	if err := CheckSwitch(uid, ForUser); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, PlainBinary, args...) // #nosec G204 -- fixed binary; arguments made by the API process
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	u := uint32(uid) // #nosec G115 -- an agent's uid, 2001..2008
	groups := []uint32{u}
	if team {
		groups = append(groups, TeamGID)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: u, Gid: u, Groups: groups}}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, errHomeFull.Error()) {
			return nil, errHomeFull
		}
		return nil, fmt.Errorf("%s: %w: %s", args[0], err, msg)
	}
	return out, nil
}
