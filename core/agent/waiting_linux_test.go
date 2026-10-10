// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
)

// fakeProc is a /proc with a few processes, for the probe's group logic.
type fakeProc struct {
	files map[string]string
	links map[string]string
}

func (f fakeProc) read(p string) ([]byte, error) {
	if s, ok := f.files[p]; ok {
		return []byte(s), nil
	}
	return nil, errors.New("no such file")
}

func (f fakeProc) readlink(p string) (string, error) {
	if s, ok := f.links[p]; ok {
		return s, nil
	}
	return "", errors.New("no such link")
}

func readSyscall() int {
	for n, name := range waitSyscalls[runtime.GOARCH] {
		if name == "read" {
			return n
		}
	}
	return -1
}

// In "seq 500 | less" the group's leader (seq) has exited: the probe checks
// the group's live members, and less reading /dev/tty counts as blocked.
func TestProbeChecksTheWholeForegroundGroup(t *testing.T) {
	if readSyscall() < 0 {
		t.Skipf("no syscall table for %s", runtime.GOARCH)
	}
	p := fakeProc{
		files: map[string]string{
			// less: pid 42 in process group 41 (seq, gone), reading fd 3.
			"/proc/42/stat":    "42 (less) S 10 41 10 34816 41 4194304",
			"/proc/42/syscall": fmt.Sprintf("%d 0x3 0xffff 0x1", readSyscall()),
			"/proc/50/stat":    "50 (other) S 1 50 50 0 -1 0",
		},
		links: map[string]string{"/proc/42/fd/3": "/dev/tty", "/proc/42/fd/0": "pipe:[123]"},
	}
	probe := tabProbe{read: p.read, readlink: p.readlink, pids: func() ([]string, error) { return []string{"42", "50"}, nil }}
	blocked, known := probe.groupBlockedOn("41", "/dev/pts/0")
	if !blocked || !known {
		t.Fatalf("less in a group whose leader exited: blocked=%v known=%v", blocked, known)
	}
	// Only the leader, gone: unknown, not "not blocked".
	alone := tabProbe{read: p.read, readlink: p.readlink}
	if b, k := alone.groupBlockedOn("41", "/dev/pts/0"); b || k {
		t.Fatalf("a gone leader: blocked=%v known=%v", b, k)
	}
}
