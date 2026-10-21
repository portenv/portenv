// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func planned(t *testing.T, ps []PlannedAgent, id string) PlannedAgent {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no planned agent %q", id)
	return PlannedAgent{}
}

func fmLine(t *testing.T, content []byte, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// With no choices, every agent is as its template has it, except the
// team_status cap (ADR 0025 §7): unticked by default.
func TestPlanDefaultsAreTheTemplate(t *testing.T) {
	for _, name := range Templates {
		files, err := Template(name)
		if err != nil {
			t.Fatal(err)
		}
		ps, err := Plan(name, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(ps) != len(files) {
			t.Fatalf("%s: %d planned, %d files", name, len(ps), len(files))
		}
		for _, p := range ps {
			want := files[p.Path]
			if p.TemplateTeamStatus {
				want = bytes.Replace(want, []byte("  team_status: read\n"), nil, 1)
			}
			if !bytes.Equal(p.Content, want) {
				t.Errorf("%s/%s: content differs from the template\n%s", name, p.ID, p.Content)
			}
			if p.Included != p.TemplateIncluded || p.Included == strings.HasPrefix(p.Path, OffDir+"/") {
				t.Errorf("%s/%s: included %v at %s", name, p.ID, p.Included, p.Path)
			}
			if p.SHA256 == "" || p.Runner != "claude" {
				t.Errorf("%s/%s: sha %q runner %q", name, p.ID, p.SHA256, p.Runner)
			}
		}
	}
}

func TestPlanStarterDefaults(t *testing.T) {
	ps, err := Plan("starter", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(ids, []string{"lead", "builder", "reviewer"}) {
		t.Fatalf("order %v", ids)
	}
	b := planned(t, ps, "builder")
	if b.Path != "builder.md" || b.Name != "Builder" || b.Model != "" || b.TemplateModel != "" || len(b.Changes) != 0 {
		t.Fatalf("%+v", b)
	}
	if !slices.Equal(b.Models, []string{"", "opus", "sonnet", "fable"}) {
		t.Fatalf("models %v", b.Models)
	}
}

// ADR 0025 §12's gate, on today's Orchestrate: the reviewer's model to
// Opus, DevOps left out. DevOps goes to off/ untouched; nobody else lists
// it; the reviewer's runner change is shown.
func TestPlanAdjusted(t *testing.T) {
	ps, err := Plan("orchestrate", []AgentChoice{
		{ID: "reviewer", Include: true, Model: "opus"},
		{ID: "devops", Include: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := planned(t, ps, "reviewer")
	if got := fmLine(t, r.Content, "runner:"); got != "runner: claude --model opus --disallowedTools Edit,Write,NotebookEdit" {
		t.Fatalf("runner line %q", got)
	}
	if r.Model != "opus" || r.TemplateModel != "fable" || !slices.Contains(r.Changes, "runner claude (fable) \u2192 claude (opus)") {
		t.Fatalf("%+v", r)
	}
	d := planned(t, ps, "devops")
	tmpl, _ := Template("orchestrate")
	if d.Included || d.Path != "off/devops.md" || !bytes.Equal(d.Content, tmpl["devops.md"]) {
		t.Fatalf("devops %+v", d)
	}
	for _, id := range []string{"coordinator", "builder-a", "builder-b", "builder-c"} {
		p := planned(t, ps, id)
		if strings.Contains(fmLine(t, p.Content, "  message:"), "devops") {
			t.Errorf("%s still messages devops: %s", id, fmLine(t, p.Content, "  message:"))
		}
		if len(p.Changes) == 0 {
			t.Errorf("%s: its message change isn't shown", id)
		}
	}
	// Untouched agents keep their exact bytes.
	if c := planned(t, ps, "reviewer"); !strings.Contains(string(c.Content), "read_clones: [builder-a, builder-b, builder-c]") {
		t.Fatalf("reviewer's read_clones changed:\n%s", c.Content)
	}
}

// Leaving out a builder takes it out of read_clones too.
func TestPlanLeaveOutPrunesReadClones(t *testing.T) {
	ps, err := Plan("starter", []AgentChoice{{ID: "builder", Include: false}})
	if err != nil {
		t.Fatal(err)
	}
	r := planned(t, ps, "reviewer")
	if strings.Contains(string(r.Content), "read_clones") || strings.Contains(fmLine(t, r.Content, "  message:"), "builder") {
		t.Fatalf("reviewer still names builder:\n%s", r.Content)
	}
}

// An off agent turned on moves up a folder, and the agents it messages
// list it back (it's shown as their change).
func TestPlanTurnOnAnOffAgent(t *testing.T) {
	ps, err := Plan("orchestrate", []AgentChoice{{ID: "design-system", Include: true}})
	if err != nil {
		t.Fatal(err)
	}
	d := planned(t, ps, "design-system")
	if !d.Included || d.Path != "design-system.md" || d.TemplateIncluded {
		t.Fatalf("%+v", d)
	}
	c := planned(t, ps, "coordinator")
	if got := fmLine(t, c.Content, "  message:"); got != "  message: [reviewer, builder-a, builder-b, builder-c, devops, design-system]" {
		t.Fatalf("coordinator %q", got)
	}
	if len(c.Changes) == 0 {
		t.Fatal("coordinator's change isn't shown")
	}
}

func TestPlanRenameAndTeamStatus(t *testing.T) {
	ps, err := Plan("orchestrate", []AgentChoice{
		{ID: "coordinator", Include: true, Name: "Lead (ops)", TeamStatus: true},
		{ID: "builder-a", Include: true, TeamStatus: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := planned(t, ps, "coordinator")
	tmpl, _ := Template("orchestrate")
	want := strings.Replace(string(tmpl["coordinator.md"]), "name: Coordinator\n", "name: Lead (ops)\n", 1)
	if string(c.Content) != want || c.Name != "Lead (ops)" || !c.TeamStatus {
		t.Fatalf("coordinator:\n%s", c.Content)
	}
	b := planned(t, ps, "builder-a")
	if fmLine(t, b.Content, "  team_status:") != "  team_status: read" || !slices.ContainsFunc(b.Changes, func(s string) bool { return strings.Contains(s, "team_status") }) {
		t.Fatalf("builder-a:\n%s %v", b.Content, b.Changes)
	}
	// Unticked: the template's team_status is taken out, and that's shown.
	ps, _ = Plan("orchestrate", nil)
	c = planned(t, ps, "coordinator")
	if strings.Contains(string(c.Content), "team_status") || !c.TemplateTeamStatus || c.TeamStatus || len(c.Changes) != 1 {
		t.Fatalf("coordinator unticked:\n%s %v", c.Content, c.Changes)
	}
}

func TestPlanRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		choices  []AgentChoice
		want     string
	}{
		{"unknown template", "nope", nil, "no template"},
		{"unknown agent", "starter", []AgentChoice{{ID: "ghost", Include: true}}, "ghost"},
		{"twice", "starter", []AgentChoice{{ID: "lead", Include: true}, {ID: "lead", Include: true}}, "twice"},
		{"model not the runner's", "starter", []AgentChoice{{ID: "lead", Include: true, Model: "gpt-5"}}, "gpt-5"},
		{"empty name", "starter", []AgentChoice{{ID: "lead", Include: true, Name: " "}}, "name"},
		{"long name", "starter", []AgentChoice{{ID: "lead", Include: true, Name: strings.Repeat("a", 41)}}, "40"},
		{"bidi name", "starter", []AgentChoice{{ID: "lead", Include: true, Name: "Le\u202Ead"}}, "name"},
		{"yaml in name", "starter", []AgentChoice{{ID: "lead", Include: true, Name: "a: b"}}, "name"},
		{"newline name", "starter", []AgentChoice{{ID: "lead", Include: true, Name: "a\nrunner: sh"}}, "name"},
	} {
		if _, err := Plan(tc.template, tc.choices); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// Section 7's caps on what a template file may ask for.
func TestCapAgentFile(t *testing.T) {
	file := "---\nid: x\nname: X\nrunner: claude\nshare_login: [claude]\ngrant:\n  panes: allow\n  max_panes: 2\n---\nhi\n"
	got, changes, err := capFile([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "share_login") || !strings.Contains(string(got), "  panes: ask\n") || len(changes) != 2 {
		t.Fatalf("%s %v", got, changes)
	}
	for _, bad := range []string{
		"---\nid: x\nname: X\nrunner: remote\nvia: ssh\n---\n",
		"---\nid: x\nname: X\nrunner: /bin/sh -c x\n---\n",
		"---\nid: x\nname: X\nrunner: codex\n---\n",
		"---\nid: x\nname: X\n---\n",
	} {
		if _, _, err := capFile([]byte(bad)); err == nil {
			t.Errorf("not refused:\n%s", bad)
		}
	}
}

func TestTemplateSummaries(t *testing.T) {
	s := TemplateSummaries()
	if len(s) != 3 || s[0].ID != "starter" || s[0].Name != "Starter" || s[1].ID != "app-product" || s[1].Name != "App product" || s[2].ID != "orchestrate" {
		t.Fatalf("%+v", s)
	}
	if s[0].Description != "Lead, Builder, Reviewer" {
		t.Fatalf("%q", s[0].Description)
	}
}
