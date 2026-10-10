// SPDX-License-Identifier: Apache-2.0

package local

import (
	"os"
	"path/filepath"
	"testing"
)

// Each dev lane's build keeps its own support folder (state, saves, the
// socket); a release build keeps Application Support/Portenv; an invalid
// instance never falls back to the shared folder.
func TestNewEnvFolderPerInstance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "Library", "Application Support"))
	t.Setenv("PORTENV_HOME", "")
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	for inst, want := range map[string]string{"": "Portenv", "dev-B": "Portenv-dev-B"} {
		t.Setenv("PORTENV_INSTANCE", inst)
		e, err := NewEnv()
		if err != nil {
			t.Fatalf("%q: %v", inst, err)
		}
		if e.Dir != filepath.Join(cfg, want) {
			t.Errorf("%q: Dir = %s, want %s", inst, e.Dir, filepath.Join(cfg, want))
		}
	}
	t.Setenv("PORTENV_INSTANCE", "dev-b")
	if _, err := NewEnv(); err == nil {
		t.Error("an invalid PORTENV_INSTANCE fell back to a folder")
	}
}
