// SPDX-License-Identifier: Apache-2.0

// Package instance names what a Portenv build keeps apart from other builds
// on the same Mac: its Keychain service and its support and log folders.
// Release builds have no instance and keep today's names. A dev lane's build
// (gmake app LANE=X) runs with PORTENV_INSTANCE=dev-X, so parallel lanes never
// share keys, state or a login item.
package instance

import (
	"fmt"
	"os"
	"regexp"
)

// Env is the variable a dev lane's build sets (the launch agent for
// portenvd, the app for what it starts).
const Env = "PORTENV_INSTANCE"

var nameRE = regexp.MustCompile(`^dev-[A-Z]$`)

// Name is one build's instance; the zero value is a release build.
type Name struct{ name string }

// Current reads PORTENV_INSTANCE: empty is a release build, dev-<A–Z> a dev
// lane's build, and anything else an error.
func Current() (Name, error) { return parse(os.Getenv(Env)) }

func parse(s string) (Name, error) {
	if s == "" {
		return Name{}, nil
	}
	if !nameRE.MatchString(s) {
		return Name{}, fmt.Errorf("%s=%q: a dev build's instance is dev- and one capital letter (gmake app LANE=X)", Env, s)
	}
	return Name{name: s}, nil
}

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
