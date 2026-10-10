// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"strings"
	"testing"
)

// storagePlace is the inspector's "Saves go to <place>": a place name only,
// never a path, a user or a credential.
func TestStoragePlaceNamesThePlaceOnly(t *testing.T) {
	for _, c := range []struct{ storage, want string }{
		{"", "this Mac"},
		{"/Users/me/.portenv/storage", "this Mac"},
		{"/Volumes/Backup Drive/portenv", "Backup Drive"},
		// A server or a bucket is never shown by host name or IP address
		// (R-0019); servers get names in 2.3.
		{"sftp:portenv-storage@test-server.example:/storage", "your server"},
		{"sftp:portenv-storage@52.47.207.191:/storage", "your server"},
		{"sftp:host-only:/srv/boxes", "your server"},
		{"s3:https://s3.eu-west-3.amazonaws.com/my-bucket", "your bucket"},
		{"s3:s3.amazonaws.com/bucket", "your bucket"},
		{"something-new:x", "your storage"},
	} {
		if got := storagePlace(c.storage); got != c.want {
			t.Errorf("storagePlace(%q) = %q, want %q", c.storage, got, c.want)
		}
	}
}

// No storage place ever contains a host name, an IP address or a user.
func TestStoragePlaceNeverShowsAHostOrAnIP(t *testing.T) {
	for _, storage := range []string{
		"sftp:portenv-storage@52.47.207.191:/storage",
		"sftp:u@[2001:db8::1]:/storage",
		"sftp:u@test-server.example:/s",
		"s3:https://bucket.s3.amazonaws.com/x",
	} {
		got := storagePlace(storage)
		for _, bad := range []string{"52.47", "2001:db8", "test-server", "amazonaws", "@", "u@"} {
			if strings.Contains(got, bad) {
				t.Errorf("storagePlace(%q) = %q, which shows %q", storage, got, bad)
			}
		}
	}
}
