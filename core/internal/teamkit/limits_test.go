// SPDX-License-Identifier: Apache-2.0

package teamkit

import "testing"

// A template asks for as many agents as it turns on: its <id>.md files
// directly in the template, not a file in a subfolder (R-0347).
func TestTemplateAgents(t *testing.T) {
	for name, want := range map[string]int{"starter": 3, "orchestrate": 4, "nope": 0} {
		if got := TemplateAgents(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
}
