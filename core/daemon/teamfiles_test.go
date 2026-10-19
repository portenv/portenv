// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/portenv/portenv/core/internal/teamkit"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// builderFile is ADR 0022 §3's example, as a teammate would commit it.
const builderFile = `---
id: builder                 # [a-z][a-z0-9-]{0,31}; the file's name must match
name: Builder               # 1–40 characters, shown in the sidebar
role: Writes the code the lead asks for   # up to 120 characters
runner: claude --permission-mode acceptEdits
avatar: hammer
share_login: []
home_seed: [docs/style-guide.md]
grant:
  panes: ask
  max_panes: 4
  message: [lead, reviewer]
  ask_owner: true
  packages: ask
---
You build what the lead asks for.
`

func simpleAgent(id string) string {
	return "---\nid: " + id + "\nname: " + strings.ToUpper(id[:1]) + id[1:] + "\nrunner: claude\n---\nWork.\n"
}

// teamDirs is a project with lead and reviewer already in it, an empty
// overrides folder and a state folder, all temporary.
func teamDirs(t *testing.T) TeamSource {
	t.Helper()
	root := t.TempDir()
	src := TeamSource{
		ProjectDir:   filepath.Join(root, "project"),
		OverridesDir: filepath.Join(root, "agents.local"),
		WorkspaceID:  "box-0123456789ab",
		StateDir:     filepath.Join(root, "state", "box-0123456789ab"),
		Limits:       DefaultTeamLimits(),
	}
	writeAgent(t, src, "lead", simpleAgent("lead"))
	writeAgent(t, src, "reviewer", simpleAgent("reviewer"))
	return src
}

func writeAgent(t *testing.T, src TeamSource, id, content string) {
	t.Helper()
	dir := filepath.Join(src.ProjectDir, ".portenv", "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeOverride(t *testing.T, src TeamSource, id, content string) {
	t.Helper()
	dir := filepath.Join(src.OverridesDir, src.WorkspaceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadAgent(t *testing.T, src TeamSource, id string) TeamAgent {
	t.Helper()
	team, err := LoadTeam(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range team.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent %s in %+v", id, team)
	return TeamAgent{}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestAgentFileExampleIsValid(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	a := loadAgent(t, src, "builder")
	if len(a.Problems) != 0 {
		t.Fatalf("problems: %q", a.Problems)
	}
	s := a.Spec
	if s.ID != "builder" || s.Name != "Builder" || s.Role != "Writes the code the lead asks for" || s.Avatar != "hammer" {
		t.Errorf("spec: %+v", s)
	}
	if !reflect.DeepEqual(a.Runner, []string{"claude", "--permission-mode", "acceptEdits"}) || a.RunnerLine != "claude --permission-mode acceptEdits" {
		t.Errorf("runner: %q %q", a.Runner, a.RunnerLine)
	}
	if !reflect.DeepEqual(s.HomeSeed, []string{"docs/style-guide.md"}) || len(a.ShareLogin) != 0 {
		t.Errorf("lists: %q %q", s.HomeSeed, a.ShareLogin)
	}
	if s.Instructions != "You build what the lead asks for.\n" {
		t.Errorf("instructions: %q", s.Instructions)
	}
	g := a.Grant
	if g.GetPanes() != typesv1.GrantMode_GRANT_MODE_ASK || g.GetMaxPanes() != 4 || !g.GetAskOwner() ||
		g.GetPackages() != typesv1.GrantMode_GRANT_MODE_ASK || !reflect.DeepEqual(g.GetMessage(), []string{"lead", "reviewer"}) {
		t.Errorf("grant: %v", g)
	}
	if a.SHA256 != sha(builderFile) || a.Approved {
		t.Errorf("sha %s approved %v", a.SHA256, a.Approved)
	}
}

func TestAgentFileDefaults(t *testing.T) {
	src := teamDirs(t)
	a := loadAgent(t, src, "lead")
	if len(a.Problems) != 0 {
		t.Fatalf("problems: %q", a.Problems)
	}
	g := a.Grant
	if g.GetPanes() != typesv1.GrantMode_GRANT_MODE_ASK || g.GetMaxPanes() != 4 || !g.GetAskOwner() ||
		g.GetPackages() != typesv1.GrantMode_GRANT_MODE_ASK || len(g.GetMessage()) != 0 {
		t.Errorf("defaults: %v", g)
	}
	// An owner limit under 4 lowers the default, rather than making a
	// file that never mentioned max_panes invalid.
	src.Limits.MaxPanes = 2
	if g := loadAgent(t, src, "lead").Grant; g.GetMaxPanes() != 2 {
		t.Errorf("max_panes under a limit of 2: %d", g.GetMaxPanes())
	}
}

// ADR 0022 §2a: checkout is own by default, or none.
func TestAgentFileCheckout(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", with())
	if a := loadAgent(t, src, "builder"); len(a.Problems) != 0 || a.Spec.Checkout != CheckoutOwn || !a.OwnClone() {
		t.Errorf("default: %+v", a)
	}
	writeAgent(t, src, "builder", with("checkout: none"))
	if a := loadAgent(t, src, "builder"); len(a.Problems) != 0 || a.Spec.Checkout != CheckoutNone || a.OwnClone() {
		t.Errorf("none: %+v", a)
	}
}

func TestAgentFileRemoteRunner(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "dots", "---\nid: dots\nname: Dots\nrunner: remote\nvia: mcp\n---\n")
	a := loadAgent(t, src, "dots")
	if len(a.Problems) != 0 || !a.Remote || a.Via != "mcp" || a.Runner != nil {
		t.Errorf("remote: %+v", a)
	}
}

// fm makes an agent file for builder from frontmatter lines.
func fm(lines ...string) string {
	return "---\n" + strings.Join(lines, "\n") + "\n---\nWork.\n"
}

// base is a valid builder's frontmatter; a case changes one line of it.
var base = []string{"id: builder", "name: Builder", "runner: claude"}

// ofm makes a personal override: frontmatter only.
func ofm(lines ...string) string {
	return "---\n" + strings.Join(lines, "\n") + "\n---\n"
}

func with(extra ...string) string { return fm(append(slices.Clone(base), extra...)...) }

func TestAgentFileRules(t *testing.T) {
	long := strings.Repeat("a", 41)
	cases := []struct {
		name, file string
		content    string
		want       string
	}{
		{"unknown key", "builder", with("colour: red"), `builder: "colour" isn't a key an agent file may have.`},
		{"unknown grant key", "builder", with("grant:", "  push: allow"), `builder: "grant.push" isn't a key an agent file may have.`},
		{"key twice", "builder", with("name: Again"), `builder: name is set twice.`},
		{"no opening line", "builder", "id: builder\n", `builder: the file must start with a --- line.`},
		{"no closing line", "builder", "---\nid: builder\n", `builder: the frontmatter has no closing --- line.`},
		{"not key value", "builder", with("just words"), `builder: line 5 isn't in the form "key: value".`},
		{"tab", "builder", with("grant:", "\tpanes: ask"), `builder: line 6 has a tab; use spaces.`},
		{"block list", "builder", with("home_seed:", "  - docs"), `builder: line 6 is indented, but only grant takes indented settings.`},
		{"yaml it doesn't take", "builder", with("avatar: &a hammer"), `builder: line 5 uses YAML an agent file doesn't take; use "key: value", or a list in [ ].`},
		{"flow map", "builder", with("grant: {panes: ask}"), `builder: line 5 uses YAML an agent file doesn't take; use "key: value", or a list in [ ].`},
		{"colon in plain value", "builder", fm("id: builder", "name: Builder", "role: Lead: plans", "runner: claude"), `builder: line 4 has ": " in a value; put the value in quotes.`},
		{"unclosed quote", "builder", with(`role: "Writes`), `builder: line 5 has an unclosed quote.`},
		{"unclosed list", "builder", with("share_login: [claude"), `builder: line 5 has a list with no closing ].`},
		{"empty list item", "builder", with("share_login: [claude,,gh]"), `builder: line 5 has an empty item in a list.`},
		{"no value", "builder", with("role:"), `builder: role has no value.`},
		{"list for scalar", "builder", with("avatar: [hammer]"), `builder: avatar must be a single value, not a list.`},
		{"scalar for list", "builder", with("share_login: claude"), `builder: share_login must be a list, like [claude].`},
		{"scalar for grant", "builder", with("grant: ask"), `builder: grant must be settings on the indented lines below it.`},
		{"id missing", "builder", fm("name: Builder", "runner: claude"), `builder: id is missing.`},
		{"name missing", "builder", fm("id: builder", "runner: claude"), `builder: name is missing.`},
		{"runner missing", "builder", fm("id: builder", "name: Builder"), `builder: runner is missing.`},
		{"id pattern", "Builder", fm("id: Builder", "name: Builder", "runner: claude"), `Builder: id "Builder" must be a lowercase letter, then up to 31 lowercase letters, digits or hyphens.`},
		{"id too long", strings.Repeat("b", 33), fm("id: "+strings.Repeat("b", 33), "name: B", "runner: claude"), strings.Repeat("b", 33) + `: id "` + strings.Repeat("b", 33) + `" must be a lowercase letter, then up to 31 lowercase letters, digits or hyphens.`},
		{"id not file name", "builder", fm("id: build", "name: Builder", "runner: claude"), `builder: id is "build"; it must match the file's name, builder.`},
		{"reserved team", "team", fm("id: team", "name: Team", "runner: claude"), `team: "team" is a reserved name; choose another id.`},
		{"reserved work", "work", fm("id: work", "name: Work", "runner: claude"), `work: "work" is a reserved name; choose another id.`},
		{"reserved portenv", "portenv-x", fm("id: portenv-x", "name: X", "runner: claude"), `portenv-x: "portenv-x" is a reserved name; choose another id.`},
		{"reserved image user", "root", fm("id: root", "name: Root", "runner: claude"), `root: "root" is a reserved name; choose another id.`},
		{"name too long", "builder", fm("id: builder", "name: "+long, "runner: claude"), `builder: name is 41 characters; it must be 1 to 40.`},
		{"name empty", "builder", fm("id: builder", `name: ""`, "runner: claude"), `builder: name is 0 characters; it must be 1 to 40.`},
		{"role too long", "builder", with("role: " + strings.Repeat("r", 121)), `builder: role is 121 characters; it must be at most 120.`},
		{"bidi in name", "builder", fm("id: builder", "name: Build\u202eer", "runner: claude"), `builder: name has a control or bidi character.`},
		{"bidi in role", "builder", with("role: a\u2066b"), `builder: role has a control or bidi character.`},
		{"control in name", "builder", fm("id: builder", "name: \"a\u0007b\"", "runner: claude"), `builder: name has a control or bidi character.`},
		{"bidi in runner", "builder", fm("id: builder", "name: Builder", "runner: claude \u202e--x"), `builder: runner has a control or bidi character.`},
		{"runner unbalanced", "builder", fm("id: builder", "name: Builder", `runner: claude --x 'a b`), `builder: runner has an unbalanced quote.`},
		{"runner lone backslash", "builder", fm("id: builder", "name: Builder", `runner: claude \`), `builder: runner ends with a lone backslash.`},
		{"runner empty", "builder", fm("id: builder", "name: Builder", `runner: ""`), `builder: runner is empty.`},
		{"remote without via", "builder", fm("id: builder", "name: Builder", "runner: remote"), `builder: runner is remote, so via (ssh, mcp or web) is required.`},
		{"via without remote", "builder", with("via: ssh"), `builder: via is set, but only runner: remote takes via.`},
		{"via unknown", "builder", fm("id: builder", "name: Builder", "runner: remote", "via: ftp"), `builder: via is "ftp"; it must be ssh, mcp or web.`},
		{"avatar pattern", "builder", with("avatar: Hammer!"), `builder: avatar "Hammer!" must be an SF Symbol or colour name: lowercase letters, digits and dots, at most 40.`},
		{"avatar too long", "builder", with("avatar: " + strings.Repeat("a", 41)), `builder: avatar "` + strings.Repeat("a", 41) + `" must be an SF Symbol or colour name: lowercase letters, digits and dots, at most 40.`},
		{"share_login item", "builder", with("share_login: [Claude]"), `builder: share_login "Claude" must be a tool name: a lowercase letter, then lowercase letters, digits or hyphens.`},
		{"panes", "builder", with("grant:", "  panes: maybe"), `builder: grant.panes is "maybe"; it must be ask, allow or never.`},
		{"packages allow", "builder", with("grant:", "  packages: allow"), `builder: grant.packages is "allow"; it must be ask or never.`},
		{"checkout", "builder", with("checkout: shared"), `builder: checkout is "shared"; it must be own or none.`},
		{"checkout list", "builder", with("checkout: [own]"), `builder: checkout must be a single value, not a list.`},
		{"max_panes high", "builder", with("grant:", "  max_panes: 12"), `builder: grant.max_panes is 12; it must be 1 to 8.`},
		{"max_panes zero", "builder", with("grant:", "  max_panes: 0"), `builder: grant.max_panes is 0; it must be 1 to 8.`},
		{"max_panes words", "builder", with("grant:", "  max_panes: four"), `builder: grant.max_panes is "four"; it must be a whole number from 1 to 8.`},
		{"max_panes over owner limit", "builder", with("grant:", "  max_panes: 6"), `builder: grant.max_panes is 6; the owner's limit is 4.`},
		{"message stranger", "builder", with("grant:", "  message: [lead, ghost]"), `builder: grant.message names "ghost", which isn't another agent in this team.`},
		{"message self", "builder", with("grant:", "  message: [builder]"), `builder: grant.message names "builder", which isn't another agent in this team.`},
		{"message owner", "builder", with("grant:", "  message: [owner]"), `builder: grant.message names "owner", which isn't another agent in this team.`},
		{"ask_owner", "builder", with("grant:", "  ask_owner: yes"), `builder: grant.ask_owner is "yes"; it must be true or false.`},
		{"ask_owner quoted", "builder", with("grant:", `  ask_owner: "true"`), `builder: grant.ask_owner is "true"; it must be true or false.`},
		{"grant indent", "builder", with("grant:", "  panes: ask", "    max_panes: 2"), `builder: line 7's indentation doesn't match grant's other settings.`},
		{"home_seed absolute", "builder", with("home_seed: [/etc/passwd]"), `builder: home_seed "/etc/passwd" must be a path relative to the project.`},
		{"home_seed dotdot", "builder", with("home_seed: [docs/../../x]"), `builder: home_seed "docs/../../x" must stay inside the project (no ..).`},
		{"home_seed dotdot inside", "builder", with("home_seed: [a/../b]"), `builder: home_seed "a/../b" must stay inside the project (no ..).`},
		{"home_seed empty", "builder", with(`home_seed: [""]`), `builder: home_seed "" must be a path relative to the project.`},
		{"invalid utf-8", "builder", "---\nid: builder\nname: B\xff\nrunner: claude\n---\n", `builder: the file isn't valid UTF-8.`},
		{"file too big", "builder", with() + strings.Repeat("x", 64<<10), `builder: the file is ` + strconv.Itoa(len(with())+64<<10) + ` bytes; it must be at most 64 KB.`},
		{"instructions too big", "builder", with() + strings.Repeat("x", 32<<10), `builder: the instructions are ` + strconv.Itoa(len("Work.\n")+32<<10) + ` bytes; they must be at most 32 KB.`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := teamDirs(t)
			writeAgent(t, src, c.file, c.content)
			a := loadAgent(t, src, c.file)
			if !slices.Contains(a.Problems, c.want) {
				t.Errorf("want %q\n got %q", c.want, a.Problems)
			}
			if a.Valid() {
				t.Error("an agent with a problem is valid")
			}
		})
	}
}

// TestAgentFileQuotingAndComments: the YAML an agent file does take reads
// as YAML would.
func TestAgentFileQuotingAndComments(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", fm(
		"# a comment line",
		"id: 'builder'",
		`name: "Bob \"B\" Builder"`,
		"role: 'It''s the builder'  # trailing comment",
		`runner: claude --append-system-prompt "be brief" 'it''s' a\ b`,
		"",
		`share_login: [ claude , "gh" ]`,
		"grant:",
		"  # nothing here",
		"  message: []",
		"  ask_owner: false",
	))
	a := loadAgent(t, src, "builder")
	if len(a.Problems) != 0 {
		t.Fatalf("problems: %q", a.Problems)
	}
	if a.Spec.Name != `Bob "B" Builder` || a.Spec.Role != "It's the builder" {
		t.Errorf("scalars: %q %q", a.Spec.Name, a.Spec.Role)
	}
	// The runner is split like a shell, but YAML's '' isn't a shell escape:
	// the value YAML gives is the line the shell rule splits.
	if want := []string{"claude", "--append-system-prompt", "be brief", "its", "a b"}; !reflect.DeepEqual(a.Runner, want) {
		t.Errorf("runner %q, want %q", a.Runner, want)
	}
	if !reflect.DeepEqual(a.ShareLogin, []string{"claude", "gh"}) || a.Grant.GetAskOwner() {
		t.Errorf("lists or ask_owner: %q %v", a.ShareLogin, a.Grant)
	}
}

func TestAgentFileCollectsEveryProblem(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", fm("id: builder", "name: Builder", "runner: claude 'x", "colour: red", "grant:", "  max_panes: 12", "  packages: allow"))
	want := []string{
		`builder: "colour" isn't a key an agent file may have.`,
		`builder: runner has an unbalanced quote.`,
		`builder: grant.max_panes is 12; it must be 1 to 8.`,
		`builder: grant.packages is "allow"; it must be ask or never.`,
	}
	got := loadAgent(t, src, "builder").Problems
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d problems, want %d: %q", len(got), len(want), got)
	}
}

func TestSplitArgv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  string
	}{
		{"claude", []string{"claude"}, ""},
		{"  a   b\tc ", []string{"a", "b", "c"}, ""},
		{`a "b c" 'd e'`, []string{"a", "b c", "d e"}, ""},
		{`a "b\"c\\d\$e"`, []string{"a", `b"c\d$e`}, ""},
		{`a "b\nc"`, []string{"a", `b\nc`}, ""},
		{`a 'b\c'`, []string{"a", `b\c`}, ""},
		{`a\ b c`, []string{"a b", "c"}, ""},
		{`a""b ''`, []string{"ab", ""}, ""},
		{`echo $HOME; rm -rf / | x`, []string{"echo", "$HOME;", "rm", "-rf", "/", "|", "x"}, ""},
		{`a "b`, nil, "unbalanced quote"},
		{`a 'b`, nil, "unbalanced quote"},
		{`a \`, nil, "lone backslash"},
		{"", nil, "empty"},
		{"   ", nil, "empty"},
	}
	for _, c := range cases {
		got, err := splitArgv(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %q %v, want %q", c.in, got, err, c.want)
		}
	}
}

func TestTeamAgentLimit(t *testing.T) {
	src := teamDirs(t)
	for _, id := range []string{"a1", "a2", "a3"} {
		writeAgent(t, src, id, simpleAgent(id))
	}
	team, err := LoadTeam(src)
	if err != nil {
		t.Fatal(err)
	}
	if want := "The team has 5 agents; the owner's limit is 4."; !slices.Contains(team.Problems, want) {
		t.Errorf("want %q in %q", want, team.Problems)
	}
}

func TestNoTeam(t *testing.T) {
	src := teamDirs(t)
	src.ProjectDir = filepath.Join(t.TempDir(), "elsewhere")
	team, err := LoadTeam(src)
	if err != nil || len(team.Agents) != 0 || len(team.Problems) != 0 {
		t.Errorf("no agents folder: %+v %v", team, err)
	}
	src.ProjectDir = ""
	if team, err := LoadTeam(src); err != nil || len(team.Agents) != 0 {
		t.Errorf("no project: %+v %v", team, err)
	}
}

func TestAgentFileLinkRefused(t *testing.T) {
	src := teamDirs(t)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte(builderFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src.ProjectDir, ".portenv", "agents", "builder.md")); err != nil {
		t.Fatal(err)
	}
	a := loadAgent(t, src, "builder")
	if want := "builder: builder.md isn't a plain file."; !slices.Contains(a.Problems, want) || a.Spec != nil || a.SHA256 != "" {
		t.Errorf("a linked file: %+v", a)
	}
}

func TestWorkspaceIDIsOneFolder(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "../x"} {
		src := teamDirs(t)
		src.WorkspaceID = id
		if _, err := LoadTeam(src); err == nil {
			t.Errorf("workspace id %q accepted", id)
		}
	}
}

func TestRunnerCheckHook(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	writeAgent(t, src, "dots", "---\nid: dots\nname: Dots\nrunner: remote\nvia: mcp\n---\n")
	var asked []string
	src.CheckRunner = func(id, program string) error {
		asked = append(asked, id+"="+program)
		if id == "builder" {
			return errors.New("runner `claude` isn't installed in this workspace. Install it, then choose Start.")
		}
		return nil
	}
	team, err := LoadTeam(src)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(asked)
	if want := []string{"builder=claude", "lead=claude", "reviewer=claude"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("asked %q, want %q (never for a remote runner)", asked, want)
	}
	for _, a := range team.Agents {
		if a.ID == "builder" && !slices.Contains(a.Problems, "builder: runner `claude` isn't installed in this workspace. Install it, then choose Start.") {
			t.Errorf("builder: %q", a.Problems)
		}
	}
}

// --- Personal overrides ---

func TestOverrideNarrowsAndChanges(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	writeOverride(t, src, "builder", ofm(
		"runner: claude --model opus",
		"avatar: blue",
		"share_login: [claude]",
		"grant:",
		"  panes: never",
		"  max_panes: 2",
		"  message: [lead]",
		"  ask_owner: false",
		"  packages: never",
	))
	a := loadAgent(t, src, "builder")
	if len(a.Problems) != 0 {
		t.Fatalf("problems: %q", a.Problems)
	}
	if !a.Override || a.Avatar != "blue" || !reflect.DeepEqual(a.Runner, []string{"claude", "--model", "opus"}) || !reflect.DeepEqual(a.ShareLogin, []string{"claude"}) {
		t.Errorf("override: %+v", a)
	}
	g := a.Grant
	if g.GetPanes() != typesv1.GrantMode_GRANT_MODE_NEVER || g.GetMaxPanes() != 2 || g.GetAskOwner() ||
		g.GetPackages() != typesv1.GrantMode_GRANT_MODE_NEVER || !reflect.DeepEqual(g.GetMessage(), []string{"lead"}) {
		t.Errorf("effective grant: %v", g)
	}
	// The repository file's own grant is as written; the pin is its bytes.
	if a.Spec.Grant.GetMaxPanes() != 4 || a.SHA256 != sha(builderFile) {
		t.Errorf("repository spec changed by the override: %v %s", a.Spec.Grant, a.SHA256)
	}
}

func TestOverrideCantWiden(t *testing.T) {
	narrow := fm("id: builder", "name: Builder", "runner: claude", "home_seed: [docs]", "grant:",
		"  panes: never", "  max_panes: 2", "  message: [lead]", "  ask_owner: false", "  packages: never")
	cases := []struct{ name, override, want string }{
		{"panes", ofm("grant:", "  panes: ask"), `builder's override: grant.panes is ask, wider than the repository file's never; an override may only narrow a grant.`},
		{"panes allow", ofm("grant:", "  panes: allow"), `builder's override: grant.panes is allow, wider than the repository file's never; an override may only narrow a grant.`},
		{"max_panes", ofm("grant:", "  max_panes: 3"), `builder's override: grant.max_panes is 3, wider than the repository file's 2; an override may only narrow a grant.`},
		{"message", ofm("grant:", "  message: [lead, reviewer]"), `builder's override: grant.message is [lead, reviewer], wider than the repository file's [lead]; an override may only narrow a grant.`},
		{"ask_owner", ofm("grant:", "  ask_owner: true"), `builder's override: grant.ask_owner is true, wider than the repository file's false; an override may only narrow a grant.`},
		{"packages", ofm("grant:", "  packages: ask"), `builder's override: grant.packages is ask, wider than the repository file's never; an override may only narrow a grant.`},
		{"team_status", ofm("grant:", "  team_status: read"), `builder's override: grant.team_status is read, wider than the repository file's none; an override may only narrow a grant.`},
		{"home_seed", ofm("home_seed: [secrets]"), `builder's override: it sets home_seed; an override can't add home_seed.`},
		{"name", ofm("name: Boss"), `builder's override: it sets name; an override may change only runner, via, avatar, share_login and grant.`},
		{"checkout", ofm("checkout: none"), `builder's override: it sets checkout; an override may change only runner, via, avatar, share_login and grant.`},
		{"unknown key", ofm("colour: red"), `builder's override: "colour" isn't a key an agent file may have.`},
		{"other id", ofm("id: lead"), `builder's override: id is "lead"; it must match the file's name, builder.`},
		{"instructions", "---\navatar: blue\n---\nDo more.\n", `builder's override: it has instructions; only the repository file's are used.`},
		{"bad value", ofm("grant:", "  max_panes: 12"), `builder's override: grant.max_panes is 12; it must be 1 to 8.`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := teamDirs(t)
			writeAgent(t, src, "builder", narrow)
			writeOverride(t, src, "builder", c.override)
			a := loadAgent(t, src, "builder")
			if len(a.Problems) != 1 || a.Problems[0] != c.want {
				t.Errorf("want only %q\n got %q", c.want, a.Problems)
			}
			if a.Override {
				t.Error("a refused override is in effect")
			}
			// A refused override never widens what's in effect.
			g := a.Grant
			if g.GetPanes() != typesv1.GrantMode_GRANT_MODE_NEVER || g.GetMaxPanes() != 2 || g.GetAskOwner() ||
				g.GetPackages() != typesv1.GrantMode_GRANT_MODE_NEVER || g.GetTeamStatus() || !reflect.DeepEqual(g.GetMessage(), []string{"lead"}) {
				t.Errorf("effective grant widened: %v", g)
			}
			if !reflect.DeepEqual(a.Spec.HomeSeed, []string{"docs"}) {
				t.Errorf("home_seed: %q", a.Spec.HomeSeed)
			}
		})
	}
}

func TestOverrideWithoutAgentFile(t *testing.T) {
	src := teamDirs(t)
	writeOverride(t, src, "ghost", ofm("avatar: blue"))
	team, err := LoadTeam(src)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ghost's override: there's no agent file ghost.md in the project's .portenv/agents."; !slices.Contains(team.Problems, want) {
		t.Errorf("want %q in %q", want, team.Problems)
	}
}

// --- The content pin and approvals ---

func TestApprovePinsContent(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	a := loadAgent(t, src, "builder")
	if a.Approved || a.ApprovedContent != "" {
		t.Fatalf("approved before Approve: %+v", a)
	}
	if err := ApproveAgentFile(src, "builder", a.SHA256); err != nil {
		t.Fatal(err)
	}
	a = loadAgent(t, src, "builder")
	if !a.Approved || a.ApprovedContent != builderFile {
		t.Fatalf("after Approve: approved %v content %q", a.Approved, a.ApprovedContent)
	}
	// An edit (a git pull, a teammate's commit) changes the pin and ends
	// the approval; the approved content stays for the app's diff.
	edited := strings.Replace(builderFile, "panes: ask", "panes: allow", 1)
	writeAgent(t, src, "builder", edited)
	b := loadAgent(t, src, "builder")
	if b.SHA256 == a.SHA256 || b.SHA256 != sha(edited) {
		t.Errorf("pin didn't change on edit: %s", b.SHA256)
	}
	if b.Approved || b.ApprovedContent != builderFile {
		t.Errorf("edited file: approved %v content %q", b.Approved, b.ApprovedContent)
	}
	// Approving the new content takes it.
	if err := ApproveAgentFile(src, "builder", b.SHA256); err != nil {
		t.Fatal(err)
	}
	if c := loadAgent(t, src, "builder"); !c.Approved || c.ApprovedContent != edited {
		t.Errorf("re-approved: %v %q", c.Approved, c.ApprovedContent)
	}
	// Other agents' approvals are kept.
	if err := ApproveAgentFile(src, "lead", sha(simpleAgent("lead"))); err != nil {
		t.Fatal(err)
	}
	if !loadAgent(t, src, "builder").Approved || !loadAgent(t, src, "lead").Approved {
		t.Error("one approval lost another")
	}
}

func TestApproveStaleSHARefused(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	shown := sha(builderFile)
	writeAgent(t, src, "builder", builderFile+"More.\n")
	err := ApproveAgentFile(src, "builder", shown)
	if err == nil || !strings.Contains(err.Error(), "builder's file changed since it was shown; nothing was approved.") {
		t.Fatalf("stale sha: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src.StateDir, agentApprovalsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused approval wrote the store: %v", err)
	}
	if loadAgent(t, src, "builder").Approved {
		t.Error("approved with a stale sha")
	}
	for _, s := range []string{"", "zz", strings.ToUpper(sha(builderFile + "More.\n"))} {
		if err := ApproveAgentFile(src, "builder", s); err == nil {
			t.Errorf("sha %q approved", s)
		}
	}
	if err := ApproveAgentFile(src, "ghost", shown); err == nil {
		t.Error("approved an agent with no file")
	}
}

func TestApprovalsStoreAtomicAndPrivate(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	if err := ApproveAgentFile(src, "builder", sha(builderFile)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(src.StateDir, agentApprovalsFile)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("store mode %v, want 0600", fi.Mode().Perm())
	}
	ents, err := os.ReadDir(src.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Errorf("left behind: %v", ents)
	}
	var store struct {
		Version int `json:"version"`
		Agents  map[string]struct {
			SHA256  string `json:"sha256"`
			Content string `json:"content"`
		} `json:"agents"`
	}
	b, err := os.ReadFile(p) // #nosec G304 -- the test's own folder
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &store); err != nil {
		t.Fatal(err)
	}
	if store.Version != 1 || store.Agents["builder"].SHA256 != sha(builderFile) || store.Agents["builder"].Content != builderFile {
		t.Errorf("store: %s", b)
	}
}

func TestApprovalsStoreUnreadableNeverOverwritten(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "builder", builderFile)
	if err := os.MkdirAll(src.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(src.StateDir, agentApprovalsFile)
	garbage := []byte(`{"version":1,"agents":{"builder":`)
	if err := os.WriteFile(p, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTeam(src); err == nil {
		t.Error("LoadTeam read an unreadable store as nothing approved")
	}
	if err := ApproveAgentFile(src, "builder", sha(builderFile)); err == nil {
		t.Error("Approve wrote over an unreadable store")
	}
	if b, _ := os.ReadFile(p); string(b) != string(garbage) { // #nosec G304 -- the test's own folder
		t.Errorf("store changed: %q", b)
	}
	// A store of a version this portenvd doesn't know is unreadable too.
	newer := []byte(`{"version":2,"agents":{}}`)
	if err := os.WriteFile(p, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApproveAgentFile(src, "builder", sha(builderFile)); err == nil {
		t.Error("Approve wrote over a newer store")
	}
	if b, _ := os.ReadFile(p); string(b) != string(newer) { // #nosec G304 -- the test's own folder
		t.Errorf("store changed: %q", b)
	}
	// A store that can't be opened at all.
	if os.Geteuid() != 0 {
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		if _, err := LoadTeam(src); err == nil {
			t.Error("LoadTeam read a store it can't open as nothing approved")
		}
		if err := ApproveAgentFile(src, "builder", sha(builderFile)); err == nil {
			t.Error("Approve wrote over a store it can't open")
		}
	}
}

func TestApprovalsStoreMissingIsNothingApproved(t *testing.T) {
	src := teamDirs(t)
	team, err := LoadTeam(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range team.Agents {
		if a.Approved {
			t.Errorf("%s approved with no store", a.ID)
		}
	}
}

// R-0257 item 1: grant.team_status is read or none, off by default.
func TestTeamStatusGrant(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "lead", fm("id: lead", "name: Lead", "runner: claude", "grant:", "  team_status: read"))
	writeAgent(t, src, "builder", fm("id: builder", "name: Builder", "runner: claude"))
	writeAgent(t, src, "odd", fm("id: odd", "name: Odd", "runner: claude", "grant:", "  team_status: write"))
	if a := loadAgent(t, src, "lead"); len(a.Problems) != 0 || !a.Grant.GetTeamStatus() {
		t.Errorf("lead: %v, %v", a.Problems, a.Grant)
	}
	if a := loadAgent(t, src, "builder"); a.Grant.GetTeamStatus() {
		t.Error("team_status is on by default")
	}
	if a := loadAgent(t, src, "odd"); len(a.Problems) != 1 || !strings.Contains(a.Problems[0], "read or none") {
		t.Errorf("odd: %v", a.Problems)
	}
}

// R-0258: routines in the agent file, checked; never from an override.
func TestRoutinesInTheAgentFile(t *testing.T) {
	src := teamDirs(t)
	writeAgent(t, src, "lead", fm("id: lead", "name: Lead", "runner: claude",
		`routine: ["every 30m: Status round: check every agent and report blockers", "every 2h: Tidy the board"]`))
	a := loadAgent(t, src, "lead")
	if len(a.Problems) != 0 {
		t.Fatal(a.Problems)
	}
	want := []AgentRoutine{{30, "Status round: check every agent and report blockers"}, {120, "Tidy the board"}}
	if !reflect.DeepEqual(a.Spec.Routines, want) {
		t.Errorf("routines = %v", a.Spec.Routines)
	}
	for name, line := range map[string]string{
		"too often":  `routine: ["every 5m: ping"]`,
		"no every":   `routine: ["30m: ping"]`,
		"too many":   `routine: ["every 10m: a", "every 10m: b", "every 10m: c", "every 10m: d", "every 10m: e"]`,
		"escape":     `routine: ["every 30m: hi \u001b[2J"]`,
		"empty note": `routine: ["every 30m: "]`,
		"a week+":    `routine: ["every 200h: x"]`,
	} {
		src := teamDirs(t)
		writeAgent(t, src, "lead", fm("id: lead", "name: Lead", "runner: claude", line))
		if a := loadAgent(t, src, "lead"); len(a.Problems) == 0 {
			t.Errorf("%s: no problem for %s", name, line)
		}
	}
	src = teamDirs(t)
	writeAgent(t, src, "lead", fm("id: lead", "name: Lead", "runner: claude"))
	writeOverride(t, src, "lead", ofm(`routine: ["every 30m: sneaky"]`))
	if a := loadAgent(t, src, "lead"); len(a.Problems) != 1 || !strings.Contains(a.Problems[0], "only the repository file") {
		t.Errorf("an override's routine: %v", a.Problems)
	}
}

// templateTeam loads a template's agents as portenv team init writes them,
// with the owner's agent limit at its ceiling (ADR 0022 §7) and every
// other limit at its default. extra adds files of the template's off/
// folder as if the owner had turned them on.
func templateTeam(t *testing.T, name string, extra ...string) Team {
	t.Helper()
	files, err := teamkit.Template(name)
	if err != nil || len(files) == 0 {
		t.Fatalf("%v, %d files", err, len(files))
	}
	src := teamDirs(t)
	src.Limits = TeamLimits{MaxAgents: teamkit.MaxTeamAgents}
	for _, id := range []string{"lead", "reviewer"} { // teamDirs's own
		if err := os.Remove(filepath.Join(src.agentsDir(), id+".md")); err != nil {
			t.Fatal(err)
		}
	}
	for f, content := range files {
		id, ok := strings.CutSuffix(f, ".md")
		if !ok {
			t.Fatalf("%s/%s isn't an agent file", name, f)
		}
		if off, isOff := strings.CutPrefix(id, teamkit.OffDir+"/"); isOff {
			if !slices.Contains(extra, off) {
				continue
			}
			id = off
		}
		writeAgent(t, src, id, string(content))
	}
	team, err := LoadTeam(src)
	if err != nil {
		t.Fatal(err)
	}
	return team
}

// teamStatusReaders is who in each template has grant.team_status: read.
var teamStatusReaders = map[string][]string{
	"starter":     nil,
	"orchestrate": {"lead"},
	"app-product": {"lead"},
}

// Every template portenv team init writes is a valid team with no
// problems (R-0232's Orchestrate team included), within the limits
// portenvd enforces, and each agent it ships off is valid once turned on.
func TestTheTemplatesAreValidTeams(t *testing.T) {
	for _, name := range teamkit.Templates {
		t.Run(name, func(t *testing.T) {
			on := teamkit.OnAgents(name)
			team := templateTeam(t, name)
			if len(team.Agents) != len(on) || len(team.Problems) != 0 {
				t.Fatalf("agents %d, want %d; team problems %v", len(team.Agents), len(on), team.Problems)
			}
			if len(on) > teamkit.MaxTeamAgents {
				t.Errorf("%d agents on; the ceiling is %d", len(on), teamkit.MaxTeamAgents)
			}
			var readers []string
			for _, a := range team.Agents {
				checkTemplateAgent(t, a, on)
				if a.Grant.GetTeamStatus() {
					readers = append(readers, a.ID)
				}
			}
			if !slices.Equal(readers, teamStatusReaders[name]) {
				t.Errorf("team_status: read for %v, want %v", readers, teamStatusReaders[name])
			}
			for _, off := range teamkit.OffAgents(name) {
				var found bool
				for _, a := range templateTeam(t, name, off).Agents {
					if a.ID != off {
						continue
					}
					found = true
					checkTemplateAgent(t, a, append(slices.Clone(on), off))
					if a.Grant.GetTeamStatus() {
						t.Errorf("%s (off) reads the team's status", off)
					}
				}
				if !found {
					t.Errorf("%s (off) wasn't loaded", off)
				}
			}
		})
	}
}

// checkTemplateAgent: the agent parses with no problems, its grant is
// within what the code allows by default, and it messages only agents in
// members.
func checkTemplateAgent(t *testing.T, a TeamAgent, members []string) {
	t.Helper()
	if len(a.Problems) != 0 || a.Spec == nil {
		t.Errorf("%s: %v", a.ID, a.Problems)
		return
	}
	if runnerIdlePattern(a.Runner) == "" && len(a.Spec.Routines) > 0 {
		t.Errorf("%s has routines but a runner without an idle pattern", a.ID)
	}
	g := a.Grant
	if g.GetMaxPanes() < 1 || g.GetMaxPanes() > teamkit.DefaultAgentPanes {
		t.Errorf("%s: max_panes %d; the default limit is %d", a.ID, g.GetMaxPanes(), teamkit.DefaultAgentPanes)
	}
	if g.GetPanes() == typesv1.GrantMode_GRANT_MODE_ALLOW || g.GetPackages() == typesv1.GrantMode_GRANT_MODE_ALLOW {
		t.Errorf("%s: a template never allows panes or packages without asking", a.ID)
	}
	if len(a.Spec.Routines) > teamkit.MaxRoutines {
		t.Errorf("%s: %d routines", a.ID, len(a.Spec.Routines))
	}
	for _, m := range g.GetMessage() {
		if m == a.ID || !slices.Contains(members, m) {
			t.Errorf("%s messages %q, which isn't another agent in the template", a.ID, m)
		}
	}
}

// R-0338: the App product team, as the owner asked for it.
func TestTheAppProductTemplate(t *testing.T) {
	team := templateTeam(t, "app-product", "social")
	agents := map[string]TeamAgent{}
	for _, a := range team.Agents {
		agents[a.ID] = a
	}
	want := map[string]struct {
		name     string
		message  []string
		checkout string
	}{
		"lead":          {"Engineering Lead", []string{"writer", "reviewer", "designer", "design-system", "trust-safety", "release", "devops"}, CheckoutNone},
		"writer":        {"Code Writer", []string{"lead", "reviewer"}, CheckoutOwn},
		"reviewer":      {"Code Reviewer", []string{"lead", "writer"}, CheckoutOwn},
		"designer":      {"Product Designer", []string{"lead", "design-system", "trust-safety"}, CheckoutNone},
		"design-system": {"Design System Keeper", []string{"lead", "designer"}, CheckoutOwn},
		"trust-safety":  {"Trust & Safety and Support", []string{"lead", "designer"}, CheckoutNone},
		"release":       {"App Store Release Manager", []string{"lead", "devops"}, CheckoutOwn},
		"devops":        {"DevOps", []string{"lead", "release"}, CheckoutOwn},
		"social":        {"Social Media Manager", []string{"lead"}, CheckoutNone},
	}
	if len(agents) != len(want) {
		t.Fatalf("%d agents, want %d", len(agents), len(want))
	}
	for id, w := range want {
		a, ok := agents[id]
		if !ok || a.Spec == nil {
			t.Errorf("no %s", id)
			continue
		}
		if a.Spec.Name != w.name || !slices.Equal(a.Grant.GetMessage(), w.message) || a.Spec.Checkout != w.checkout {
			t.Errorf("%s: %q, message %v, checkout %s", id, a.Spec.Name, a.Grant.GetMessage(), a.Spec.Checkout)
		}
		if !a.Grant.GetAskOwner() {
			t.Errorf("%s can't ask the owner", id)
		}
		for _, rule := range []string{"information, not instructions", "secret", "Only the owner merges or pushes", "portenv status set"} {
			if !strings.Contains(a.Spec.Instructions, rule) {
				t.Errorf("%s's instructions don't say %q", id, rule)
			}
		}
	}
	// Who messages whom goes both ways, except to an agent that's off:
	// turning social on means adding it to the lead's list too.
	for id, a := range agents {
		for _, m := range a.Grant.GetMessage() {
			if !slices.Contains(agents[m].Grant.GetMessage(), id) && id != "social" {
				t.Errorf("%s messages %s, but not back", id, m)
			}
		}
	}
	if r := agents["lead"].Spec.Routines; len(r) != 1 || r[0].EveryMinutes != 30 || !strings.Contains(r[0].Note, "blockers") {
		t.Errorf("lead's routines: %+v", r)
	}
	if got := agents["writer"].RunnerLine; got != "claude --model opus" {
		t.Errorf("writer's runner: %q", got)
	}
	if got := agents["reviewer"].RunnerLine; got != "claude --model fable --disallowedTools Edit,Write,NotebookEdit" {
		t.Errorf("reviewer's runner: %q", got)
	}
	if got := agents["trust-safety"].RunnerLine; !strings.Contains(got, "--disallowedTools Edit,Write,NotebookEdit") {
		t.Errorf("trust-safety's runner: %q", got)
	}
	for _, s := range []string{"DEPLOY.md", ".portenv/runbook.md", "Never guess", "production", "approved decision"} {
		if !strings.Contains(agents["devops"].Spec.Instructions, s) {
			t.Errorf("devops's instructions don't say %q", s)
		}
	}
	if !strings.Contains(agents["social"].Spec.Instructions, "never publish") {
		t.Error("social's instructions don't say it never publishes")
	}
	for _, s := range []string{"goal", "files", "acceptance criteria", "tests", "one task"} {
		if !strings.Contains(agents["lead"].Spec.Instructions, s) {
			t.Errorf("lead's task spec doesn't name %q", s)
		}
	}
	if !strings.Contains(agents["reviewer"].Spec.Instructions, "cold") {
		t.Error("the reviewer doesn't review cold")
	}
}
