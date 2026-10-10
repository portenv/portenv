// SPDX-License-Identifier: Apache-2.0

// Package instance names what a Portenv build keeps apart from other builds
// on the same Mac: its Keychain service and its support and log folders.
//
// The instance is stamped into the binary at build time, never read from the
// environment alone. Release binaries (no stamp) always use the release
// names and ignore PORTENV_INSTANCE entirely. A dev lane's binaries (gmake …
// LANE=X, -ldflags -X …stamp=dev-X) use their lane's names, and refuse to
// run when PORTENV_INSTANCE is set to anything else.
package instance

import (
	"fmt"
	"os"
	"regexp"
)

// Env may only agree with a dev build's stamp (the app and the launch agent
// set it); a release build ignores it.
const Env = "PORTENV_INSTANCE"

// stamp is set at build time with -ldflags "-X …/instance.stamp=dev-X".
var stamp string

var nameRE = regexp.MustCompile(`^dev-[A-Z]$`)

// Name is one build's instance; the zero value is a release build.
type Name struct{ name string }

// Current is this binary's instance: release (no stamp), or its dev lane.
func Current() (Name, error) {
	if stamp == "" {
		return Name{}, nil
	}
	if !nameRE.MatchString(stamp) {
		return Name{}, fmt.Errorf("this build's instance %q is malformed (gmake … LANE=X builds dev-X)", stamp)
	}
	if v := os.Getenv(Env); v != "" && v != stamp {
		return Name{}, fmt.Errorf("this is the %s build, but %s=%s; unset it or run the %s build", stamp, Env, v, v)
	}
	return Name{name: stamp}, nil
}

// SetStampForTesting stamps the instance for a test and returns the restore.
// Tests only: real binaries are stamped by the build.
func SetStampForTesting(s string) (restore func()) {
	old := stamp
	stamp = s
	return func() { stamp = old }
}

// Stamp is this binary's build-time stamp ("" for a release build), for
// version output; it reads nothing from the environment.
func Stamp() string { return stamp }

// String is the instance ("" for a release build).
func (n Name) String() string { return n.name }

// KeychainService holds the boxes' repository keys.
func (n Name) KeychainService() string { return n.suffix("dev.portenv.repository", ".") }

// Folder names the support and log folders (Application Support/<Folder>,
// Logs/<Folder>).
func (n Name) Folder() string { return n.suffix("Portenv", "-") }

func (n Name) suffix(base, sep string) string {
	if n.name == "" {
		return base
	}
	return base + sep + n.name
}
