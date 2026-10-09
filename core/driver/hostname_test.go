// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"strings"
	"testing"
)

// TestHostnameIsTheBoxName: the prompt reads work@acme-api, so people and
// agents can tell which box they're in. The name becomes a valid hostname
// (lowercase letters, digits and hyphens, at most 63 characters, no leading
// or trailing hyphen), else box-<first 8 of the id>.
func TestHostnameIsTheBoxName(t *testing.T) {
	const id = BoxID("box-4a858db43165")
	for _, c := range []struct{ name, want string }{
		{"acme-api", "acme-api"},
		{"Acme API", "acme-api"},
		{"my_box.v2", "my-box-v2"},
		{"--acme--api--", "acme-api"},
		{"café", "caf"},
		{"2026", "2026"},
		{"", "box-4a858db4"},
		{"---", "box-4a858db4"},
		{"日本語", "box-4a858db4"},
		{strings.Repeat("a", 70), strings.Repeat("a", 63)},
		{strings.Repeat("a", 62) + "-b", strings.Repeat("a", 62)},
	} {
		got := Hostname(c.name, id)
		if got != c.want {
			t.Errorf("Hostname(%q) = %q, want %q", c.name, got, c.want)
		}
		if !validHostname(got) {
			t.Errorf("Hostname(%q) = %q is not a valid hostname", c.name, got)
		}
	}
}

func validHostname(h string) bool {
	if h == "" || len(h) > 63 || h[0] == '-' || h[len(h)-1] == '-' {
		return false
	}
	for _, r := range h {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
