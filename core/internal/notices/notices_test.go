// SPDX-License-Identifier: Apache-2.0

package notices

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

const (
	apacheText = "Apache License\n Version 2.0, January 2004\n http://www.apache.org/licenses/"
	mitText    = "MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy"
	bsd2Text   = "Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:\n1. ...\n2. ..."
	bsd3Text   = bsd2Text + "\n3. Neither the name of the copyright holder nor the names of its contributors may be used"
	mplText    = "Mozilla Public License Version 2.0\n==================================\n1.13. \"Secondary License\"\n    means either the GNU General Public License, Version 2.0, the GNU Lesser\n    General Public License, Version 2.1, the GNU Affero General Public\n    License, Version 3.0, or any later versions of those licenses."
	iscText    = "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted"
	gplText    = "GNU GENERAL PUBLIC LICENSE\nVersion 3\n\nThe GNU General Public License is a free, copyleft license"
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
		{"openssl and ssleay", "OpenSSL License\n Copyright (c) 1998-2011 The OpenSSL Project.\n Redistribution and use in source and binary forms ... endorse or promote products\n Original SSLeay License\n" + iscText, []string{"ISC", "OpenSSL"}},
		{"isc in a code comment", "/* Copyright 2015 The BoringSSL Authors\n *\n * Permission to use, copy, modify, and/or distribute this software for any\n * purpose with or without fee is hereby granted */", []string{"ISC"}},
		{"unlicense", "This is free and unencumbered software released into the public domain.", []string{"Unlicense"}},
		{"unknown", "All rights reserved. Do what you like.", nil},
	} {
		if got := Identify([]byte(tc.text)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Identify = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Copyleft terms in any spelling (R-0116), and MPL-2.0's own definition of
// "Secondary License" isn't one.
func TestCopyleft(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"gpl-2.0 inside an mit file", mitText + "\nPortions are GPL-2.0.", true},
		{"spdx or-later", "SPDX-License-Identifier: GPL-2.0-or-later", true},
		{"gplv3", "This program is released under GPLv3.", true},
		{"capitals", "GNU GENERAL PUBLIC LICENSE\nVersion 2, June 1991", true},
		{"lgpl", "Licensed under the LGPL-2.1.", true},
		{"agpl", "AGPLv3 only", true},
		{"debian copyright", "Files: *\nCopyright: 2020 Someone\nLicense: GPL-2+", true},
		{"gnu lesser", "the GNU Lesser General Public Licence", true},
		{"affero", "GNU Affero", true},
		{"cc by-sa", "Creative Commons BY-SA 4.0", true},
		{"share-alike", "Attribution-Share-Alike", true},
		{"mpl's secondary licenses", mplText, false},
		{"mit", mitText, false},
		{"apache", apacheText, false},
		{"ssleay's gnu public licence", "this code cannot simply be copied and put under another distribution licence [including the GNU Public Licence.]", false},
		{"words that contain gpl", "egplant standby-safe", false},
	} {
		if got := len(Copyleft([]byte(tc.text))) > 0; got != tc.want {
			t.Errorf("%s: Copyleft = %q, want copyleft %v", tc.name, Copyleft([]byte(tc.text)), tc.want)
		}
	}

	// A Debian-style COPYRIGHT file (never identified as a licence) still
	// fails the build.
	w := newWorld(t)
	deb := w.add(Component{KindGo, "example.com/deb", "v1.0.0"}, map[string]string{"LICENSE": mitText, "COPYRIGHT": "Files: x/*\nLicense: GPL-2+"})
	if _, err := Collect([]Component{deb}, w.source, origin, emptyAllowlist(t)); err == nil || !strings.Contains(err.Error(), "copyleft terms in COPYRIGHT (gpl-2)") {
		t.Errorf("a Debian COPYRIGHT with GPL-2+: %v", err)
	}
}

func TestLicenceFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"LICENSE", "LICENSE.txt", "LICENCE-MIT", "LICENSE.APACHE", "LICENSE-2.0.txt", "LICENSES.txt",
		"UNLICENSE", "COPYING", "COPYRIGHT", "NOTICE.md", "PATENTS", "LICENSE_THIRD_PARTY", "THIRD_PARTY_NOTICES", "third-party-notices.txt",
		"LICENSES/MIT.txt", "LICENSES/Apache-2.0.txt", "b2/licenses.csv",
		"vendor/boringssl/LICENSE", "Sources/CNIOBoringSSL/hash.txt", "third_party/zlib/COPYING.md",
		"license.go", "LICENSE_test.go", "README.md", "licensing.md", "NOTICES.json",
		".git/LICENSE", "testdata/LICENSE", "Tests/Fixtures/LICENSE",
	} {
		write(t, filepath.Join(dir, n), "x")
	}
	got, err := LicenceFiles(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"COPYING", "COPYRIGHT", "LICENCE-MIT", "LICENSE", "LICENSE-2.0.txt", "LICENSE.APACHE", "LICENSE.txt",
		"LICENSES.txt", "LICENSES/Apache-2.0.txt", "LICENSES/MIT.txt", "LICENSE_THIRD_PARTY", "NOTICE.md", "PATENTS", "THIRD_PARTY_NOTICES", "UNLICENSE",
		"third-party-notices.txt", "third_party/zlib/COPYING.md", "vendor/boringssl/LICENSE",
	}
	if !slices.Equal(got, want) {
		t.Errorf("LicenceFiles = %q,\nwant %q", got, want)
	}

	// Without recursion: the top folder and its REUSE folder only.
	top, err := LicenceFiles(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(top, "vendor/boringssl/LICENSE") || !slices.Contains(top, "LICENSES/MIT.txt") || !slices.Contains(top, "LICENSE") {
		t.Errorf("LicenceFiles, top only = %q", top)
	}
}

func TestComponentsFrom(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.26.4",
		Main:      debug.Module{Path: "github.com/restic/restic", Version: "v0.19.1"},
		Deps: []*debug.Module{
			{Path: "github.com/portenv/portenv/proto/gen/go", Version: "v0.0.0"},
			{Path: "github.com/portenv/portenvish", Version: "v1.0.0"},
			{Path: "golang.org/x/sys", Version: "v0.30.0"},
			{Path: "github.com/pkg/sftp", Version: "v1.13.9", Replace: &debug.Module{Path: "github.com/fork/sftp", Version: "v1.13.10-fork"}},
			{Path: "github.com/Azure/go-x", Version: "v0.0.0-20251001120000-0123456789ab"},
		},
	}
	got, err := componentsFrom(bi)
	if err != nil {
		t.Fatal(err)
	}
	want := []Component{
		{KindGoStd, "Go standard library", "go1.26.4"},
		{KindGo, "github.com/restic/restic", "v0.19.1"},
		{KindGo, "github.com/portenv/portenvish", "v1.0.0"},
		{KindGo, "golang.org/x/sys", "v0.30.0"},
		{KindGo, "github.com/fork/sftp", "v1.13.10-fork"},
		{KindGo, "github.com/Azure/go-x", "v0.0.0-20251001120000-0123456789ab"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("componentsFrom =\n%v\nwant\n%v", got, want)
	}

	// A local replacement and a main module built from a working tree have no
	// version to pin.
	local := &debug.BuildInfo{GoVersion: "go1.26.7", Main: debug.Module{Path: "github.com/portenv/portenv/core"},
		Deps: []*debug.Module{{Path: "github.com/pkg/sftp", Version: "v1.13.9", Replace: &debug.Module{Path: "../sftp"}}}}
	if _, err := componentsFrom(local); err == nil || !strings.Contains(err.Error(), "../sftp has no version") {
		t.Errorf("a local replacement: %v", err)
	}
	devel := &debug.BuildInfo{GoVersion: "go1.26.7", Main: debug.Module{Path: "github.com/restic/restic", Version: "(devel)"}}
	if _, err := componentsFrom(devel); err == nil || !strings.Contains(err.Error(), "restic has no version") {
		t.Errorf("a (devel) main module: %v", err)
	}
}

func TestOrigins(t *testing.T) {
	for _, tc := range []struct {
		c    Component
		want string
	}{
		{Component{KindGo, "github.com/hashicorp/golang-lru/v2", "v2.0.7"}, "https://proxy.golang.org/github.com/hashicorp/golang-lru/v2/@v/v2.0.7.zip"},
		{Component{KindGo, "github.com/Azure/go-x", "v0.0.0-20251001120000-0123456789ab"}, "https://proxy.golang.org/github.com/!azure/go-x/@v/v0.0.0-20251001120000-0123456789ab.zip"},
		{Component{KindGoStd, "Go standard library", "go1.26.7"}, "https://go.dev/dl/go1.26.7.src.tar.gz"},
		{Component{KindSwift, "swiftterm", "1.2.0"}, ""},
	} {
		if got := GoOrigin(tc.c); got != tc.want {
			t.Errorf("GoOrigin(%v) = %q, want %q", tc.c, got, tc.want)
		}
	}
	p := SwiftPin{Identity: "swift-nio-ssl", Location: "https://github.com/apple/swift-nio-ssl.git", Version: "2.30.0", Revision: "abc123"}
	if got := p.Origin(); got != "https://github.com/apple/swift-nio-ssl.git at abc123" {
		t.Errorf("SwiftPin.Origin = %q", got)
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

// origin is GoOrigin, with a fixed repository for Swift components.
func origin(c Component) string {
	if c.Kind == KindSwift {
		return "https://example.com/" + c.Name + ".git at 0123abcd"
	}
	return GoOrigin(c)
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
	lru := w.add(Component{KindGo, "github.com/hashicorp/golang-lru/v2", "v2.0.7"}, map[string]string{"LICENSE": mplText})
	// A licence name with brackets of its own.
	digest := w.add(Component{KindGo, "github.com/opencontainers/go-digest", "v1.0.0"}, map[string]string{"LICENSE": apacheText})
	dual := w.add(Component{KindGo, "example.com/dual", "v1.0.0"}, map[string]string{"LICENSE-MIT": mitText, "LICENSE-APACHE": apacheText})
	ssl := w.add(Component{KindSwift, "swift-nio-ssl", "2.30.0"}, map[string]string{
		"LICENSE.txt": apacheText, "Sources/CNIOBoringSSL/LICENSE": iscText, "Tests/LICENSE": "test fixture",
	})
	comps := []Component{term, restic, nio, std, lru, dual, ssl, digest}

	allowDir := t.TempDir()
	write(t, filepath.Join(allowDir, "allowlist.json"), `[{"kind":"go","name":"github.com/opencontainers/go-digest","version":"v1.0.0","licence":"Apache-2.0 (documentation: CC-BY-SA-4.0)","reason":"r"}]`)
	allow, err := LoadAllowlist(filepath.Join(allowDir, "allowlist.json"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Collect(comps, w.source, origin, allow)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range entries {
		order = append(order, e.Name)
	}
	if want := []string{"Go standard library", "example.com/dual", "github.com/hashicorp/golang-lru/v2", "github.com/opencontainers/go-digest", "github.com/restic/restic", "swift-nio", "swift-nio-ssl", "swiftterm"}; !slices.Equal(order, want) {
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
		"== github.com/restic/restic v0.19.1 (BSD-2-Clause)",
		"== swift-nio 2.104.0 (Apache-2.0 WITH Swift-exception)",
		"-- NOTICE.txt --\n\nSwiftNIO notice\n",
		"== Go standard library go1.26.7 (BSD-3-Clause)\nSource: https://go.dev/dl/go1.26.7.src.tar.gz\n",
		"Additional IP Rights Grant",
		// MPL-2.0 needs a pointer to the source (R-0110).
		"== github.com/hashicorp/golang-lru/v2 v2.0.7 (MPL-2.0)\nSource: https://proxy.golang.org/github.com/hashicorp/golang-lru/v2/@v/v2.0.7.zip\n",
		"== swift-nio 2.104.0 (Apache-2.0 WITH Swift-exception)\nSource: https://example.com/swift-nio.git at 0123abcd\n",
		"== example.com/dual v1.0.0 (Apache-2.0, MIT)",
		// Vendored code's licence, from its subfolder.
		"== swift-nio-ssl 2.30.0 (Apache-2.0, ISC)",
		"-- Sources/CNIOBoringSSL/LICENSE --\n\n" + iscText,
	} {
		if !strings.Contains(notices, want) {
			t.Errorf("ThirdPartyNotices.txt lacks %q", want)
		}
	}
	if !regexp.MustCompile(`(?m)^github\.com/restic/restic v0\.19\.1 · BSD-2-Clause$`).MatchString(notices) {
		t.Errorf("the table lacks restic's row:\n%s", notices)
	}
	if strings.Contains(notices, "Component") {
		t.Error("the list still has a column header")
	}
	if strings.Contains(notices, "package main") {
		t.Error("a source file was copied as a licence")
	}
	if strings.Contains(notices, "test fixture") {
		t.Error("a licence under Tests/ was copied")
	}
	if strings.Count(notices, "\nSource: ") != len(comps) {
		t.Errorf("not every section has a Source line:\n%s", notices)
	}
	if got := read(t, filepath.Join(app, LicensesDir, "swift", "swift-nio-ssl@2.30.0", "Sources", "CNIOBoringSSL", "LICENSE")); got != iscText {
		t.Errorf("BoringSSL's licence in the app: %q", got)
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
	// And one left with only a NOTICE.
	if err := os.Remove(filepath.Join(app, LicensesDir, "swift", "swift-nio@2.104.0", "LICENSE.txt")); err != nil {
		t.Fatal(err)
	}
	if err := Check(app, comps); err == nil || !strings.Contains(err.Error(), "swift-nio 2.104.0") {
		t.Errorf("Check with only swift-nio's NOTICE left: %v", err)
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
	// A recognised LICENSE doesn't hide an unrecognised file beside it.
	hidden := w.add(Component{KindGo, "example.com/hidden", "v1.0.0"}, map[string]string{"LICENSE": mitText, "COPYING": "Do what you like."})
	// Nor GPL terms, beside it, in a vendored folder or in a notice.
	gpl := w.add(Component{KindGo, "example.com/gpl", "v1.0.0"}, map[string]string{"LICENSE": mitText, "COPYING": gplText})
	vendoredGPL := w.add(Component{KindSwift, "vendored", "1.0.0"}, map[string]string{"LICENSE": apacheText, "third_party/x/LICENSE": "GNU Lesser General Public License"})
	agpl := w.add(Component{KindGo, "example.com/agpl", "v1.0.0"}, map[string]string{"LICENSE": mitText, "NOTICE": "Parts are under the GNU Affero General Public License"})
	// MPL-2.0's own mention of the GNU licences isn't copyleft, but the
	// same words anywhere else are.
	mplPlus := w.add(Component{KindGo, "example.com/mplplus", "v1.0.0"}, map[string]string{"LICENSE": mplText + "\nThis file is also under the GNU General Public License."})
	cc := w.add(Component{KindGo, "example.com/cc", "v1.0.0"}, map[string]string{"LICENSE": mitText, "LICENSE-docs": "Creative Commons Attribution-ShareAlike 4.0"})
	missing := Component{KindSwift, "nowhere", "1.0.0"}
	noOrigin := w.add(Component{Kind: "other", Name: "example.com/noorigin", Version: "v1"}, map[string]string{"LICENSE": mitText})
	_, err := Collect([]Component{noFile, onlyNotice, unknown, hidden, gpl, vendoredGPL, agpl, mplPlus, cc, missing, noOrigin}, w.source, func(c Component) string {
		if c.Kind == "other" {
			return ""
		}
		return origin(c)
	}, emptyAllowlist(t))
	if err == nil {
		t.Fatal("Collect passed components without a recognised licence")
	}
	for _, want := range []string{
		"example.com/nofile v1.0.0: no licence file",
		"example.com/onlynotice v1.0.0: no licence file",
		"example.com/unknown v1.0.0: licence not recognised in LICENSE",
		"example.com/hidden v1.0.0: licence not recognised in COPYING",
		"example.com/gpl v1.0.0: copyleft terms in COPYING (general public license)",
		"vendored 1.0.0: copyleft terms in third_party/x/LICENSE (general public license)",
		"example.com/agpl v1.0.0: copyleft terms in NOTICE (affero, general public license)",
		"example.com/cc v1.0.0: copyleft terms in LICENSE-docs (sharealike)",
		"example.com/mplplus v1.0.0: copyleft terms in LICENSE (general public license)",
		"nowhere 1.0.0: no source",
		"example.com/noorigin v1: no source to point to",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// A Swift package that vendors another project's code (hash.txt) without
// its licence fails until the allowlist adds it beside the package's own
// files (R-0110: BoringSSL in swift-nio-ssl).
func TestVendoredCodeNeedsItsLicence(t *testing.T) {
	w := newWorld(t)
	marker := func(rev string) string {
		return "This directory is derived from BoringSSL cloned from https://boringssl.googlesource.com/boringssl at revision " + rev + "\n"
	}
	files := map[string]string{
		"LICENSE.txt": apacheText, "NOTICE.txt": "This product contains code from boringssl.",
		"Sources/CNIOBoringSSL/hash.txt": marker("817ab07e"), "Sources/CNIOBoringSSL/crypto/mem.cc": "/* Licensed under the OpenSSL license */",
		"Sources/NIOSSL/hash.txt": "not vendored",
	}
	ssl := w.add(Component{KindSwift, "swift-nio-ssl", "2.37.5"}, files)
	_, err := Collect([]Component{ssl}, w.source, origin, emptyAllowlist(t))
	if err == nil || !strings.Contains(err.Error(), "swift-nio-ssl 2.37.5: vendored code without its licence in Sources/CNIOBoringSSL") {
		t.Fatalf("Collect without BoringSSL's licence: %v", err)
	}

	// hash.txt is found at any depth.
	deep := w.add(Component{KindSwift, "deep", "1.0.0"}, map[string]string{"LICENSE": mitText, "Sources/A/vendor/zlib/hash.txt": "This directory is derived from zlib cloned from https://example.com/zlib at revision abcdef1"})
	if _, err := Collect([]Component{deep}, w.source, origin, emptyAllowlist(t)); err == nil || !strings.Contains(err.Error(), "vendored code without its licence in Sources/A/vendor/zlib") {
		t.Errorf("a deep hash.txt: %v", err)
	}

	dir := t.TempDir()
	write(t, filepath.Join(dir, "texts", "boringssl-817ab07e-LICENSE"), iscText)
	list := filepath.Join(dir, "allowlist.json")
	entry := func(rev, as string) string {
		return `[{"kind":"swift","name":"swift-nio-ssl","version":"2.37.5",
		  "extra":[{"text":"texts/boringssl-817ab07e-LICENSE","as":"` + as + `","revision":"` + rev + `"}],
		  "reason":"BoringSSL's LICENSE at the vendored revision; the vendoring script leaves it out."}]`
	}
	load := func(text string) *Allowlist {
		t.Helper()
		write(t, list, text)
		a, err := LoadAllowlist(list)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	allow := load(entry("817ab07e", "Sources/CNIOBoringSSL/LICENSE"))
	entries, err := Collect([]Component{ssl}, w.source, origin, allow)
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e.Licence != "Apache-2.0, ISC" {
		t.Errorf("licence %q: the extra text is identified with the package's own", e.Licence)
	}
	var names []string
	for _, f := range e.Files {
		names = append(names, f.Name)
	}
	if want := []string{"LICENSE.txt", "NOTICE.txt", "Sources/CNIOBoringSSL/LICENSE"}; !slices.Equal(names, want) {
		t.Errorf("files %q, want %q", names, want)
	}

	// An update that vendors another revision fails until the entry names it.
	write(t, filepath.Join(w.dirs[ssl], "Sources", "CNIOBoringSSL", "hash.txt"), marker("0226f304"))
	if _, err := Collect([]Component{ssl}, w.source, origin, allow); err == nil || !strings.Contains(err.Error(), "is for revision 817ab07e, but Sources/CNIOBoringSSL/hash.txt vendors 0226f304") {
		t.Errorf("a new vendored revision: %v", err)
	}
	write(t, filepath.Join(w.dirs[ssl], "Sources", "CNIOBoringSSL", "hash.txt"), marker("817ab07e"))

	// An extra text is checked like the package's own.
	write(t, filepath.Join(dir, "texts", "boringssl-817ab07e-LICENSE"), gplText)
	if _, err := Collect([]Component{ssl}, w.source, origin, allow); err == nil || !strings.Contains(err.Error(), "copyleft terms in Sources/CNIOBoringSSL/LICENSE") {
		t.Errorf("a GPL extra text: %v", err)
	}
	write(t, filepath.Join(dir, "texts", "boringssl-817ab07e-LICENSE"), iscText)
	// It can't replace one of the package's files, nor sit outside a vendored folder.
	for as, want := range map[string]string{"LICENSE.txt": "already in the source", "Sources/NIOSSL/LICENSE": "isn't in a vendored folder"} {
		if _, err := Collect([]Component{ssl}, w.source, origin, load(entry("817ab07e", as))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("an extra at %s: %v", as, err)
		}
	}
	// And it needs its revision.
	for _, rev := range []string{"", "HEAD", "817ab07e; rm"} {
		write(t, list, entry(rev, "Sources/CNIOBoringSSL/LICENSE"))
		if _, err := LoadAllowlist(list); err == nil {
			t.Errorf("an extra with revision %q was accepted", rev)
		}
	}
}

func TestAllowlist(t *testing.T) {
	w := newWorld(t)
	unknown := w.add(Component{KindGo, "example.com/unknown", "v1.0.0"}, map[string]string{"LICENSE": "Do what you like, says the author."})
	bare := w.add(Component{KindGo, "example.com/bare", "v2.0.0"}, map[string]string{})
	// go-digest's case: one file the reviewer read is ignored, the rest is
	// still checked.
	docs := w.add(Component{KindGo, "example.com/docs", "v1.0.0"}, map[string]string{"LICENSE": apacheText, "LICENSE.docs": "Creative Commons Attribution-ShareAlike 4.0"})
	docsGPL := w.add(Component{KindGo, "example.com/docs", "v1.1.0"}, map[string]string{"LICENSE": apacheText, "LICENSE.docs": "CC BY-SA", "NOTICE": "Parts are GPL-2.0."})
	stale := w.add(Component{KindGo, "example.com/docs", "v1.2.0"}, map[string]string{"LICENSE": apacheText})
	// A licence exception doesn't switch off the copyleft check.
	lgpl := w.add(Component{KindGo, "example.com/lgpl", "v1.0.0"}, map[string]string{"LICENSE": "A grant without a name.", "COPYING.LESSER": "GNU Lesser General Public License"})

	dir := t.TempDir()
	write(t, filepath.Join(dir, "texts", "bare.txt"), mitText)
	list := filepath.Join(dir, "allowlist.json")
	write(t, list, `[
	  {"kind":"go","name":"example.com/unknown","version":"v1.0.0","licence":"Unlicense-like","reason":"Read by the reviewer: a permissive grant without a standard name."},
	  {"kind":"go","name":"example.com/bare","version":"v2.0.0","licence":"MIT","texts":["texts/bare.txt"],"reason":"The repository states MIT in its README; text added by hand."},
	  {"kind":"go","name":"example.com/docs","version":"v1.0.0","ignore":["LICENSE.docs"],"reason":"Documentation only, not compiled in."},
	  {"kind":"go","name":"example.com/docs","version":"v1.1.0","ignore":["LICENSE.docs"],"reason":"Documentation only, not compiled in."},
	  {"kind":"go","name":"example.com/docs","version":"v1.2.0","ignore":["LICENSE.docs"],"reason":"Documentation only, not compiled in."},
	  {"kind":"go","name":"example.com/lgpl","version":"v1.0.0","licence":"MIT","reason":"r"}
	]`)
	allow, err := LoadAllowlist(list)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Collect([]Component{unknown, bare, docs}, w.source, origin, allow)
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
	if e := got["example.com/docs"]; e.Licence != "Apache-2.0" || len(e.Files) != 2 {
		t.Errorf("an ignored file still ships, and the rest is identified: %+v", e)
	}
	for c, want := range map[Component]string{
		docsGPL: "copyleft terms in NOTICE (gpl-2)",
		stale:   "ignores LICENSE.docs, which isn't among its licence files",
		lgpl:    "copyleft terms in COPYING.LESSER",
	} {
		if _, err := Collect([]Component{c}, w.source, origin, allow); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s %s: %v, want %q", c.Name, c.Version, err, want)
		}
	}

	// Another version isn't covered by the entry.
	other := w.add(Component{KindGo, "example.com/unknown", "v1.1.0"}, map[string]string{"LICENSE": "Do what you like, says the author."})
	if _, err := Collect([]Component{other}, w.source, origin, allow); err == nil {
		t.Error("an allowlist entry covered a different version")
	}

	// Malformed entries are refused.
	for name, entry := range map[string]string{
		"no reason":         `{"kind":"go","name":"a","version":"v1","licence":"X","reason":" "}`,
		"unknown kind":      `{"kind":"rust","name":"a","version":"v1","licence":"X","reason":"r"}`,
		"no kind":           `{"name":"a","version":"v1","licence":"X","reason":"r"}`,
		"a text outside":    `{"kind":"go","name":"a","version":"v1","licence":"X","texts":["../secret.txt"],"reason":"r"}`,
		"a text inside..":   `{"kind":"go","name":"a","version":"v1","licence":"X","texts":["texts/../../x"],"reason":"r"}`,
		"absolute text":     `{"kind":"go","name":"a","version":"v1","licence":"X","texts":["/etc/hosts"],"reason":"r"}`,
		"duplicate texts":   `{"kind":"go","name":"a","version":"v1","licence":"X","texts":["a/LICENSE","b/LICENSE"],"reason":"r"}`,
		"no licence":        `{"kind":"go","name":"a","version":"v1","reason":"r"}`,
		"texts, no licence": `{"kind":"go","name":"a","version":"v1","texts":["a"],"extra":[{"text":"a","as":"b"}],"reason":"r"}`,
		"extra outside":     `{"kind":"go","name":"a","version":"v1","extra":[{"text":"../x","as":"b"}],"reason":"r"}`,
		"extra as ..":       `{"kind":"go","name":"a","version":"v1","extra":[{"text":"x","as":"../b"}],"reason":"r"}`,
		"extra no as":       `{"kind":"go","name":"a","version":"v1","extra":[{"text":"x"}],"reason":"r"}`,
		"extra twice":       `{"kind":"go","name":"a","version":"v1","extra":[{"text":"x","as":"b/L","revision":"abcdef1"},{"text":"y","as":"b//L","revision":"abcdef1"}],"reason":"r"}`,
		"ignore outside":    `{"kind":"go","name":"a","version":"v1","ignore":["../LICENSE"],"reason":"r"}`,
	} {
		write(t, list, "["+entry+"]")
		if _, err := LoadAllowlist(list); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dup := `{"kind":"go","name":"a","version":"v1","licence":"X","reason":"r"}`
	write(t, list, "["+dup+","+dup+"]")
	if _, err := LoadAllowlist(list); err == nil {
		t.Error("an entry listed twice was accepted")
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
