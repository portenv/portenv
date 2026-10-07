// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ForbiddenCapabilities are the capabilities no box may start with. Any of
// them lets root in the box read another process's memory or reach the
// kernel, which would expose the repository password while restic runs
// (ADR 0005, conditions). A privileged container has all of them.
var ForbiddenCapabilities = map[uint]string{
	16: "CAP_SYS_MODULE",
	17: "CAP_SYS_RAWIO",
	19: "CAP_SYS_PTRACE",
	21: "CAP_SYS_ADMIN",
	38: "CAP_PERFMON",
	39: "CAP_BPF",
}

// forbiddenIn returns the forbidden capabilities present in a capability
// set, in bit order.
func forbiddenIn(set uint64) []string {
	var out []string
	for bit := range uint(64) {
		if name, ok := ForbiddenCapabilities[bit]; ok && set&(1<<bit) != 0 {
			out = append(out, name)
		}
	}
	return out
}

// BoundingSet reads this process's capability bounding set.
func BoundingSet() (uint64, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	return parseCapBnd(f)
}

func parseCapBnd(r io.Reader) (uint64, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "CapBnd:"); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no CapBnd line in /proc/self/status")
}

// checkIsolation fails if the box was started with a forbidden capability.
func checkIsolation(read func() (uint64, error)) error {
	set, err := read()
	if err != nil {
		return fmt.Errorf("read capability bounding set: %w", err)
	}
	if bad := forbiddenIn(set); len(bad) > 0 {
		return fmt.Errorf("the box was started with %s; refusing to run because restic's password would be readable (ADR 0005)", strings.Join(bad, ", "))
	}
	return nil
}
