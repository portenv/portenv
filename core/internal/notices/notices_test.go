// SPDX-License-Identifier: Apache-2.0

package notices

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	apacheText = "Apache License\n Version 2.0, January 2004\n http://www.apache.org/licenses/"
	mitText    = "MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy"
	bsd2Text   = "Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:\n1. ...\n2. ..."
	bsd3Text   = bsd2Text + "\n3. Neither the name of the copyright holder nor the names of its contributors may be used"
)

func TestIdentify(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"apache", apacheText, []string{"Apache-2.0"}},
		{"swift runtime exception", apacheText + "\n## Runtime Library Exception to the Apache 2.0 License: ##", []string{"Apache-2.0 WITH Swift-exception"}},
		{"mit", mitText, []string{"MIT"}},
		{"mit, wrapped differently", "Permission is hereby granted,\n   free of charge, to any person", []string{"MIT"}},
		{"bsd-2", bsd2Text, []string{"BSD-2-Clause"}},
		{"bsd-3", bsd3Text, []string{"BSD-3-Clause"}},
		{"isc", "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted", []string{"ISC"}},
		{"mpl", "Mozilla Public License Version 2.0\n==================================", []string{"MPL-2.0"}},
		{"boost, not mit", "Boost Software License - Version 1.0\nPermission is hereby granted, free of charge, to any person or organization", []string{"BSL-1.0"}},
		{"several in one file", apacheText + "\n\n" + mitText + "\n\n" + bsd3Text, []string{"Apache-2.0", "MIT", "BSD-3-Clause"}},
		{"unknown", "All rights reserved. Do what you like.", nil},
	} {
		if got := Identify([]byte(tc.text)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Identify = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLicenceFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"LICENSE", "LICENSE.txt", "LICENCE-MIT", "LICENSE.APACHE", "COPYING", "NOTICE.md", "PATENTS",
		"license.go", "LICENSE_test.go", "README.md", "licensing.md", "NOTICES.json",
	} {
		write(t, filepath.Join(dir, n), "x")
	}
	if err := os.Mkdir(filepath.Join(dir, "LICENSES"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := LicenceFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"COPYING", "LICENCE-MIT", "LICENSE", "LICENSE.APACHE", "LICENSE.txt", "NOTICE.md", "PATENTS"}
	if !slices.Equal(got, want) {
		t.Errorf("LicenceFiles = %q, want %q", got, want)
	}
}

// The test binary itself has Go build information: the standard library at
// this Go, and none of Portenv's own modules.
func TestGoComponentsReadsBuildInformation(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	comps, err := GoComponents([]string{exe, exe})
	if err != nil {
		t.Fatal(err)
	}
	std := Component{Kind: KindGoStd, Name: "Go standard library", Version: runtime.Version()}
	if !slices.Contains(comps, std) {
		t.Errorf("components %v don't include %v", comps, std)
	}
	seen := map[Component]bool{}
	for _, c := range comps {
		if strings.HasPrefix(c.Name, OwnModulePrefix) {
			t.Errorf("Portenv's own module %s listed as third-party", c.Name)
		}
		if seen[c] {
			t.Errorf("%v listed twice", c)
		}
		seen[c] = true
	}
}

func TestGoComponentsRefusesANonGoHelper(t *testing.T) {
	f := filepath.Join(t.TempDir(), "helper")
	write(t, f, "#!/bin/sh\n")
	if _, err := GoComponents([]string{f}); err == nil || !strings.Contains(err.Error(), "no Go build information") {
		t.Fatalf("GoComponents on a shell script: %v, want a refusal", err)
	}
}

func TestSwiftPins(t *testing.T) {
	f := filepath.Join(t.TempDir(), "Package.resolved")
	write(t, f, `{"pins":[
	  {"identity":"swiftterm","kind":"remoteSourceControl","location":"https://github.com/migueldeicaza/SwiftTerm.git","state":{"revision":"5d14","version":"1.20.0"}},
	  {"identity":"swift-argument-parser","kind":"remoteSourceControl","location":"https://github.com/apple/swift-argument-parser","state":{"revision":"6a52"}}
	],"version":3}`)
	pins, err := SwiftPins(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins[0].CheckoutName() != "SwiftTerm" || pins[1].CheckoutName() != "swift-argument-parser" {
		t.Fatalf("pins %+v", pins)
	}
	if c := pins[1].Component(); c.Version != "6a52" {
		t.Errorf("a pin without a version uses its revision: %+v", c)
	}
}

// world is a set of fake component sources.
type world struct {
	t    *testing.T
	dirs map[Component]string
}

func newWorld(t *testing.T) *world { return &world{t: t, dirs: map[Component]string{}} }

func (w *world) add(c Component, files map[string]string) Component {
	dir := filepath.Join(w.t.TempDir(), "src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.t.Fatal(err)
	}
	for n, text := range files {
		write(w.t, filepath.Join(dir, n), text)
	}
	w.dirs[c] = dir
	return c
}

func (w *world) source(c Component) (string, error) {
	if d, ok := w.dirs[c]; ok {
		return d, nil
	}
	return "", errors.New("no source")
}

func emptyAllowlist(t *testing.T) *Allowlist {
	a, err := LoadAllowlist(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestGenerateThenCheck(t *testing.T) {
	w := newWorld(t)
	std := w.add(Component{KindGoStd, "Go standard library", "go1.26.7"}, map[string]string{"LICENSE": bsd3Text, "PATENTS": "Additional IP Rights Grant"})
	restic := w.add(Component{KindGo, "github.com/restic/restic", "v0.19.1"}, map[string]string{"LICENSE": bsd2Text, "main.go": "package main"})
	nio := w.add(Component{KindSwift, "swift-nio", "2.104.0"}, map[string]string{"LICENSE.txt": apacheText + " Runtime Library Exception", "NOTICE.txt": "SwiftNIO notice"})
	term := w.add(Component{KindSwift, "swiftterm", "1.20.0"}, map[string]string{"LICENSE": mitText})
	comps := []Component{term, restic, nio, std}

	entries, err := Collect(comps, w.source, emptyAllowlist(t))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range entries {
		order = append(order, e.Name)
	}
	if want := []string{"Go standard library", "github.com/restic/restic", "swift-nio", "swiftterm"}; !slices.Equal(order, want) {
		t.Errorf("order %q, want %q", order, want)
	}

	app := t.TempDir()
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(app, entries); err != nil {
		t.Fatal(err)
	}
	if err := Check(app, comps); err != nil {
		t.Fatalf("Check after Write: %v", err)
	}

	notices := read(t, filepath.Join(app, NoticesFile))
	for _, want := range []string{
		"github.com/restic/restic  v0.19.1",
		"== github.com/restic/restic v0.19.1 (BSD-2-Clause)",
		"== swift-nio 2.104.0 (Apache-2.0 WITH Swift-exception)",
		"-- NOTICE.txt --\n\nSwiftNIO notice\n",
		"== Go standard library go1.26.7 (BSD-3-Clause)",
		"Additional IP Rights Grant",
	} {
		if !strings.Contains(notices, want) {
			t.Errorf("ThirdPartyNotices.txt lacks %q", want)
		}
	}
	if strings.Contains(notices, "package main") {
		t.Error("a source file was copied as a licence")
	}
	if got := read(t, filepath.Join(app, LicensesDir, "go", "github.com/restic/restic@v0.19.1", "LICENSE")); got != bsd2Text {
		t.Errorf("restic's LICENSE in the app: %q", got)
	}

	// A component the notices don't cover fails the check, by name.
	extra := Component{KindGo, "github.com/pkg/sftp", "v1.13.10"}
	if err := Check(app, append(comps, extra)); err == nil || !strings.Contains(err.Error(), "github.com/pkg/sftp v1.13.10") {
		t.Errorf("Check with an uncovered module: %v", err)
	}
	// So does a component whose licence folder was emptied.
	if err := os.Remove(filepath.Join(app, LicensesDir, "swift", "swiftterm@1.20.0", "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err := Check(app, comps); err == nil || !strings.Contains(err.Error(), "swiftterm 1.20.0") {
		t.Errorf("Check with swiftterm's licence removed: %v", err)
	}
}

func TestCheckNeedsTheNoticesFile(t *testing.T) {
	if err := Check(t.TempDir(), nil); err == nil {
		t.Fatal("Check passed an app without ThirdPartyNotices.txt")
	}
}

func TestCollectReportsEveryProblem(t *testing.T) {
	w := newWorld(t)
	noFile := w.add(Component{KindGo, "example.com/nofile", "v1.0.0"}, map[string]string{"README.md": "hi"})
	onlyNotice := w.add(Component{KindGo, "example.com/onlynotice", "v1.0.0"}, map[string]string{"NOTICE": "notice only"})
	unknown := w.add(Component{KindGo, "example.com/unknown", "v1.0.0"}, map[string]string{"LICENSE": "Do what you like."})
	missing := Component{KindSwift, "nowhere", "1.0.0"}
	_, err := Collect([]Component{noFile, onlyNotice, unknown, missing}, w.source, emptyAllowlist(t))
	if err == nil {
		t.Fatal("Collect passed components without a recognised licence")
	}
	for _, want := range []string{
		"example.com/nofile v1.0.0: no licence file",
		"example.com/onlynotice v1.0.0: no licence file",
		"example.com/unknown v1.0.0: licence not recognised",
		"nowhere 1.0.0: no source",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestAllowlist(t *testing.T) {
	w := newWorld(t)
	unknown := w.add(Component{KindGo, "example.com/unknown", "v1.0.0"}, map[string]string{"LICENSE": "Do what you like, says the author."})
	bare := w.add(Component{KindGo, "example.com/bare", "v2.0.0"}, map[string]string{})

	dir := t.TempDir()
	write(t, filepath.Join(dir, "texts", "bare.txt"), mitText)
	list := filepath.Join(dir, "allowlist.json")
	write(t, list, `[
	  {"kind":"go","name":"example.com/unknown","version":"v1.0.0","licence":"Unlicense-like","reason":"Read by the reviewer: a permissive grant without a standard name."},
	  {"kind":"go","name":"example.com/bare","version":"v2.0.0","licence":"MIT","texts":["texts/bare.txt"],"reason":"The repository states MIT in its README; text added by hand."}
	]`)
	allow, err := LoadAllowlist(list)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Collect([]Component{unknown, bare}, w.source, allow)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range entries {
		got[e.Name] = e
	}
	if e := got["example.com/unknown"]; e.Licence != "Unlicense-like" || len(e.Files) != 1 {
		t.Errorf("unknown: %+v", e)
	}
	if e := got["example.com/bare"]; e.Licence != "MIT" || len(e.Files) != 1 || string(e.Files[0].Text) != mitText {
		t.Errorf("bare: %+v", e)
	}

	// Another version isn't covered by the entry.
	other := w.add(Component{KindGo, "example.com/unknown", "v1.1.0"}, map[string]string{"LICENSE": "Do what you like, says the author."})
	if _, err := Collect([]Component{other}, w.source, allow); err == nil {
		t.Error("an allowlist entry covered a different version")
	}

	// An entry without a reason is refused.
	write(t, list, `[{"kind":"go","name":"example.com/unknown","version":"v1.0.0","licence":"X","reason":" "}]`)
	if _, err := LoadAllowlist(list); err == nil {
		t.Error("an allowlist entry without a reason was accepted")
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
