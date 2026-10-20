// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
)

//go:embed templates
var templates embed.FS

// Templates are the names portenv team init offers.
var Templates = []string{"starter", "orchestrate", "app-product"}

// OffDir is the folder, inside a template and inside the project's
// .portenv/agents/, for agents a template ships off by default. portenvd
// and the helper read only <id>.md directly in .portenv/agents/, so a file
// in off/ is never loaded, approved or started; the owner turns one on by
// moving it up a folder, then approves it like any other.
const OffDir = "off"

// Template is a template's agent files, by path relative to
// .portenv/agents/: <id>.md, or off/<id>.md for an agent that's off.
func Template(name string) (map[string][]byte, error) {
	dir := path.Join("templates", name)
	if !slices.Contains(Templates, name) {
		return nil, fmt.Errorf("no template %q (there are %v)", name, Templates)
	}
	out := map[string][]byte{}
	err := fs.WalkDir(templates, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := templates.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, dir+"/")] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TemplateFiles is a template's file paths, sorted.
func TemplateFiles(name string) []string {
	files, _ := Template(name)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// OnAgents is the ids of a template's agents that are on by default.
func OnAgents(name string) []string { return agentIDs(name, false) }

// OffAgents is the ids of a template's agents that are off by default.
func OffAgents(name string) []string { return agentIDs(name, true) }

func agentIDs(name string, off bool) []string {
	var ids []string
	for _, f := range TemplateFiles(name) {
		rest, inOff := strings.CutPrefix(f, OffDir+"/")
		if inOff == off {
			ids = append(ids, strings.TrimSuffix(rest, ".md"))
		}
	}
	return ids
}
