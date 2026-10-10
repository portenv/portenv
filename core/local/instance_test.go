// SPDX-License-Identifier: Apache-2.0

package local

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/portenv/portenv/core/internal/instance"
)

// Each dev lane's build keeps its own support folder (state, saves, and
// portenvd's socket, which the CLI and portenvd both find in Env.Dir), so a
// dev CLI never talks to the release daemon or the reverse. A release build
// keeps Application Support/Portenv whatever PORTENV_INSTANCE says.
func TestNewEnvFolderPerInstance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "Library", "Application Support"))
	t.Setenv("PORTENV_HOME", "")
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ stamp, env, want string }{
		{"", "", "Portenv"},
		{"", "dev-B", "Portenv"}, // release ignores the variable
		{"dev-B", "", "Portenv-dev-B"},
		{"dev-B", "dev-B", "Portenv-dev-B"},
	} {
		restore := instance.SetStampForTesting(c.stamp)
		t.Setenv(instance.Env, c.env)
		e, err := NewEnv()
		restore()
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if e.Dir != filepath.Join(cfg, c.want) {
			t.Errorf("%+v: Dir = %s, want %s", c, e.Dir, filepath.Join(cfg, c.want))
		}
	}
	defer instance.SetStampForTesting("dev-B")()
	t.Setenv(instance.Env, "dev-C")
	if _, err := NewEnv(); err == nil {
		t.Error("a dev-B build accepted PORTENV_INSTANCE=dev-C")
	}
}
