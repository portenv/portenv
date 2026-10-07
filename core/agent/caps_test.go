// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"slices"
	"strings"
	"testing"
)

// Docker's default capability set, as /proc/self/status shows it.
const dockerDefaultCapBnd = 0x00000000a80425fb

func TestDockerDefaultsPass(t *testing.T) {
	if bad := forbiddenIn(dockerDefaultCapBnd); len(bad) != 0 {
		t.Fatalf("Docker defaults flagged: %v", bad)
	}
}

func TestForbiddenCapabilitiesAreFound(t *testing.T) {
	for bit, name := range ForbiddenCapabilities {
		if got := forbiddenIn(dockerDefaultCapBnd | 1<<bit); !slices.Equal(got, []string{name}) {
			t.Errorf("bit %d: got %v, want [%s]", bit, got, name)
		}
	}
	// Privileged: every capability.
	if got := forbiddenIn(^uint64(0)); len(got) != len(ForbiddenCapabilities) {
		t.Errorf("privileged: got %v", got)
	}
}

func TestParseCapBnd(t *testing.T) {
	status := "Name:\tportenv-agent\nCapInh:\t0000000000000000\nCapBnd:\t00000000a80425fb\nCapAmb:\t0000000000000000\n"
	got, err := parseCapBnd(strings.NewReader(status))
	if err != nil || got != dockerDefaultCapBnd {
		t.Fatalf("got %x %v", got, err)
	}
}

func TestCheckIsolation(t *testing.T) {
	if err := checkIsolation(func() (uint64, error) { return dockerDefaultCapBnd, nil }); err != nil {
		t.Fatal(err)
	}
	err := checkIsolation(func() (uint64, error) { return dockerDefaultCapBnd | 1<<19 | 1<<21, nil })
	if err == nil || !strings.Contains(err.Error(), "CAP_SYS_PTRACE, CAP_SYS_ADMIN") {
		t.Fatalf("got %v", err)
	}
}
