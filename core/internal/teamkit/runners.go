// SPDX-License-Identifier: Apache-2.0

package teamkit

// The runners the New-workspace Team step offers (ADR 0025 §9). This is
// the registry's first slice: the menus list only shown entries, so
// Anthropic's Claude Code with its own model aliases. An agent file still
// names its runner as an argv (`runner: claude --model opus`), as the
// built-in templates write it; the Team step changes only the --model
// value, and refuses a template whose runner program isn't a shown entry.

// Runner is one runner the Team step can offer.
type Runner struct {
	// ID is the program's name, the runner line's first word.
	ID   string
	Name string
	// ModelFlag sets the model on the runner's command line.
	ModelFlag string
	// Models are the runner's own aliases, in menu order.
	Models []string
	Shown  bool
}

// Runners is the registry, in menu order.
var Runners = []Runner{
	{ID: "claude", Name: "Claude Code", ModelFlag: "--model", Models: []string{"opus", "sonnet", "fable"}, Shown: true},
}

// RunnerByID is the shown runner with that id.
func RunnerByID(id string) (Runner, bool) {
	for _, r := range Runners {
		if r.ID == id && r.Shown {
			return r, true
		}
	}
	return Runner{}, false
}
