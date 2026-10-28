// SPDX-License-Identifier: Apache-2.0

// Package tzname finds the Mac's time zone as an IANA name, for the
// workspace's clock (R-0411), and checks a name before it goes anywhere
// near a path: a workspace links /etc/localtime to it.
package tzname

import (
	"os"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // Go's own tz database: a name is checked the same on every Mac
)

// UTC is the fallback for any name that isn't a known IANA zone.
const UTC = "UTC"

// maxLen is longer than any IANA name (the longest is 32 bytes).
const maxLen = 64

// nameRE is an IANA zone name: parts of letters, digits, "_", "+" and
// "-", joined by single slashes.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)

// Valid says whether name has an IANA zone name's shape: never "Local",
// never a "." or ".." part, never a leading or trailing slash. It doesn't
// say the zone exists; the tz database does.
func Valid(name string) bool {
	if len(name) == 0 || len(name) > maxLen || !nameRE.MatchString(name) {
		return false
	}
	if strings.EqualFold(name, "Local") || strings.Contains(name, "..") {
		return false
	}
	return true
}

// FromLink is the zone name in an /etc/localtime link's target (the part
// after the last "zoneinfo/"), or "" when it holds none.
func FromLink(target string) string {
	i := strings.LastIndex(target, "zoneinfo/")
	if i < 0 {
		return ""
	}
	name := target[i+len("zoneinfo/"):]
	if !Valid(name) {
		return ""
	}
	return name
}

// Mac is this Mac's time zone, as an IANA name Go's tz database knows, or
// UTC.
func Mac() string {
	return mac(os.Readlink, time.Local.String(), load)
}

func mac(readlink func(string) (string, error), local string, load func(string) error) string {
	if target, err := readlink("/etc/localtime"); err == nil {
		if name := FromLink(target); name != "" && load(name) == nil {
			return name
		}
	}
	if Valid(local) && load(local) == nil {
		return local
	}
	return UTC
}

// load checks name against the tz database.
func load(name string) error {
	_, err := time.LoadLocation(name)
	return err
}
