// SPDX-License-Identifier: Apache-2.0

package driver

import "strings"

// Hostname is the box's hostname: its name made valid (lowercase letters,
// digits and hyphens, at most 63 characters, no leading or trailing
// hyphen), so the prompt reads work@acme-api. A name with nothing usable
// left falls back to box-<first 8 of the id>. The name itself, unchanged,
// is what the app shows everywhere else.
func Hostname(name string, id BoxID) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	h := b.String()
	if len(h) > 63 {
		h = h[:63]
	}
	if h = strings.Trim(h, "-"); h != "" {
		return h
	}
	short := strings.TrimPrefix(string(id), "box-")
	if len(short) > 8 {
		short = short[:8]
	}
	return "box-" + short
}
