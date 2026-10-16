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
