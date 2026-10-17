// SPDX-License-Identifier: Apache-2.0

package teamkit

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

//go:embed templates
var templates embed.FS

// Templates are the names portenv team init offers.
var Templates = []string{"starter", "orchestrate"}

// Template is a template's agent files, by file name (id.md).
func Template(name string) (map[string][]byte, error) {
	dir := path.Join("templates", name)
	entries, err := fs.ReadDir(templates, dir)
	if err != nil {
		return nil, fmt.Errorf("no template %q (there are %v)", name, Templates)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		b, err := templates.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = b
	}
	return out, nil
}

// TemplateFiles is a template's file names, sorted.
func TemplateFiles(name string) []string {
	files, _ := Template(name)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
