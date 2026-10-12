// SPDX-License-Identifier: Apache-2.0

// Package notices builds and checks Portenv.app's third-party notices
// (R-0096): every Go module compiled into the app's helpers (read from each
// binary's build information, as `go version -m` shows it), Go's own licence
// for the standard library, and every Swift package pinned in
// Package.resolved. Each component's licence files, including those of code
// it vendors in subfolders, are copied into Contents/Resources/Licenses/, and
// ThirdPartyNotices.txt lists every component, its version, licence and
// source, followed by the texts.
//
// A component without a licence file, with a licence file that isn't
// recognised, or with copyleft terms, fails the build unless the reviewed
// allowlist names it, with the text added by hand and a reason.
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
	"runtime/debug"
	"sort"
	"strings"
	"unicode"
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

// GoOrigin is where a Go component's source can be had: the module's zip on
// the Go module proxy at the exact version (the same bytes the checksum
// database covers), or Go's source release for the standard library. It is
// "" for any other kind.
func GoOrigin(c Component) string {
	switch c.Kind {
	case KindGoStd:
		return "https://go.dev/dl/" + c.Version + ".src.tar.gz"
	case KindGo:
		return "https://proxy.golang.org/" + proxyEscape(c.Name) + "/@v/" + proxyEscape(c.Version) + ".zip"
	}
	return ""
}

// proxyEscape is the module proxy's case encoding: each capital letter
// becomes "!" and the letter in lower case (github.com/Azure → github.com/!azure).
func proxyEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

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
		comps, err := componentsFrom(bi)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(b), err)
		}
		for _, c := range comps {
			add(c)
		}
	}
	return out, nil
}

// componentsFrom lists one binary's components: the standard library at its
// Go version, its main module and every dependency, each after its
// replacement. A module without a released or pseudo-version (a local
// replacement, or a main module built from a working tree) is refused, since
// its licence can't be pinned. Portenv's own modules are left out.
func componentsFrom(bi *debug.BuildInfo) ([]Component, error) {
	out := []Component{{Kind: KindGoStd, Name: "Go standard library", Version: bi.GoVersion}}
	mods := []debug.Module{bi.Main}
	for _, d := range bi.Deps {
		if d.Replace != nil {
			mods = append(mods, *d.Replace)
		} else {
			mods = append(mods, *d)
		}
	}
	for _, m := range mods {
		if m.Path == "" || m.Path == OwnModulePrefix || strings.HasPrefix(m.Path, OwnModulePrefix+"/") {
			continue
		}
		if m.Version == "" || m.Version == "(devel)" {
			return nil, fmt.Errorf("module %s has no version (a local replacement or a working tree), so its licence can't be pinned", m.Path)
		}
		out = append(out, Component{Kind: KindGo, Name: m.Path, Version: m.Version})
	}
	return out, nil
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

// Origin is where the pin's source can be had: its repository at the pinned
// revision.
func (p SwiftPin) Origin() string {
	if p.Location == "" || p.Revision == "" {
		return ""
	}
	return p.Location + " at " + p.Revision
}

// CheckoutName is SwiftPM's folder for the pin under .build/checkouts: the
// last part of its URL, without .git.
func (p SwiftPin) CheckoutName() string {
	return strings.TrimSuffix(path.Base(strings.TrimSuffix(p.Location, "/")), ".git")
}

// A licence file: LICENSE, LICENCE, LICENSES, UNLICENSE, COPYING,
// COPYRIGHT, NOTICE, PATENTS or THIRD_PARTY_NOTICES, alone or with a suffix
// such as .txt, .md, -MIT, -2.0.txt, _THIRD_PARTY or .APACHE, but not a source file that happens to start with
// one of those words (license.go). Every file in a REUSE LICENSES/ folder is
// a licence file too.
var (
	licenceFileName = regexp.MustCompile(`(?i)^(un)?(licen[cs]es?|copying|copyright|notices?|patents|third[-_]party[-_]notices?)([-_.][a-z0-9._-]+)?$`)
	sourceFile      = regexp.MustCompile(`(?i)\.(go|swift|c|h|m|mm|cc|cpp|s|py|sh|js|ts|json|ya?ml|toml|html?|csv|tsv)$`)
	reuseDir        = regexp.MustCompile(`(?i)^licen[cs]es$`)
)

// skipDir names folders whose files never ship: version control, test data
// and tests.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "testdata" || name == "Tests"
}

// LicenceFiles lists the licence, copying, copyright, notice and patents
// files in a component's source folder as slash-separated paths relative to
// it, sorted. With recursive, it also looks in every subfolder (vendored code
// keeps its own licence beside it), except version control, test data and
// tests; without, only at the top and in a REUSE LICENSES/ folder there.
func LicenceFiles(dir string, recursive bool) ([]string, error) {
	var out []string
	root := filepath.Clean(dir)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		parent := path.Base(path.Dir(rel))
		inReuse := rel != "." && path.Dir(rel) != "." && reuseDir.MatchString(parent)
		if d.IsDir() {
			switch {
			case rel == ".":
				return nil
			case skipDir(d.Name()):
				return fs.SkipDir
			case reuseDir.MatchString(d.Name()):
				return nil // its files are licences, at any depth the walk reaches
			case !recursive:
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || sourceFile.MatchString(d.Name()) {
			return nil
		}
		if inReuse || licenceFileName.MatchString(d.Name()) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// isNotice is true for NOTICE, PATENTS and COPYRIGHT files, which accompany
// a licence but needn't name one.
func isNotice(name string) bool {
	n := strings.ToLower(path.Base(name))
	for _, p := range []string{"notice", "patents", "copyright", "third_party_notice", "third-party-notice"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// copyleft matches, in a lower-cased normalised text, the names of licences
// whose terms need more than a notice: GPL, LGPL and AGPL in any spelling
// (GPL-2.0, GPL-2.0-or-later, GPLv3, a Debian "License: GPL-2+", "GNU
// GENERAL PUBLIC LICENSE"), Affero, and Creative Commons ShareAlike. Every
// licence file is read, NOTICE, COPYRIGHT and PATENTS included; a match fails
// the build unless the allowlist covers the file.
var copyleft = regexp.MustCompile(`\b[al]?gpl(v?\d|-\d)|general public licen[cs]e|affero|sharealike|share-alike|\bby-sa\b`)

// mplSecondary is MPL-2.0's own definition of "Secondary License", which
// names the GNU licences without putting the code under them; only this exact
// sentence is ignored.
const mplSecondary = "means either the GNU General Public License, Version 2.0, the GNU Lesser General Public License, Version 2.1, the GNU Affero General Public License, Version 3.0, or any later versions of those licenses."

// Copyleft returns the copyleft terms found in a text, each once, as written
// in lower case.
func Copyleft(text []byte) []string {
	t := strings.ToLower(strings.ReplaceAll(normalize(text), mplSecondary, ""))
	var found []string
	for _, m := range copyleft.FindAllString(t, -1) {
		if !contains(found, m) {
			found = append(found, m)
		}
	}
	return found
}

// normalize joins a licence text into one line of single spaces, without
// the comment leaders (" * ", "//", "#") of a text kept as a code comment.
func normalize(text []byte) string {
	var words []string
	for _, l := range strings.Split(string(text), "\n") {
		l = strings.TrimSpace(l)
		for _, lead := range []string{"/*", "*/", "*", "//", "#"} {
			l = strings.TrimSpace(strings.TrimPrefix(l, lead))
		}
		words = append(words, strings.Fields(l)...)
	}
	return strings.Join(words, " ")
}

// Identify names the licences in a licence text, as SPDX identifiers in a
// fixed order. A file can hold several (a Go module with vendored code often
// adds the vendored licences below its own). It returns nothing when it
// recognises none of them.
func Identify(text []byte) []string {
	t := normalize(text)
	has := func(s string) bool { return strings.Contains(t, s) }
	var ids []string
	if has("This is free and unencumbered software released into the public domain") {
		ids = append(ids, "Unlicense")
	}
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
	openSSL := has("The OpenSSL Project") && has("Original SSLeay License")
	if openSSL {
		ids = append(ids, "OpenSSL")
	}
	if has("Redistribution and use in source and binary forms") && !openSSL {
		if has("Neither the name") || has("may be used to endorse or promote products") {
			ids = append(ids, "BSD-3-Clause")
		} else {
			ids = append(ids, "BSD-2-Clause")
		}
	}
	return ids
}

// AllowEntry is a reviewed entry for one component. With a licence, it is an
// exception: a component whose source has no licence file, or whose licence
// Identify doesn't recognise; the copyleft and vendored-code checks still
// apply. Without one, it narrows the checks instead:
// Extra adds the licences of code the component vendors but whose licence its
// source leaves out (checked like its own files), and Ignore names the files
// the reviewer has read and exempted from identification and the copyleft
// check (they still ship).
type AllowEntry struct {
	Kind    string      `json:"kind"`
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Licence string      `json:"licence"`
	Texts   []string    `json:"texts"` // hand-added files, relative to the allowlist; empty: use the component's own
	Extra   []ExtraText `json:"extra"`
	Ignore  []string    `json:"ignore"`
	Reason  string      `json:"reason"`
}

// ExtraText is a hand-added licence file (Text, relative to the allowlist)
// placed at As in the component's folder, beside its own files. Revision is
// the vendored code's revision the text belongs to, as the vendored folder's
// hash.txt states it.
type ExtraText struct {
	Text     string `json:"text"`
	As       string `json:"as"`
	Revision string `json:"revision"`
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
		if e.Name == "" || e.Version == "" || (e.Licence == "" && (len(e.Extra)+len(e.Ignore) == 0 || len(e.Texts) > 0)) || strings.TrimSpace(e.Reason) == "" {
			return nil, fmt.Errorf("%s: every entry needs kind, name, version, a reason and a licence (or only extra or ignored files)", file)
		}
		if _, ok := kindOrder[e.Kind]; !ok {
			return nil, fmt.Errorf("%s: %s %s: kind %q isn't %s, %s or %s", file, e.Name, e.Version, e.Kind, KindGo, KindGoStd, KindSwift)
		}
		c := Component{Kind: e.Kind, Name: e.Name, Version: e.Version}
		if _, dup := a.entries[c]; dup {
			return nil, fmt.Errorf("%s: %s %s is listed twice", file, e.Name, e.Version)
		}
		names := map[string]bool{}
		for _, t := range e.Texts {
			if !filepath.IsLocal(t) || strings.Contains(filepath.ToSlash(t), "../") {
				return nil, fmt.Errorf("%s: %s %s: text %q must be a path inside %s", file, e.Name, e.Version, t, a.dir)
			}
			if b := strings.ToLower(filepath.Base(t)); names[b] {
				return nil, fmt.Errorf("%s: %s %s: two texts named %s", file, e.Name, e.Version, filepath.Base(t))
			} else {
				names[b] = true
			}
		}
		for _, x := range e.Extra {
			for _, p := range []string{x.Text, x.As} {
				if p == "" || !filepath.IsLocal(p) || strings.Contains(filepath.ToSlash(p), "../") {
					return nil, fmt.Errorf("%s: %s %s: extra path %q must be a path inside the folder", file, e.Name, e.Version, p)
				}
			}
			if as := strings.ToLower(path.Clean(filepath.ToSlash(x.As))); names[as] {
				return nil, fmt.Errorf("%s: %s %s: two extra texts at %s", file, e.Name, e.Version, x.As)
			} else {
				names[as] = true
			}
			if !revision.MatchString(x.Revision) {
				return nil, fmt.Errorf("%s: %s %s: extra %s needs the vendored revision it belongs to (hash.txt's)", file, e.Name, e.Version, x.As)
			}
		}
		for _, n := range e.Ignore {
			if n == "" || !filepath.IsLocal(n) || strings.Contains(filepath.ToSlash(n), "../") {
				return nil, fmt.Errorf("%s: %s %s: ignored file %q must be a path inside the component", file, e.Name, e.Version, n)
			}
		}
		a.entries[c] = e
	}
	return a, nil
}

// Source finds a component's source folder; it returns "" when there is none.
type Source func(Component) (string, error)

// Origin says where a component's source can be had (GoOrigin,
// SwiftPin.Origin); "" means unknown.
type Origin func(Component) string

// Entry is one component's notice: its licence name, its source and files.
type Entry struct {
	Component
	Licence string
	Source  string
	Files   []NamedText
}

// NamedText is one licence file; Name is its slash-separated path in the
// component's source (LICENSE, or vendor/boringssl/LICENSE).
type NamedText struct {
	Name string
	Text []byte
}

// Collect gathers each component's licence files, names its licence and
// says where its source is. It returns every problem at once, so one build
// shows all of them.
func Collect(comps []Component, src Source, origin Origin, allow *Allowlist) ([]Entry, error) {
	var out []Entry
	var problems []string
	for _, c := range comps {
		e, err := collectOne(c, src, origin, allow)
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

func collectOne(c Component, src Source, origin Origin, allow *Allowlist) (Entry, error) {
	e := Entry{Component: c, Source: origin(c)}
	if e.Source == "" {
		return e, fmt.Errorf("%s %s: no source to point to", c.Name, c.Version)
	}
	ae, listed := allow.entries[c]
	allowed := listed && ae.Licence != ""
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
		// Go's own tree vendors only the Go Authors' code under the same
		// licence, and holds the toolchain's sources too: its top folder is
		// the standard library's licence.
		names, err := LicenceFiles(dir, c.Kind != KindGoStd)
		if err != nil {
			return e, fmt.Errorf("%s %s: %w", c.Name, c.Version, err)
		}
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(n))) // #nosec G304 -- licence files in the module cache or a checkout
			if err != nil {
				return e, err
			}
			if len(bytes.TrimSpace(b)) == 0 {
				continue
			}
			e.Files = append(e.Files, NamedText{Name: n, Text: b})
		}
		var vendored []vendoredFolder
		if c.Kind != KindGoStd {
			if vendored, err = vendoredFolders(dir); err != nil {
				return e, fmt.Errorf("%s %s: %w", c.Name, c.Version, err)
			}
		}
		for _, x := range ae.Extra {
			as := path.Clean(filepath.ToSlash(x.As))
			for _, f := range e.Files {
				if f.Name == as {
					return e, fmt.Errorf("%s %s: allowlist extra %s is already in the source", c.Name, c.Version, as)
				}
			}
			// The text belongs to one vendored revision: an update that
			// vendors another fails until the entry is updated with it.
			v, ok := vendoredFolderOf(vendored, as)
			switch {
			case !ok:
				return e, fmt.Errorf("%s %s: allowlist extra %s isn't in a vendored folder (one with a hash.txt)", c.Name, c.Version, as)
			case v.revision != x.Revision:
				return e, fmt.Errorf("%s %s: allowlist extra %s is for revision %s, but %s/hash.txt vendors %s: add that revision's licence", c.Name, c.Version, as, x.Revision, v.folder, v.revision)
			}
			b, err := os.ReadFile(filepath.Join(allow.dir, x.Text)) // #nosec G304 -- hand-added texts beside the reviewed allowlist
			if err != nil {
				return e, fmt.Errorf("%s %s: allowlist extra: %w", c.Name, c.Version, err)
			}
			e.Files = append(e.Files, NamedText{Name: as, Text: b})
		}
		sort.Slice(e.Files, func(i, j int) bool { return e.Files[i].Name < e.Files[j].Name })
		if missing := vendoredWithoutLicence(vendored, e.Files); len(missing) > 0 {
			return e, fmt.Errorf("%s %s: vendored code without its licence in %s: add its text to the allowlist's extra", c.Name, c.Version, strings.Join(missing, ", "))
		}
	}
	// Each licence file is identified on its own, so a recognised LICENSE
	// can't hide an unrecognised COPYING beside it, and no file may carry
	// copyleft terms.
	for _, n := range ae.Ignore {
		found := false
		for _, f := range e.Files {
			found = found || f.Name == path.Clean(filepath.ToSlash(n))
		}
		if !found {
			return e, fmt.Errorf("%s %s: the allowlist ignores %s, which isn't among its licence files", c.Name, c.Version, n)
		}
	}
	var ids, unknown, copyleftIn []string
	licences := 0
	for _, f := range e.Files {
		if contains(ae.Ignore, f.Name) {
			continue // read by the reviewer; it still ships
		}
		if m := Copyleft(f.Text); len(m) > 0 {
			copyleftIn = append(copyleftIn, f.Name+" ("+strings.Join(m, ", ")+")")
		}
		if isNotice(f.Name) {
			continue
		}
		licences++
		found := Identify(f.Text)
		if len(found) == 0 {
			unknown = append(unknown, f.Name)
		}
		for _, id := range found {
			if !contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	switch {
	case len(copyleftIn) > 0:
		return e, fmt.Errorf("%s %s: copyleft terms in %s: the allowlist can ignore a file the reviewer has read", c.Name, c.Version, strings.Join(copyleftIn, ", "))
	case allowed:
		e.Licence = ae.Licence
	case licences == 0:
		return e, fmt.Errorf("%s %s: no licence file", c.Name, c.Version)
	case len(unknown) > 0:
		return e, fmt.Errorf("%s %s: licence not recognised in %s", c.Name, c.Version, strings.Join(unknown, ", "))
	default:
		e.Licence = strings.Join(ids, ", ")
	}
	if len(e.Files) == 0 {
		return e, fmt.Errorf("%s %s: no licence text", c.Name, c.Version)
	}
	return e, nil
}

// vendoredMarker is the first line SwiftPM packages that vendor another
// project write into the vendored folder's hash.txt ("This directory is
// derived from BoringSSL cloned from … at revision 817ab07e…"); their
// vendoring scripts leave the project's LICENSE out.
var (
	vendoredMarker = regexp.MustCompile(`(?i)^This directory is derived from \S+ cloned from \S+(?: at revision ([0-9a-f]{7,64}))?`)
	revision       = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// vendoredFolder is a folder of vendored code: its slash path in the
// component and the revision its hash.txt states.
type vendoredFolder struct{ folder, revision string }

// vendoredFolders finds every hash.txt with vendoredMarker in a component's
// source, at any depth except version control, test data and tests.
func vendoredFolders(dir string) ([]vendoredFolder, error) {
	var out []vendoredFolder
	root := filepath.Clean(dir)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "hash.txt" || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 G122 -- reading (never writing) a hash.txt in the module cache or a checkout, both build inputs
		if err != nil {
			return err
		}
		m := vendoredMarker.FindSubmatch(bytes.TrimSpace(b))
		if m == nil {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		out = append(out, vendoredFolder{folder: filepath.ToSlash(rel), revision: string(m[1])})
		return nil
	})
	return out, err
}

// vendoredFolderOf is the vendored folder a file is in (the deepest one).
func vendoredFolderOf(vs []vendoredFolder, name string) (vendoredFolder, bool) {
	var best vendoredFolder
	ok := false
	for _, v := range vs {
		if (v.folder == "." || strings.HasPrefix(name, v.folder+"/")) && (!ok || len(v.folder) > len(best.folder)) {
			best, ok = v, true
		}
	}
	return best, ok
}

// vendoredWithoutLicence lists the vendored folders with no licence file
// among files.
func vendoredWithoutLicence(vs []vendoredFolder, files []NamedText) []string {
	var out []string
	for _, v := range vs {
		found := false
		for _, f := range files {
			if (v.folder == "." || strings.HasPrefix(f.Name, v.folder+"/")) && !isNotice(f.Name) {
				found = true
			}
		}
		if !found {
			out = append(out, v.folder)
		}
	}
	sort.Strings(out)
	return out
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
			p := filepath.Join(dir, filepath.FromSlash(f.Name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // #nosec G301 -- inside the app bundle, read by everyone who runs the app
				return err
			}
			if err := os.WriteFile(p, f.Text, 0o644); err != nil { // #nosec G306 -- a public licence text in the app bundle
				return err
			}
		}
	}
	return os.WriteFile(filepath.Join(app, filepath.FromSlash(NoticesFile)), Render(entries), 0o644) // #nosec G306 -- public notices in the app bundle
}

// Render is ThirdPartyNotices.txt: one line per component ("name version ·
// licence"), then each component's section with its licence texts, in the
// same order. The list is for reading, so it has no columns to break in a
// narrow window.
func Render(entries []Entry) []byte {
	var b bytes.Buffer
	b.WriteString("Portenv includes the following third-party software. Each component's\n")
	b.WriteString("licence follows below in full, in the same order.\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "%s %s · %s\n", e.Name, e.Version, e.Licence)
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "\n\n%s (%s)\nSource: %s\n", e.Header(), e.Licence, e.Source)
		for _, f := range e.Files {
			fmt.Fprintf(&b, "\n-- %s --\n\n", f.Name)
			b.Write(bytes.TrimRight(f.Text, "\n"))
			b.WriteString("\n")
		}
	}
	return b.Bytes()
}

// Check confirms that an app's notices cover the given components: each has
// its folder with at least one non-empty licence file (a NOTICE alone isn't
// enough), and its section in ThirdPartyNotices.txt.
func Check(app string, comps []Component) error {
	notices, err := os.ReadFile(filepath.Join(app, filepath.FromSlash(NoticesFile))) // #nosec G304 -- the notices file inside the app being checked
	if err != nil {
		return fmt.Errorf("third-party notices: %w", err)
	}
	var headers []string
	for _, l := range strings.Split(string(notices), "\n") {
		if strings.HasPrefix(l, "== ") {
			headers = append(headers, l)
		}
	}
	hasSection := func(c Component) bool {
		for _, h := range headers {
			if strings.HasPrefix(h, c.Header()+" (") {
				return true
			}
		}
		return false
	}
	var missing []string
	for _, c := range comps {
		dir := filepath.Join(app, filepath.FromSlash(LicensesDir), filepath.FromSlash(c.Dir()))
		if !hasLicenceFile(dir) || !hasSection(c) {
			missing = append(missing, c.Name+" "+c.Version)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("third-party notices don't cover:\n  %s\nRebuild with gmake app", strings.Join(missing, "\n  "))
	}
	return nil
}

func hasLicenceFile(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return fs.SkipAll
		}
		if d.Type().IsRegular() && !isNotice(d.Name()) {
			if info, err := d.Info(); err == nil && info.Size() > 0 {
				found = true
			}
		}
		return nil
	})
	return found
}
