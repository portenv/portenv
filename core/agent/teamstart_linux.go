// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// writeFileAsUser writes data to path as uid and gid (tee in a process
// with that identity), so the file is the agent's and the helper never
// writes into an agent's home itself.
func writeFileAsUser(uid, gid int, path string, data []byte) error {
	cmd := exec.Command("/usr/bin/tee", "--", path) // #nosec G204 -- a fixed program; path is the agent's own home
	cmd.Stdin = bytes.NewReader(data)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Dir = filepath.Dir(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}} // #nosec G115 -- agent uids, 2001..2008
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// killAgentUID sends SIGKILL to every process whose real or effective uid
// is uid, an agent's only.
func killAgentUID(uid int) error {
	if err := checkKillableUID(uid); err != nil {
		return err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	want := strconv.Itoa(uid)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status")) // #nosec G304 -- /proc
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 3 && f[0] == "Uid:" {
				if f[1] == want || f[2] == want {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				break
			}
		}
	}
	return nil
}
