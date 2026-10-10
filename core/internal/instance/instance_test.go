// SPDX-License-Identifier: Apache-2.0

package instance

import "testing"

// The names a build uses: a release build keeps today's names; a dev lane's
// build gets its own Keychain service, support folder and log folder.
func TestNames(t *testing.T) {
	for _, c := range []struct {
		stamp, service, folder string
	}{
		{"", "dev.portenv.repository", "Portenv"},
		{"dev-A", "dev.portenv.repository.dev-A", "Portenv-dev-A"},
		{"dev-C", "dev.portenv.repository.dev-C", "Portenv-dev-C"},
	} {
		restore := SetStampForTesting(c.stamp)
		t.Setenv(Env, "")
		n, err := Current()
		restore()
		if err != nil {
			t.Fatalf("%q: %v", c.stamp, err)
		}
		if got := n.KeychainService(); got != c.service {
			t.Errorf("%q: KeychainService = %q, want %q", c.stamp, got, c.service)
		}
		if got := n.Folder(); got != c.folder {
			t.Errorf("%q: Folder = %q, want %q", c.stamp, got, c.folder)
		}
	}
}

// A release binary ignores PORTENV_INSTANCE entirely: setting it can never
// move a release build onto a dev Keychain service or folder.
func TestReleaseIgnoresTheEnvironment(t *testing.T) {
	defer SetStampForTesting("")()
	for _, v := range []string{"dev-B", "dev-C", "nonsense"} {
		t.Setenv(Env, v)
		n, err := Current()
		if err != nil || n.String() != "" || n.Folder() != "Portenv" {
			t.Errorf("PORTENV_INSTANCE=%q: release build got %+v, %v", v, n, err)
		}
	}
}

// A dev binary carries its lane; the environment may only agree with it.
func TestDevRefusesAMismatch(t *testing.T) {
	defer SetStampForTesting("dev-C")()
	for _, ok := range []string{"", "dev-C"} {
		t.Setenv(Env, ok)
		if n, err := Current(); err != nil || n.String() != "dev-C" {
			t.Errorf("PORTENV_INSTANCE=%q: got %+v, %v", ok, n, err)
		}
	}
	for _, bad := range []string{"dev-B", "nonsense"} {
		t.Setenv(Env, bad)
		if _, err := Current(); err == nil {
			t.Errorf("PORTENV_INSTANCE=%q accepted by a dev-C build", bad)
		}
	}
}

// A malformed stamp (a bad -ldflags) is refused, never treated as release.
func TestInvalidStampRefused(t *testing.T) {
	for _, bad := range []string{"dev", "dev-b", "dev-AB", "prod", "../x", "dev-A "} {
		restore := SetStampForTesting(bad)
		t.Setenv(Env, "")
		if _, err := Current(); err == nil {
			t.Errorf("stamp %q accepted", bad)
		}
		restore()
	}
}
