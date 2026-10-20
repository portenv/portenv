// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The command list (ADR 0027 §7): portenv help --json, made from the CLI's
// registry, and bundled in the app for S2's completions and S4's
// suggestions.

// bundledCommands is the copy the app bundles.
var bundledCommands = filepath.Join("..", "..", "..", "apps", "mac", "Resources", "portenv-commands.json")

func helpJSON(t *testing.T) []byte {
	t.Helper()
	code, out, errOut := runBoth(t, "help", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("help --json: exit %d, stderr %q", code, errOut)
	}
	return []byte(out)
}

// TestTheCommandListMatchesItsSchema checks every field against ADR 0027
// §7's schema: portenv.commands/1, a version, and for each command its
// path, aliases, summary, callers, approval, arguments and flags.
func TestTheCommandListMatchesItsSchema(t *testing.T) {
	var list struct {
		Schema   string            `json:"schema"`
		Version  string            `json:"version"`
		Commands []json.RawMessage `json:"commands"`
	}
	dec := json.NewDecoder(bytes.NewReader(helpJSON(t)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		t.Fatal(err)
	}
	semver := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	if list.Schema != "portenv.commands/1" || !semver.MatchString(list.Version) || len(list.Commands) == 0 {
		t.Fatalf("schema %q, version %q, %d commands", list.Schema, list.Version, len(list.Commands))
	}
	types := []string{"string", "text", "int", "bool", "path", "agent_id", "tab_id", "decision_id", "choice", "runbook_target", "command"}
	type param struct {
		Name     string   `json:"name"`
		Type     string   `json:"type"`
		Required *bool    `json:"required"`
		Repeated *bool    `json:"repeated"`
		Choices  []string `json:"choices"`
		Caller   []string `json:"caller"`
	}
	type command struct {
		Path         []string `json:"path"`
		Aliases      []string `json:"aliases"`
		Summary      string   `json:"summary"`
		Caller       []string `json:"caller"`
		Approval     string   `json:"approval"`
		ApprovalNote *string  `json:"approval_note"`
		Args         []param  `json:"args"`
		Flags        []param  `json:"flags"`
		Since        string   `json:"since"`
	}
	seen := map[string]bool{}
	for _, raw := range list.Commands {
		var c command
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		key := strings.Join(c.Path, " ")
		switch {
		case len(c.Path) == 0 || seen[key]:
			t.Errorf("%s: empty or repeated path", raw)
		case c.Aliases == nil || c.Args == nil || c.Flags == nil || c.ApprovalNote == nil:
			t.Errorf("%s: aliases, args, flags and approval_note are always present", key)
		case c.Summary == "" || !strings.HasSuffix(c.Summary, "."):
			t.Errorf("%s: a summary is a sentence", key)
		case !slices.Contains([]string{"never", "always", "depends"}, c.Approval):
			t.Errorf("%s: approval %q", key, c.Approval)
		case c.Approval != "never" && *c.ApprovalNote == "":
			t.Errorf("%s: approval %q needs a note", key, c.Approval)
		case !semver.MatchString(c.Since):
			t.Errorf("%s: since %q", key, c.Since)
		}
		seen[key] = true
		if len(c.Caller) == 0 {
			t.Errorf("%s: no caller", key)
		}
		for _, who := range c.Caller {
			if who != "owner" && who != "agent" {
				t.Errorf("%s: caller %q", key, who)
			}
		}
		for _, p := range append(slices.Clone(c.Args), c.Flags...) {
			if p.Name == "" || strings.HasPrefix(p.Name, "-") || !slices.Contains(types, p.Type) || p.Required == nil || p.Repeated == nil {
				t.Errorf("%s: parameter %+v", key, p)
			}
			if (p.Type == "choice") != (len(p.Choices) > 0) {
				t.Errorf("%s %s: choices go with type choice", key, p.Name)
			}
			for _, who := range p.Caller {
				if !slices.Contains(c.Caller, who) {
					t.Errorf("%s %s: caller %q isn't the command's", key, p.Name, who)
				}
			}
		}
	}
	// ADR 0027 §5's owner commands are all there, for the owner.
	for _, want := range []string{"msg", "wake", "tab new", "team start", "team stop", "deploy", "decision list", "decision answer", "status"} {
		if !seen[want] {
			t.Errorf("the list has no %q", want)
		}
	}
}

// TestTheBundledCommandListIsTheRegistrys: the app's copy is exactly what
// portenv help --json prints. Regenerate it with
// go run ./core/cmd/portenv help --json > apps/mac/Resources/portenv-commands.json.
func TestTheBundledCommandListIsTheRegistrys(t *testing.T) {
	want, err := os.ReadFile(bundledCommands)
	if err != nil {
		t.Fatal(err)
	}
	if got := helpJSON(t); !bytes.Equal(got, want) {
		t.Fatalf("%s differs from portenv help --json; regenerate it", bundledCommands)
	}
}

// TestEveryListedCommandRuns: each owner command in the list is one this
// portenv runs for the owner, each agent command one it relays for an
// agent, and every agent command is listed.
func TestEveryListedCommandRuns(t *testing.T) {
	var list struct {
		Commands []struct {
			Path   []string `json:"path"`
			Caller []string `json:"caller"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(helpJSON(t), &list); err != nil {
		t.Fatal(err)
	}
	agentFirst := map[string]bool{}
	for _, c := range list.Commands {
		for _, who := range c.Caller {
			switch who {
			case "owner":
				if !ownerTeamCommand(c.Path) && commands[c.Path[0]].run == nil && (c.Path[0] != "team" || c.Path[1] != "init") && c.Path[0] != "guide" {
					t.Errorf("%v is listed for the owner, but the owner's portenv doesn't run it", c.Path)
				}
			case "agent":
				if _, ok := teamUsage[c.Path[0]]; !ok {
					t.Errorf("%v is listed for agents, but isn't an agent's command", c.Path)
				}
				agentFirst[c.Path[0]] = true
			}
		}
	}
	for name := range teamUsage {
		if !agentFirst[name] {
			t.Errorf("the agent's %q isn't in the command list", name)
		}
	}
}
