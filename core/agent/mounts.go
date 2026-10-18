// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// StorageMount is where local storage appears inside a box: the box's own
// repository folder, at StorageMount/<box-id> (this Mac) or
// StorageMount/boxes/<box-id> (a drive), and nothing else.
const StorageMount = "/run/portenv/storage"

// engineSockets are the control sockets of container engines. A box with
// one can start containers on the machine as root (R-0199, R-0201).
var engineSockets = []string{"docker.sock", "containerd.sock", "podman.sock"}

// EngineSocket is whether p names a container engine's control socket.
func EngineSocket(p string) bool {
	base := path.Base(p)
	for _, s := range engineSockets {
		if base == s {
			return true
		}
	}
	return false
}

// usualSockets are where engines put their sockets, checked whether or not
// anything is mounted there.
var usualSockets = []string{
	"/var/run/docker.sock", "/run/docker.sock",
	"/run/containerd/containerd.sock", "/var/run/containerd/containerd.sock",
	"/run/podman/podman.sock", "/var/run/podman/podman.sock",
}

// checkMounts refuses a box that can reach more than its own (R-0201,
// ruling 2): a storage mount holding anything but its own repository, or
// any container engine's socket, mounted on its own or inside a mounted
// folder. mountinfo is /proc/self/mountinfo; boxID is the box this helper
// runs in (BoxID); socketAt says whether an engine socket exists at a path.
func checkMounts(mountinfo, boxID string, socketAt func(string) bool) error {
	own := map[string]bool{}
	if boxID != "" {
		own[StorageMount+"/"+boxID] = true
		own[StorageMount+"/boxes/"+boxID] = true
	}
	for _, l := range strings.Split(mountinfo, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		root, at := unescapeMountinfo(f[3]), path.Clean(unescapeMountinfo(f[4]))
		if at == StorageMount || strings.HasPrefix(at, StorageMount+"/") {
			if boxID == "" {
				return fmt.Errorf("refusing to start: this workspace has saves mounted but wasn't told which are its own, so it could see other workspaces' saves. Restart the workspace from Portenv")
			}
			if !own[at] {
				return fmt.Errorf("refusing to start: this workspace could see other workspaces' saves (%s is mounted). Restart the workspace from Portenv so it gets only its own", at)
			}
		}
		if EngineSocket(root) || EngineSocket(at) {
			return engineSocketError(at)
		}
		for _, s := range engineSockets {
			if p := path.Join(at, s); at != "/" && socketAt(p) {
				return engineSocketError(p)
			}
		}
	}
	for _, p := range usualSockets {
		if socketAt(p) {
			return engineSocketError(p)
		}
	}
	return nil
}

func engineSocketError(at string) error {
	return fmt.Errorf("refusing to start: a container engine's socket is at %s in this workspace, which would give programs in it control of the machine. Remove that mount", at)
}

// unescapeMountinfo undoes mountinfo's octal escapes (\040 space, \011
// tab, \012 newline, \134 backslash). Anything else is left as it is.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1:i+4]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if s[i] < '0' || s[i] > '7' {
			return false
		}
	}
	return s[0] <= '3'
}

// Mounts reads this process's mount table.
func Mounts() (string, error) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	return string(b), err
}

// isSocket is whether an engine socket file exists at p.
func isSocket(p string) bool {
	fi, err := os.Stat(p) // #nosec G703 -- fixed engine socket names
	return err == nil && fi.Mode()&os.ModeSocket != 0
}
