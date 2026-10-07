// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"slices"
	"strings"
	"testing"
)

func TestParsePackages(t *testing.T) {
	in := `# system packages for this box
redis-tools
libpq-dev   # for the pg gem

g++
python3.12-venv
nodejs:amd64
postgresql-client=16+257build1
redis-tools
`
	got, err := ParsePackages(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"redis-tools", "libpq-dev", "g++", "python3.12-venv", "nodejs:amd64", "postgresql-client=16+257build1"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParsePackagesRejects(t *testing.T) {
	for _, line := range []string{
		"-o APT::Get::AllowUnauthenticated=true",
		"--allow-unauthenticated",
		"redis tools",
		"Redis",
		"a",
		"pkg;rm -rf /",
		"pkg$(id)",
		"../pkg",
	} {
		_, err := ParsePackages(strings.NewReader("ok-pkg\n" + line + "\n"))
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: got error %v, want a line 2 error", line, err)
		}
	}
}

func TestPackageName(t *testing.T) {
	for in, want := range map[string]string{
		"jq":                  "jq",
		"nodejs:amd64":        "nodejs",
		"postgresql-client=1": "postgresql-client",
		"libfoo:arm64=1.2-3":  "libfoo",
	} {
		if got := packageName(in); got != want {
			t.Errorf("packageName(%q) = %q, want %q", in, got, want)
		}
	}
}
