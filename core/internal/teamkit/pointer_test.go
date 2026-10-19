// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"strings"
	"testing"
)

// R-0327: every template's agent is told to read the guide at its start.
func TestEveryTemplateSaysToReadTheGuideFirst(t *testing.T) {
	for _, name := range Templates {
		files, err := Template(name)
		if err != nil {
			t.Fatal(err)
		}
		for file, b := range files {
			s := strings.Join(strings.Fields(string(b)), " ")
			if !strings.Contains(s, "Start by running `portenv whoami` and `portenv guide`, and check `portenv inbox` after each step.") {
				t.Errorf("%s/%s doesn't say to run portenv guide first", name, file)
			}
		}
	}
}

func TestThePointerIsFixedText(t *testing.T) {
	for _, rel := range PointerFiles {
		s := PointerText("builder", rel)
		if !strings.HasPrefix(s, PointerMarker+"\n") || !strings.Contains(s, FirstStepLine("builder")) || !strings.Contains(s, "~/AGENTS.md") {
			t.Errorf("%s:\n%s", rel, s)
		}
		if len(s) > 1024 {
			t.Errorf("%s's pointer is %d bytes; it's meant to be short", rel, len(s))
		}
	}
	for _, rel := range []string{"", ".bashrc", "../.claude/CLAUDE.md", ".claude/settings.json"} {
		if IsPointerFile(rel) {
			t.Errorf("IsPointerFile(%q)", rel)
		}
	}
}
