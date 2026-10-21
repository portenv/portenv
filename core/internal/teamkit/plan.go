// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A team plan (ADR 0025 §3, §4): a built-in template with the owner's
// adjustments in the New-workspace Team step, as the exact agent files
// that will be written to the project's .portenv/agents/, the same place
// and folder layout `portenv team init` writes (an agent left out goes to
// off/, which Portenv doesn't load). The bytes made here are the bytes the
// owner reads and approves, and portenvd writes and pins exactly those.

// AgentChoice is the owner's adjustment of one template agent.
type AgentChoice struct {
	ID      string
	Include bool
	// Name is the display name; empty keeps the template's.
	Name string
	// Model is one of the runner's models; empty keeps the template's.
	Model string
	// TeamStatus lets the agent see the team's status (ADR 0025 §7:
	// none unless the owner ticks it).
	TeamStatus bool
}

// PlannedAgent is one agent file as it will be written.
type PlannedAgent struct {
	ID, Name, Role, Avatar string
	TemplateName           string
	Included               bool
	TemplateIncluded       bool
	// Path is relative to .portenv/agents/: <id>.md, or off/<id>.md.
	Path    string
	Content []byte
	SHA256  string
	// Runner is the registry id; RunnerLine the file's runner line.
	Runner     string
	RunnerLine string
	// Model is the --model value, empty for the runner's own default.
	Model, TemplateModel string
	// Models is the model menu: "" (the runner's default) first when the
	// template sets none, then the runner's models.
	Models             []string
	TemplateTeamStatus bool
	TeamStatus         bool
	// OwnClone: the agent works in its own copy of the project (checkout
	// isn't none), which the workspace has to hold.
	OwnClone bool
	// Changes from the template, one plain line each.
	Changes []string
}

// MaxNameChars is a display name's length (ADR 0022 §3).
const MaxNameChars = 40

// templateOrder is how the Team step lists each template's agents.
var templateOrder = map[string][]string{
	"starter":     {"lead", "builder", "reviewer"},
	"app-product": {"lead", "writer", "reviewer", "designer", "design-system", "trust-safety", "release", "devops", "social"},
	"orchestrate": {"coordinator", "reviewer", "builder-a", "builder-b", "builder-c", "devops", "design-system"},
}

// TemplateSummary is one template as the Team step's picker shows it.
type TemplateSummary struct {
	ID, Name, Description string
}

// TemplateSummaries are the built-ins, in the picker's order.
func TemplateSummaries() []TemplateSummary {
	var out []TemplateSummary
	for _, t := range []struct{ id, name string }{{"starter", "Starter"}, {"app-product", "App product"}, {"orchestrate", "Orchestrate"}} {
		ps, err := Plan(t.id, nil)
		if err != nil {
			continue
		}
		var names []string
		for _, p := range ps {
			if p.Included {
				names = append(names, p.Name)
			}
		}
		out = append(out, TemplateSummary{ID: t.id, Name: t.name, Description: strings.Join(names, ", ")})
	}
	return out
}

type tmplAgent struct {
	id, path string
	on       bool
	content  []byte
}

// Plan makes a template's agent files with the owner's choices. Agents the
// choices don't name keep the template's defaults.
func Plan(template string, choices []AgentChoice) ([]PlannedAgent, error) {
	files, err := Template(template)
	if err != nil {
		return nil, err
	}
	var agents []tmplAgent
	for p, b := range files {
		rest, off := strings.CutPrefix(p, OffDir+"/")
		agents = append(agents, tmplAgent{id: strings.TrimSuffix(rest, ".md"), path: p, on: !off, content: b})
	}
	order := templateOrder[template]
	rank := func(id string) int {
		if i := slices.Index(order, id); i >= 0 {
			return i
		}
		return len(order)
	}
	sort.Slice(agents, func(i, j int) bool {
		if ri, rj := rank(agents[i].id), rank(agents[j].id); ri != rj {
			return ri < rj
		}
		return agents[i].id < agents[j].id
	})

	chosen := map[string]AgentChoice{}
	for _, c := range choices {
		if !slices.ContainsFunc(agents, func(a tmplAgent) bool { return a.id == c.ID }) {
			return nil, fmt.Errorf("the %s team has no agent %q", template, c.ID)
		}
		if _, dup := chosen[c.ID]; dup {
			return nil, fmt.Errorf("%s is chosen twice", c.ID)
		}
		chosen[c.ID] = c
	}
	choice := func(a tmplAgent) AgentChoice {
		if c, ok := chosen[a.id]; ok {
			return c
		}
		return AgentChoice{ID: a.id, Include: a.on}
	}
	included := map[string]bool{}
	for _, a := range agents {
		if choice(a).Include {
			included[a.id] = true
		}
	}

	// Each agent's file after the caps, and who an off agent turned on
	// messages, so they can answer it.
	type capped struct {
		fm      *frontmatter
		changes []string
	}
	caps := map[string]capped{}
	answer := map[string][]string{}
	for _, a := range agents {
		out, changes, err := capFile(a.content)
		if err != nil {
			return nil, fmt.Errorf("the %s team's %s: %w", template, a.id, err)
		}
		fm, _ := parseFM(out)
		caps[a.id] = capped{fm, changes}
		if included[a.id] && !a.on {
			for _, to := range fm.grantList("message") {
				if included[to] {
					answer[to] = append(answer[to], a.id)
				}
			}
		}
	}

	var out []PlannedAgent
	for _, a := range agents {
		c := choice(a)
		cp := caps[a.id]
		fm := cp.fm
		runnerLine := fm.get("runner")
		argv := strings.Fields(runnerLine)
		runner, _ := RunnerByID(argv[0])
		tmplModel := modelOf(argv, runner.ModelFlag)
		p := PlannedAgent{
			ID: a.id, Name: fm.get("name"), TemplateName: fm.get("name"), Role: fm.get("role"), Avatar: fm.get("avatar"),
			Included: c.Include, TemplateIncluded: a.on, Runner: runner.ID,
			Model: tmplModel, TemplateModel: tmplModel, TemplateTeamStatus: fm.grant("team_status") == "read",
			OwnClone: fm.get("checkout") != "none",
		}
		if tmplModel == "" {
			p.Models = append(p.Models, "")
		}
		p.Models = append(p.Models, runner.Models...)
		if !c.Include {
			// Left out: the template's file, untouched, where Portenv
			// doesn't load it (team init's off/ folder).
			p.Path, p.Content, p.RunnerLine = OffDir+"/"+a.id+".md", a.content, runnerLine
			p.SHA256 = sum(p.Content)
			out = append(out, p)
			continue
		}
		p.Path = a.id + ".md"
		p.Changes = append(p.Changes, cp.changes...)
		if n := strings.TrimSpace(c.Name); c.Name != "" && n != p.TemplateName {
			if err := checkName(c.Name); err != nil {
				return nil, fmt.Errorf("%s's name: %w", a.id, err)
			}
			fm.set("name", n)
			p.Name = n
			p.Changes = append(p.Changes, fmt.Sprintf("name %s → %s", p.TemplateName, n))
		}
		if c.Model != "" && c.Model != tmplModel {
			if !slices.Contains(runner.Models, c.Model) {
				return nil, fmt.Errorf("%s: %s has no model %q", a.id, runner.Name, c.Model)
			}
			argv = setModel(argv, runner.ModelFlag, c.Model)
			fm.set("runner", strings.Join(argv, " "))
			p.Model = c.Model
			p.Changes = append(p.Changes, fmt.Sprintf("runner %s (%s) → %s (%s)", runner.ID, modelWord(tmplModel), runner.ID, c.Model))
		}
		for _, key := range []string{"message", "read_clones"} {
			was, ok := fm.grantListOK(key)
			if !ok {
				continue
			}
			var now []string
			for _, id := range was {
				if included[id] {
					now = append(now, id)
				}
			}
			if key == "message" {
				for _, id := range answer[a.id] {
					if !slices.Contains(now, id) {
						now = append(now, id)
					}
				}
			}
			if slices.Equal(was, now) {
				continue
			}
			if len(now) == 0 {
				fm.removeGrant(key)
			} else {
				fm.setGrant(key, "["+strings.Join(now, ", ")+"]")
			}
			p.Changes = append(p.Changes, fmt.Sprintf("%s %s → %s", key, listWord(was), listWord(now)))
		}
		p.TeamStatus = c.TeamStatus
		switch {
		case p.TemplateTeamStatus && !c.TeamStatus:
			fm.removeGrant("team_status")
			p.Changes = append(p.Changes, "team_status read → none (tick it to let this agent see the team’s status)")
		case !p.TemplateTeamStatus && c.TeamStatus:
			fm.setGrant("team_status", "read")
			p.Changes = append(p.Changes, "team_status none → read")
		}
		p.RunnerLine = fm.get("runner")
		p.Content = fm.bytes()
		p.SHA256 = sum(p.Content)
		out = append(out, p)
	}
	return out, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func modelWord(m string) string {
	if m == "" {
		return "default"
	}
	return m
}

func listWord(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return "[" + strings.Join(l, ", ") + "]"
}

// checkName is a display name the Team step may write: plain words, 1 to
// 40 characters, that the agent-file reader takes as they are.
func checkName(s string) error {
	n := strings.TrimSpace(s)
	if c := utf8.RuneCountInString(n); c < 1 || c > MaxNameChars {
		return fmt.Errorf("a name is 1 to %d characters", MaxNameChars)
	}
	for i, r := range n {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || (i > 0 && strings.ContainsRune(" -'.&()/+", r))
		if !ok {
			return fmt.Errorf("a name has letters, numbers, spaces and - ' . & ( ) / +, and starts with a letter or number; %q has %q", n, r)
		}
	}
	return nil
}

// modelOf is the value after the model flag, or "".
func modelOf(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
	}
	return ""
}

// setModel sets the model flag's value, or adds the flag after the
// program.
func setModel(argv []string, flag, model string) []string {
	out := slices.Clone(argv)
	for i, a := range out {
		if a == flag && i+1 < len(out) {
			out[i+1] = model
			return out
		}
		if strings.HasPrefix(a, flag+"=") {
			out[i] = flag + "=" + model
			return out
		}
	}
	return slices.Insert(out, 1, flag, model)
}

// capFile applies ADR 0025 §7's caps to a template's agent file: panes
// and packages at most ask, no share_login, and a runner that's a shown
// registry entry (no remote, no other program). A capped grant is a
// change; anything else is a refusal.
func capFile(content []byte) ([]byte, []string, error) {
	fm, err := parseFM(content)
	if err != nil {
		return nil, nil, err
	}
	var changes []string
	line := fm.get("runner")
	switch argv := strings.Fields(line); {
	case line == "":
		return nil, nil, fmt.Errorf("it has no runner")
	case line == "remote" || fm.has("via"):
		return nil, nil, fmt.Errorf("a team template can't have a remote runner")
	case strings.ContainsAny(line, "\"'\\$`;|&<>"):
		return nil, nil, fmt.Errorf("its runner %q isn't a plain command line", line)
	default:
		if _, ok := RunnerByID(argv[0]); !ok {
			return nil, nil, fmt.Errorf("it runs %q, which isn't a runner Portenv offers", argv[0])
		}
	}
	if fm.has("share_login") {
		fm.remove("share_login")
		changes = append(changes, "share_login removed: sharing your sign-ins isn’t available")
	}
	for _, key := range []string{"panes", "packages"} {
		if fm.grant(key) == "allow" {
			fm.setGrant(key, "ask")
			changes = append(changes, key+" allow → ask")
		}
	}
	return fm.bytes(), changes, nil
}

// frontmatter is an agent file's lines, for the few edits the plan makes.
// The reader that decides what a file means is portenvd's; the plan's
// output is checked by it before anything is written.
type frontmatter struct {
	lines []string
	end   int // the closing --- line
}

func parseFM(b []byte) (*frontmatter, error) {
	lines := strings.Split(string(b), "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return nil, fmt.Errorf("the file must start with a --- line")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return &frontmatter{lines: lines, end: i}, nil
		}
	}
	return nil, fmt.Errorf("the frontmatter has no closing --- line")
}

func (f *frontmatter) bytes() []byte { return []byte(strings.Join(f.lines, "\n")) }

func (f *frontmatter) find(key string) int {
	for i := 1; i < f.end; i++ {
		if strings.HasPrefix(f.lines[i], key+":") {
			return i
		}
	}
	return -1
}

func value(l string) string {
	_, v, _ := strings.Cut(l, ":")
	return strings.TrimSpace(v)
}

func (f *frontmatter) has(key string) bool { return f.find(key) >= 0 }

func (f *frontmatter) get(key string) string {
	if i := f.find(key); i >= 0 {
		return value(f.lines[i])
	}
	return ""
}

func (f *frontmatter) set(key, v string) {
	if i := f.find(key); i >= 0 {
		f.lines[i] = key + ": " + v
	}
}

func (f *frontmatter) remove(key string) {
	if i := f.find(key); i >= 0 {
		f.lines = slices.Delete(f.lines, i, i+1)
		f.end--
	}
}

// grantRange is the grant block's indented lines: [from, to).
func (f *frontmatter) grantRange() (int, int) {
	g := f.find("grant")
	if g < 0 {
		return -1, -1
	}
	j := g + 1
	for j < f.end && (strings.HasPrefix(f.lines[j], " ") || strings.TrimSpace(f.lines[j]) == "") {
		j++
	}
	return g + 1, j
}

func (f *frontmatter) findGrant(key string) int {
	from, to := f.grantRange()
	for i := from; i >= 0 && i < to; i++ {
		if strings.HasPrefix(strings.TrimLeft(f.lines[i], " "), key+":") {
			return i
		}
	}
	return -1
}

func (f *frontmatter) grant(key string) string {
	if i := f.findGrant(key); i >= 0 {
		return value(f.lines[i])
	}
	return ""
}

func (f *frontmatter) grantListOK(key string) ([]string, bool) {
	i := f.findGrant(key)
	if i < 0 {
		return nil, false
	}
	v := value(f.lines[i])
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, false
	}
	var out []string
	for _, s := range strings.Split(v[1:len(v)-1], ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

func (f *frontmatter) grantList(key string) []string {
	l, _ := f.grantListOK(key)
	return l
}

func (f *frontmatter) setGrant(key, v string) {
	if i := f.findGrant(key); i >= 0 {
		indent := len(f.lines[i]) - len(strings.TrimLeft(f.lines[i], " "))
		f.lines[i] = strings.Repeat(" ", indent) + key + ": " + v
		return
	}
	from, to := f.grantRange()
	if from < 0 {
		f.lines = slices.Insert(f.lines, f.end, "grant:", "  "+key+": "+v)
		f.end += 2
		return
	}
	// After the last setting (comments after it stay last).
	at := from
	for i := from; i < to; i++ {
		if t := strings.TrimLeft(f.lines[i], " "); t != "" && !strings.HasPrefix(t, "#") {
			at = i + 1
		}
	}
	f.lines = slices.Insert(f.lines, at, "  "+key+": "+v)
	f.end++
}

func (f *frontmatter) removeGrant(key string) {
	if i := f.findGrant(key); i >= 0 {
		f.lines = slices.Delete(f.lines, i, i+1)
		f.end--
	}
}
