// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"slices"
	"strings"
	"testing"
)

// The built-in templates, in the order portenv team init and the New
// workspace picker list them (R-0338 adds app-product).
func TestTheBuiltInTemplates(t *testing.T) {
	if want := []string{"starter", "orchestrate", "app-product"}; !slices.Equal(Templates, want) {
		t.Fatalf("Templates = %v, want %v", Templates, want)
	}
	want := map[string][]string{
		"starter":     {"builder.md", "lead.md", "reviewer.md"},
		"orchestrate": {"builder.md", "design.md", "lead.md", "reviewer.md"},
		"app-product": {
			"design-system.md", "designer.md", "devops.md", "lead.md",
			"off/social.md", "release.md", "reviewer.md", "trust-safety.md", "writer.md",
		},
	}
	for _, name := range Templates {
		if got := TemplateFiles(name); !slices.Equal(got, want[name]) {
			t.Errorf("%s: files %v, want %v", name, got, want[name])
		}
		files, err := Template(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for f, b := range files {
			if !strings.HasPrefix(string(b), "---\n") {
				t.Errorf("%s/%s doesn't start with its frontmatter", name, f)
			}
		}
	}
	if _, err := Template("nope"); err == nil {
		t.Error("an unknown template has files")
	}
}

// An agent that's off by default lives in off/, which Portenv's loader
// and the helper skip: only <id>.md directly in .portenv/agents/ is an
// agent. Turning one on is moving its file up a folder.
func TestOffAgentsAreInTheOffFolder(t *testing.T) {
	for _, name := range Templates {
		for _, f := range TemplateFiles(name) {
			dir, base, sub := strings.Cut(f, "/")
			if sub && (dir != OffDir || strings.Contains(base, "/")) {
				t.Errorf("%s/%s: only %s/ may hold a template's files", name, f, OffDir)
			}
		}
		on, off := OnAgents(name), OffAgents(name)
		if len(on) == 0 || len(on) > MaxTeamAgents {
			t.Errorf("%s: %d agents on by default; at most %d", name, len(on), MaxTeamAgents)
		}
		for _, id := range off {
			if slices.Contains(on, id) {
				t.Errorf("%s: %s is both on and off", name, id)
			}
		}
	}
	if got := OffAgents("app-product"); !slices.Equal(got, []string{"social"}) {
		t.Errorf("app-product's off agents: %v", got)
	}
}
