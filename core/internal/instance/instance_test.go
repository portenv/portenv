// SPDX-License-Identifier: Apache-2.0

package instance

import "testing"

// A release build (no PORTENV_INSTANCE) keeps today's names exactly; a dev
// lane's build gets its own Keychain service, support folder and log folder.
func TestNames(t *testing.T) {
	for _, c := range []struct {
		inst, service, folder string
	}{
		{"", "dev.portenv.repository", "Portenv"},
		{"dev-A", "dev.portenv.repository.dev-A", "Portenv-dev-A"},
		{"dev-C", "dev.portenv.repository.dev-C", "Portenv-dev-C"},
	} {
		n, err := parse(c.inst)
		if err != nil {
			t.Fatalf("%q: %v", c.inst, err)
		}
		if got := n.KeychainService(); got != c.service {
			t.Errorf("%q: KeychainService = %q, want %q", c.inst, got, c.service)
		}
		if got := n.Folder(); got != c.folder {
			t.Errorf("%q: Folder = %q, want %q", c.inst, got, c.folder)
		}
	}
}

// Anything but dev-<one capital letter> is refused: an instance name ends up
// in paths and a Keychain service, so it never takes slashes, dots or spaces.
func TestInvalidRefused(t *testing.T) {
	for _, bad := range []string{"dev", "dev-", "dev-b", "dev-AB", "prod", "dev-A/..", "../x", " dev-A", "dev-A ", "DEV-A"} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFromEnvironment(t *testing.T) {
	t.Setenv(Env, "dev-B")
	n, err := Current()
	if err != nil || n.Folder() != "Portenv-dev-B" {
		t.Fatalf("Current() = %+v, %v", n, err)
	}
	t.Setenv(Env, "")
	n, err = Current()
	if err != nil || n.Folder() != "Portenv" {
		t.Fatalf("empty: Current() = %+v, %v", n, err)
	}
	t.Setenv(Env, "nope")
	if _, err := Current(); err == nil {
		t.Fatal("an invalid PORTENV_INSTANCE was accepted")
	}
}
