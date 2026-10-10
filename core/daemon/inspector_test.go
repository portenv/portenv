// SPDX-License-Identifier: Apache-2.0

package daemon

import "testing"

// storagePlace is the inspector's "Saves go to <place>": a place name only,
// never a path, a user or a credential.
func TestStoragePlaceNamesThePlaceOnly(t *testing.T) {
	for _, c := range []struct{ storage, want string }{
		{"", "this Mac"},
		{"/Users/me/.portenv/storage", "this Mac"},
		{"/Volumes/Backup Drive/portenv", "Backup Drive"},
		{"sftp:portenv-storage@test-server.example:/storage", "test-server.example"},
		{"sftp:host-only:/srv/boxes", "host-only"},
		{"s3:https://s3.eu-west-3.amazonaws.com/my-bucket", "s3.eu-west-3.amazonaws.com"},
		{"s3:s3.amazonaws.com/bucket", "s3.amazonaws.com"},
		{"something-new:x", "your storage"},
	} {
		if got := storagePlace(c.storage); got != c.want {
			t.Errorf("storagePlace(%q) = %q, want %q", c.storage, got, c.want)
		}
	}
}
