// SPDX-License-Identifier: Apache-2.0

package boundary

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const recoveryPkg = "github.com/portenv/portenv/core/keys/recovery"

// deps lists the packages pkg depends on, itself included.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}

// The daemons receive derived recovery passwords and never derive one, so
// none of them links the recovery key's package (ADR 0019, test 8).
func TestDaemonsNeverImportTheRecoveryKey(t *testing.T) {
	if !slices.Contains(deps(t, "./keys/recovery"), recoveryPkg) {
		t.Fatal("the check can't see the package")
	}
	for _, cmd := range []string{"./cmd/portenvd", "./cmd/portenv-runner", "./cmd/portenv-agent"} {
		if slices.Contains(deps(t, cmd), recoveryPkg) {
			t.Errorf("%s depends on %s", cmd, recoveryPkg)
		}
	}
}
