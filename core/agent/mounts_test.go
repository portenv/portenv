// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strings"
	"testing"
)

// line is one /proc/self/mountinfo line: root is the path inside the
// mounted file system, at is where it's mounted in the box.
func line(root, at string) string {
	return "100 90 0:50 " + root + " " + at + " rw,relatime - fakeowner /dev/sda1 rw\n"
}

const baseMounts = "90 1 0:40 / / rw - overlay overlay rw\n" +
	"91 90 0:41 / /proc rw - proc proc rw\n" +
	"92 90 0:42 / /dev rw - tmpfs tmpfs rw\n" +
	"93 90 0:43 / /home rw - ext4 /dev/vda1 rw\n" +
	"94 90 0:44 / /var/cache/portenv-sync rw - ext4 /dev/vda1 rw\n"

// TestTheHelperRefusesOtherWorkspacesSaves (R-0201, ruling 2): the helper
// starts only when the storage mount holds its own repository and nothing
// else, and says so in plain words when it doesn't.
func TestTheHelperRefusesOtherWorkspacesSaves(t *testing.T) {
	const id = "box-0123456789ab"
	for name, mounts := range map[string]string{
		"no storage (SFTP, S3)":   baseMounts,
		"this Mac's own":          baseMounts + line("/Repositories/"+id, StorageMount+"/"+id),
		"a drive's own":           baseMounts + line("/boxes/"+id, StorageMount+"/boxes/"+id),
		"a path with a space":     baseMounts + line(`/Application\040Support/Portenv/Repositories/`+id, StorageMount+"/"+id),
		"a cache outside storage": baseMounts + line("/x", "/var/tmp/x"),
	} {
		if err := checkMounts(mounts, id, noSockets); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, mounts := range map[string]string{
		"every workspace's (the old mount)": baseMounts + line("/Repositories", StorageMount),
		"a drive's boxes folder":            baseMounts + line("/boxes", StorageMount+"/boxes"),
		"another workspace's":               baseMounts + line("/Repositories/box-ffffffffffff", StorageMount+"/box-ffffffffffff"),
		"its own and another's":             baseMounts + line("/Repositories/"+id, StorageMount+"/"+id) + line("/Repositories/box-ffffffffffff", StorageMount+"/box-ffffffffffff"),
		"something inside its own":          baseMounts + line("/Repositories/"+id, StorageMount+"/"+id) + line("/Repositories", StorageMount+"/"+id+"/all"),
		"a look-alike":                      baseMounts + line("/Repositories/"+id+"x", StorageMount+"/"+id+"x"),
		"escaped dots":                      baseMounts + line("/Repositories", StorageMount+`/boxes/..`),
	} {
		err := checkMounts(mounts, id, noSockets)
		if err == nil {
			t.Errorf("%s: started", name)
			continue
		}
		if !strings.Contains(err.Error(), "other workspaces") {
			t.Errorf("%s: %q doesn't say why in plain words", name, err)
		}
	}
	// A helper not told its box can't tell which repository is its own.
	if err := checkMounts(baseMounts+line("/Repositories/"+id, StorageMount+"/"+id), "", noSockets); err == nil {
		t.Error("a storage mount was accepted without knowing the box")
	}
	if err := checkMounts(baseMounts, "", noSockets); err != nil {
		t.Errorf("no storage, no box ID: %v", err)
	}
}

// TestTheHelperRefusesAnEngineSocket (R-0201, ruling 2): any container
// engine's socket, mounted on its own or inside a mounted folder, stops
// the start, in plain words.
func TestTheHelperRefusesAnEngineSocket(t *testing.T) {
	const id = "box-0123456789ab"
	for name, c := range map[string]struct {
		mounts  string
		sockets map[string]bool
	}{
		"docker.sock on its own":       {baseMounts + line("/docker.sock", "/var/run/docker.sock"), nil},
		"at another name":              {baseMounts + line("/run/docker.sock", "/tmp/engine"), nil},
		"containerd":                   {baseMounts + line("/containerd/containerd.sock", "/run/containerd/containerd.sock"), nil},
		"podman":                       {baseMounts + line("/podman/podman.sock", "/home/work/podman.sock"), nil},
		"inside a mounted folder":      {baseMounts + line("/var/run", "/hostrun"), map[string]bool{"/hostrun/docker.sock": true}},
		"at the usual path, unmounted": {baseMounts, map[string]bool{"/var/run/docker.sock": true}},
	} {
		has := func(p string) bool { return c.sockets[p] }
		err := checkMounts(c.mounts, id, has)
		if err == nil {
			t.Errorf("%s: started", name)
			continue
		}
		if !strings.Contains(err.Error(), "control of the machine") {
			t.Errorf("%s: %q doesn't say why in plain words", name, err)
		}
	}
	if err := checkMounts(baseMounts+line("/notes", "/home/work/docker.socket.md"), id, noSockets); err != nil {
		t.Errorf("a file that only looks like a socket name: %v", err)
	}
}

// TestMountinfoUnescapes: mountinfo escapes spaces, tabs, newlines and
// backslashes as octal.
func TestMountinfoUnescapes(t *testing.T) {
	for in, want := range map[string]string{
		`/a\040b`: "/a b",
		`/a\011b`: "/a\tb",
		`/a\012b`: "/a\nb",
		`/a\134b`: `/a\b`,
		`/plain`:  "/plain",
		`/odd\04`: `/odd\04`,
	} {
		if got := unescapeMountinfo(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func noSockets(string) bool { return false }
