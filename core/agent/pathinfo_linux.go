// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// PlainBinary is the agent without file capabilities.
const PlainBinary = "/usr/local/bin/portenv-agent"

// pathInfoAs answers a path check as the main user, through the agent
// binary that carries no capabilities: the API process itself cannot read
// work's home (it has no cap_dac_override). Outside a box (tests) it
// answers directly.
func pathInfoAs(ctx context.Context, cfg Config, path string) (map[string]bool, error) {
	if _, err := os.Stat(ServeBinary); err != nil || os.Getuid() != ServeUID {
		return PathInfo(path)
	}
	if err := CheckSwitch(cfg.UID, ForUser); err != nil {
		return nil, err
	}
	groups, err := groupsOf(cfg.User)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, PlainBinary, "path-info", path) // #nosec G204 -- fixed binary; path checked by path-info
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	uid := uint32(cfg.UID) // #nosec G115 -- 1000
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: groups}}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("path-info: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var info map[string]bool
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, err
	}
	return info, nil
}
