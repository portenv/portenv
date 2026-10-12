// SPDX-License-Identifier: Apache-2.0

// Package notices builds and checks Portenv.app's third-party notices
// (R-0096): every Go module compiled into the app's helpers (read from each
// binary's build information, as `go version -m` shows it), Go's own licence
// for the standard library, and every Swift package pinned in
// Package.resolved. Each component's licence files are copied into
// Contents/Resources/Licenses/, and ThirdPartyNotices.txt lists every
// component, its version and licence, followed by the texts.
//
// A component without a licence file, or whose licence isn't recognised,
// fails the build unless the reviewed allowlist names it, with the text added
// by hand and a reason.
package notices

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kinds of component.
const (
	KindGoStd = "go-std"
	KindGo    = "go"
	KindSwift = "swift"
)

// OwnModulePrefix is Portenv's own code, covered by its own licence.
const OwnModulePrefix = "github.com/portenv/portenv"

// File names inside the app.
const (
	LicensesDir = "Contents/Resources/Licenses"
	NoticesFile = "Contents/Resources/ThirdPartyNotices.txt"
)

// Component is one piece of third-party code in the app.
type Component struct {
	Kind    string
	Name    string // module path, Swift package identity, or "Go standard library"
	Version string
}

// Dir is the component's folder under Contents/Resources/Licenses.
func (c Component) Dir() string {
	switch c.Kind {
	case KindGoStd:
		return path.Join("go", "std@"+c.Version)
	case KindGo:
		return path.Join("go", c.Name+"@"+c.Version)
	default:
		return path.Join("swift", c.Name+"@"+c.Version)
	}
}

// Header is the line that starts the component's section in
// ThirdPartyNotices.txt; Check looks for it.
func (c Component) Header() string { return "== " + c.Name + " " + c.Version }

// GoComponents reads the build information of each Go binary: the
// standard library at the binary's Go version, the main module (restic's own,
// for example) and every dependency, after replacements. Portenv's own modules
// are left out. A file without Go build information is an error: every helper
// must be a Go binary whose modules can be listed.
func GoComponents(binaries []string) ([]Component, error) {
	seen := map[Component]bool{}
	var out []Component
	add := func(c Component) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, b := range binaries {
		bi, err := buildinfo.ReadFile(b)
		if err != nil {
			return nil, fmt.Errorf("%s: no Go build information (%w): every helper must be a Go binary so its modules can be listed", filepath.Base(b), err)
		}
		add(Component{Kind: KindGoStd, Name: "Go standard library", Version: bi.GoVersion})
		mods := []*modRef{{bi.Main.Path, bi.Main.Version, nil}}
		for _, d := range bi.Deps {
			var r *modRef
			if d.Replace != nil {
				r = &modRef{d.Replace.Path, d.Replace.Version, nil}
			}
			mods = append(mods, &modRef{d.Path, d.Version, r})
		}
		for _, m := range mods {
			if m.replace != nil {
				m = m.replace
			}
			if m.path == "" || strings.HasPrefix(m.path, OwnModulePrefix) {
				continue
			}
			if m.version == "" || m.version == "(devel)" {
				return nil, fmt.Errorf("%s: module %s has no version, so its licence can't be pinned", filepath.Base(b), m.path)
			}
			add(Component{Kind: KindGo, Name: m.path, Version: m.version})
		}
	}
	return out, nil
}

type modRef struct {
	path, version string
	replace       *modRef
}

// SwiftPin is one entry of Package.resolved.
type SwiftPin struct {
	Identity string
	Location string
	Version  string
	Revision string
}

// SwiftPins reads Package.resolved (version 2 or 3).
func SwiftPins(resolved string) ([]SwiftPin, error) {
	data, err := os.ReadFile(resolved) // #nosec G304 -- the repository's Package.resolved, named by the Makefile
	if err != nil {
		return nil, err
	}
	var doc struct {
		Pins []struct {
			Identity string `json:"identity"`
			Location string `json:"location"`
			State    struct {
				Version  string `json:"version"`
				Revision string `json:"revision"`
			} `json:"state"`
		} `json:"pins"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", resolved, err)
	}
	if len(doc.Pins) == 0 {
		return nil, fmt.Errorf("%s: no pins", resolved)
	}
	var out []SwiftPin
	for _, p := range doc.Pins {
		v := p.State.Version
		if v == "" {
			v = p.State.Revision
		}
		out = append(out, SwiftPin{Identity: p.Identity, Location: p.Location, Version: v, Revision: p.State.Revision})
	}
	return out, nil
}

// Component is the pin as a component.
func (p SwiftPin) Component() Component {
	return Component{Kind: KindSwift, Name: p.Identity, Version: p.Version}
}

// CheckoutName is SwiftPM's folder for the pin under .build/checkouts: the
// last part of its URL, without .git.
func (p SwiftPin) CheckoutName() string {
	return strings.TrimSuffix(path.Base(strings.TrimSuffix(p.Location, "/")), ".git")
}

// A licence file: LICENSE, LICENCE, COPYING, NOTICE or PATENTS, alone or
// with a suffix such as .txt, .md, -MIT or .APACHE, but not a source file
// that happens to start with one of those words (license.go).
var (
	licenceFileName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents)([-_.][a-z0-9-]+)?(\.(txt|md|rst))?$`)
	sourceFile      = regexp.MustCompile(`(?i)\.(go|swift|c|h|m|mm|cc|cpp|s|py|sh|js|ts|json|ya?ml|toml|html?)$`)
)

// LicenceFiles lists the licence, copying, notice and patents files at the
// top of a component's source folder, sorted.
func LicenceFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && licenceFileName.MatchString(n) && !sourceFile.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// isNotice is true for NOTICE and PATENTS files, which accompany a licence
// but don't name it.
func isNotice(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "notice") || strings.HasPrefix(n, "patents")
}

// Identify names the licences in a licence text, as SPDX identifiers in a
// fixed order. A file can hold several (a Go module with vendored code often
// adds the vendored licences below its own). It returns nothing when it
// recognises none of them.
func Identify(text []byte) []string {
	t := strings.Join(strings.Fields(string(text)), " ")
	has := func(s string) bool { return strings.Contains(t, s) }
	var ids []string
	if has("Apache License") && has("Version 2.0") {
		if has("Runtime Library Exception") {
			ids = append(ids, "Apache-2.0 WITH Swift-exception")
		} else {
			ids = append(ids, "Apache-2.0")
		}
	}
	if has("Mozilla Public License Version 2.0") || has("Mozilla Public License, version 2.0") {
		ids = append(ids, "MPL-2.0")
	}
	switch {
	case has("Boost Software License"):
		ids = append(ids, "BSL-1.0")
	case has("Permission is hereby granted, free of charge"):
		ids = append(ids, "MIT")
	}
	if has("Permission to use, copy, modify, and/or distribute this software for any purpose") ||
		has("Permission to use, copy, modify, and distribute this software for any purpose with or without fee") {
		ids = append(ids, "ISC")
	}
	if has("Redistribution and use in source and binary forms") {
		if has("Neither the name") || has("may be used to endorse or promote products") {
			ids = append(ids, "BSD-3-Clause")
		} else {
			ids = append(ids, "BSD-2-Clause")
		}
	}
	return ids
}

// AllowEntry is a reviewed exception: a component whose source has no
// licence file, or whose licence Identify doesn't recognise.
type AllowEntry struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Licence string   `json:"licence"`
	Texts   []string `json:"texts"` // hand-added files, relative to the allowlist; empty: use the component's own
	Reason  string   `json:"reason"`
}

// Allowlist is the reviewed list of exceptions.
type Allowlist struct {
	dir     string
	entries map[Component]AllowEntry
}

// LoadAllowlist reads the allowlist; a missing file is an empty list.
func LoadAllowlist(file string) (*Allowlist, error) {
	a := &Allowlist{dir: filepath.Dir(file), entries: map[Component]AllowEntry{}}
	data, err := os.ReadFile(file) // #nosec G304 -- the repository's reviewed allowlist
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	var list []AllowEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	for _, e := range list {
		if e.Name == "" || e.Version == "" || e.Licence == "" || strings.TrimSpace(e.Reason) == "" {
			return nil, fmt.Errorf("%s: every entry needs kind, name, version, licence and a reason", file)
		}
		a.entries[Component{Kind: e.Kind, Name: e.Name, Version: e.Version}] = e
	}
	return a, nil
}

// Source finds a component's source folder; it returns "" when there is none.
type Source func(Component) (string, error)

// Entry is one component's notice: its licence name and files.
type Entry struct {
	Component
	Licence string
	Files   []NamedText
}

// NamedText is one licence file.
type NamedText struct {
	Name string
	Text []byte
}

// Collect gathers each component's licence files and names its licence. It
// returns every problem at once, so one build shows all of them.
func Collect(comps []Component, src Source, allow *Allowlist) ([]Entry, error) {
	var out []Entry
	var problems []string
	for _, c := range comps {
		e, err := collectOne(c, src, allow)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out = append(out, e)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("third-party notices:\n  %s\nAdd the licence to the reviewed allowlist only with its text and a reason", strings.Join(problems, "\n  "))
	}
	sortEntries(out)
	return out, nil
}

func collectOne(c Component, src Source, allow *Allowlist) (Entry, error) {
	e := Entry{Component: c}
	ae, allowed := allow.entries[c]
	if allowed && len(ae.Texts) > 0 {
		for _, rel := range ae.Texts {
			b, err := os.ReadFile(filepath.Join(allow.dir, rel)) // #nosec G304 -- hand-added texts beside the reviewed allowlist
			if err != nil {
				return e, fmt.Errorf("%s %s: allowlist text: %w", c.Name, c.Version, err)
			}
			e.Files = append(e.Files, NamedText{Name: filepath.Base(rel), Text: b})
		}
	} else {
		dir, err := src(c)
		if err != nil {
			return e, fmt.Errorf("%s %s: %w", c.Name, c.Version, err)
		}
		names, err := LicenceFiles(dir)
		if err != nil {
			return e, fmt.Errorf("%s %s: %w", c.Name, c.Version, err)
		}
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(dir, n)) // #nosec G304 -- licence files in the module cache or a checkout
			if err != nil {
				return e, err
			}
			if len(bytes.TrimSpace(b)) == 0 {
				continue
			}
			e.Files = append(e.Files, NamedText{Name: n, Text: b})
		}
	}
	var ids []string
	licences := 0
	for _, f := range e.Files {
		if isNotice(f.Name) {
			continue
		}
		licences++
		for _, id := range Identify(f.Text) {
			if !contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	switch {
	case allowed:
		e.Licence = ae.Licence
	case licences == 0:
		return e, fmt.Errorf("%s %s: no licence file", c.Name, c.Version)
	case len(ids) == 0:
		return e, fmt.Errorf("%s %s: licence not recognised", c.Name, c.Version)
	default:
		e.Licence = strings.Join(ids, " AND ")
	}
	if len(e.Files) == 0 {
		return e, fmt.Errorf("%s %s: no licence text", c.Name, c.Version)
	}
	return e, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var kindOrder = map[string]int{KindGoStd: 0, KindGo: 1, KindSwift: 2}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Version < b.Version
	})
}

// Write puts the licence files under Contents/Resources/Licenses and writes
// ThirdPartyNotices.txt into the app.
func Write(app string, entries []Entry) error {
	lic := filepath.Join(app, filepath.FromSlash(LicensesDir))
	if err := os.RemoveAll(lic); err != nil {
		return err
	}
	for _, e := range entries {
		dir := filepath.Join(lic, filepath.FromSlash(e.Dir()))
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- inside the app bundle, read by everyone who runs the app
			return err
		}
		for _, f := range e.Files {
			if err := os.WriteFile(filepath.Join(dir, f.Name), f.Text, 0o644); err != nil { // #nosec G306 -- a public licence text in the app bundle
				return err
			}
		}
	}
	return os.WriteFile(filepath.Join(app, filepath.FromSlash(NoticesFile)), Render(entries), 0o644) // #nosec G306 -- public notices in the app bundle
}

// Render is ThirdPartyNotices.txt: a table of every component, then each
// component's licence texts in the same order.
func Render(entries []Entry) []byte {
	var b bytes.Buffer
	b.WriteString("Portenv includes the following third-party software. Each component's\n")
	b.WriteString("licence follows below in full, in the same order.\n\n")
	nameW, verW := len("Component"), len("Version")
	for _, e := range entries {
		nameW = max(nameW, len(e.Name))
		verW = max(verW, len(e.Version))
	}
	fmt.Fprintf(&b, "%-*s  %-*s  %s\n", nameW, "Component", verW, "Version", "Licence")
	for _, e := range entries {
		fmt.Fprintf(&b, "%-*s  %-*s  %s\n", nameW, e.Name, verW, e.Version, e.Licence)
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "\n\n%s (%s)\n", e.Header(), e.Licence)
		for _, f := range e.Files {
			fmt.Fprintf(&b, "\n-- %s --\n\n", f.Name)
			b.Write(bytes.TrimRight(f.Text, "\n"))
			b.WriteString("\n")
		}
	}
	return b.Bytes()
}

// Check confirms that an app's notices cover the given components: each has
// its folder with at least one non-empty file, and its section in
// ThirdPartyNotices.txt.
func Check(app string, comps []Component) error {
	notices, err := os.ReadFile(filepath.Join(app, filepath.FromSlash(NoticesFile))) // #nosec G304 -- the notices file inside the app being checked
	if err != nil {
		return fmt.Errorf("third-party notices: %w", err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(notices), "\n") {
		if strings.HasPrefix(l, "== ") {
			if i := strings.LastIndex(l, " ("); i > 0 {
				lines[l[:i]] = true
			}
		}
	}
	var missing []string
	for _, c := range comps {
		dir := filepath.Join(app, filepath.FromSlash(LicensesDir), filepath.FromSlash(c.Dir()))
		if !hasNonEmptyFile(dir) || !lines[c.Header()] {
			missing = append(missing, c.Name+" "+c.Version)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("third-party notices don't cover:\n  %s\nRebuild with gmake app", strings.Join(missing, "\n  "))
	}
	return nil
}

func hasNonEmptyFile(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}
