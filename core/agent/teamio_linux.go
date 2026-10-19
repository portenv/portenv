// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// asAgentEnv is the fixed environment of team-io run as an agent.
func asAgentEnv(home string) []string {
	id := filepath.Base(home)
	return []string{"HOME=" + home, "USER=" + id, "LOGNAME=" + id, "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
}

// asAgentCommand is "portenv-agent as-agent UID UID HOME -- team-io ARGS":
// this binary switches to the agent (its group and team, no capabilities,
// no_new_privs) and then runs the plain binary's team-io (RunAsAgent).
// init (root) and the API process (cap_setuid, cap_setgid) can both run
// it.
func asAgentCommand(ctx context.Context, uid int, home string, args []string) (*exec.Cmd, error) {
	if _, err := checkAsAgent(uid, uid, home); err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	argv := append([]string{"as-agent", strconv.Itoa(uid), strconv.Itoa(uid), home, "--", "team-io"}, args...)
	cmd := exec.CommandContext(ctx, self, argv...) // #nosec G204 -- this binary; arguments made by the caller and checked by as-agent
	cmd.Dir = "/"
	cmd.Env = asAgentEnv(home)
	return cmd, nil
}

// runTeamIOAsAgent runs team-io as an agent and returns its output.
func runTeamIOAsAgent(ctx context.Context, uid int, home string, args []string, stdin io.Reader) ([]byte, error) {
	cmd, err := asAgentCommand(ctx, uid, home, args)
	if err != nil {
		return nil, err
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := asAgentMessage(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("%s: %w", args[0], err)
	}
	return out, nil
}

// streamTeamIOAsAgent starts team-io as an agent with its standard output
// on the returned reader, which the caller closes; wait reaps it and
// returns what it said went wrong.
func streamTeamIOAsAgent(ctx context.Context, uid int, home string, args []string) (io.ReadCloser, func() error, error) {
	cmd, err := asAgentCommand(ctx, uid, home, args)
	if err != nil {
		return nil, nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = pw, &stderr
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, nil, err
	}
	_ = pw.Close()
	wait := func() error {
		if err := cmd.Wait(); err != nil {
			if msg := asAgentMessage(stderr.String()); msg != "" {
				return errors.New(msg)
			}
			return fmt.Errorf("%s: %w", args[0], err)
		}
		return nil
	}
	return pr, wait, nil
}

// asAgentMessage is what as-agent's team-io said on stderr, on one line.
func asAgentMessage(s string) string {
	msg := strings.TrimSpace(s)
	msg = strings.TrimPrefix(strings.TrimPrefix(msg, "portenv-agent: "), "portenv-agent as-agent: ")
	return strings.Join(strings.Fields(strings.ReplaceAll(msg, "\n", " · ")), " ")
}

// runAsAgent runs the plain portenv-agent binary as an agent's uid, with
// its own group and group team, for the Agent files view and hand-offs.
// Group team is how an agent reads the project (ADR 0022 §2a), so a
// hand-off of a project file needs it (CC-0507). Outside the API process
// it runs local in this process.
func runAsAgent(ctx context.Context, uid int, args []string, stdin io.Reader, local func(io.Writer) error) ([]byte, error) {
	return runAsAgentWith(ctx, uid, true, args, stdin, local)
}

// runAsAgentInTeam runs as the agent with the team group, as the agent's
// own sessions have it: for reading the project, which the team reads
// through its ACL (home_seed, ADR 0022 §2). runAsAgent has the team group
// too, since hand-offs read the project (CC-0507).
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
