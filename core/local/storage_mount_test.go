// SPDX-License-Identifier: Apache-2.0

package local

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/portenv/portenv/core/driver/docker"
)

// TestStorageMountsOnlyTheBoxsOwnRepository (R-0201): local storage gives
// the driver the box's own repository folder, never the folder holding
// every box's (this Mac's Repositories, a drive's boxes folder). The
// repository's path inside the box doesn't change, so saves, restic's cache
// and resume work as before. Two boxes' folders never overlap.
func TestStorageMountsOnlyTheBoxsOwnRepository(t *testing.T) {
	e := &Env{Dir: t.TempDir()}
	drive := t.TempDir()
	for _, c := range []struct {
		name, storage string
		dir           func(id string) string
		repo          func(id string) string
	}{
		{"this Mac", "", func(id string) string { return filepath.Join(e.Dir, "Repositories", id) }, func(id string) string { return docker.StorageMount + "/" + id }},
		{"a drive", drive, func(id string) string { return filepath.Join(drive, "boxes", id) }, func(id string) string { return docker.StorageMount + "/boxes/" + id }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var dirs []string
			for _, id := range []string{"box-0123456789ab", "box-ffffffffffff"} {
				repo, dir, _, err := e.Storage(BoxConfig{ID: id, Storage: c.storage})
				if err != nil {
					t.Fatal(err)
				}
				if dir != c.dir(id) || repo != c.repo(id) {
					t.Fatalf("%s: mounts %s at %s, want %s at %s", id, dir, repo, c.dir(id), c.repo(id))
				}
				dirs = append(dirs, dir)
			}
			a, b := dirs[0]+string(filepath.Separator), dirs[1]+string(filepath.Separator)
			if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
				t.Fatalf("two boxes' storage folders overlap: %s and %s", dirs[0], dirs[1])
			}
		})
	}
	// SFTP and S3 mount nothing.
	for _, s := range []string{"sftp:u@h:/storage", "s3:https://s3.example.com/bucket"} {
		if _, dir, _, err := e.Storage(BoxConfig{ID: "box-0123456789ab", Storage: s, StorageHostKey: "ssh-ed25519 AAAA"}); err != nil || dir != "" {
			t.Errorf("%s: mounts %q (%v)", s, dir, err)
		}
	}
}
