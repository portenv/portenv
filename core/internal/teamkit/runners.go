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
	// TrustSetting is how the helper pre-answers the runner's folder-trust
	// question for the folder it starts in (ADR 0025 §9, R-0411): "claude"
	// is Claude Code's projects["<folder>"].hasTrustDialogAccepted in
	// ~/.claude.json. Empty: Portenv writes nothing, and the runner asks.
	TrustSetting string
	// SettingsFlag names a settings file on the runner's command line
	// (ADR 0025 §9's settings row): Claude Code's --settings, ranked above
	// every settings file the agent can edit. Empty: no settings.
	SettingsFlag string
}

// Runners is the registry, in menu order.
var Runners = []Runner{
	{ID: "claude", Name: "Claude Code", ModelFlag: "--model", Models: []string{"opus", "sonnet", "fable"}, Shown: true, TrustSetting: "claude", SettingsFlag: "--settings"},
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

// TrustSettingFor is the trust setting of the runner whose program is
// named name (shown or not), or "" for one Portenv doesn't know.
func TrustSettingFor(name string) string {
	for _, r := range Runners {
		if r.ID == name {
			return r.TrustSetting
		}
	}
	return ""
}

// SettingsFlagFor is the settings flag of the runner whose program is
// named name (shown or not), or "".
func SettingsFlagFor(name string) string {
	for _, r := range Runners {
		if r.ID == name {
			return r.SettingsFlag
		}
	}
	return ""
}
