// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// seedFixture is a project with a style guide, a folder of notes, and the
// things seeding must never copy, next to an empty home.
func seedFixture(t *testing.T) (project, home, outside string) {
	t.Helper()
	root := t.TempDir()
	project, home, outside = filepath.Join(root, "acme"), filepath.Join(root, "lead"), filepath.Join(root, "secret")
	for _, d := range []string{project + "/docs/notes/deep", project + "/.github", home, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(project+"/docs/style-guide.md", "# Style\n", 0o644)
	write(project+"/docs/notes/a.md", "a\n", 0o644)
	write(project+"/docs/notes/deep/b.sh", "#!/bin/sh\n", 0o755)
	write(project+"/docs/notes/.hidden", "h\n", 0o644)
	write(project+"/.github/ci.yml", "ci\n", 0o644)
	write(outside+"/key", "the owner's secret\n", 0o644)
	for link, target := range map[string]string{
		project + "/docs/notes/key-link": outside + "/key",
		project + "/docs/notes/dir-link": outside,
		project + "/linked-guide.md":     outside + "/key",
		project + "/outside":             outside,
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	return project, home, outside
}

func runSeed(t *testing.T, project, home string, quota string, paths ...string) (seedResult, error) {
	t.Helper()
	var out bytes.Buffer
	err := TeamIO(append([]string{"seed-home", project, home, quota}, paths...), nil, &out)
	var r seedResult
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &r); jerr != nil {
			t.Fatalf("seed-home printed %q: %v", out.String(), jerr)
		}
	}
	return r, err
}

func readHome(t *testing.T, home, rel string) (string, os.FileMode, bool) {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(home, rel))
	if err != nil {
		return "", 0, false
	}
	b, _ := os.ReadFile(filepath.Join(home, rel)) // #nosec G304 -- test
	return string(b), fi.Mode(), true
}

// ADR 0022 §2: the listed paths land in the home at the same relative
// paths; links, special files and hidden names never cross; a marker
// records it and a later start copies nothing.
func TestSeedHomeCopiesPlainFilesOnce(t *testing.T) {
	project, home, _ := seedFixture(t)
	r, err := runSeed(t, project, home, "1000000", "docs/style-guide.md", "docs/notes", "linked-guide.md", "outside/key", ".github/ci.yml", "missing.md")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(r.Copied)
	if want := []string{"docs/notes/a.md", "docs/notes/deep/b.sh", "docs/style-guide.md"}; !slices.Equal(r.Copied, want) {
		t.Errorf("copied %v, want %v", r.Copied, want)
	}
	if s, mode, ok := readHome(t, home, "docs/style-guide.md"); !ok || s != "# Style\n" || mode.Perm() != 0o600 {
		t.Errorf("style guide: %q %v %v", s, mode, ok)
	}
	if _, mode, ok := readHome(t, home, "docs/notes/deep/b.sh"); !ok || mode.Perm() != 0o700 {
		t.Errorf("an executable keeps its x bit for the agent only: %v %v", mode, ok)
	}
	for _, never := range []string{"docs/notes/key-link", "docs/notes/dir-link", "linked-guide.md", "outside", "outside/key", "docs/notes/.hidden", ".github/ci.yml", "missing.md"} {
		if _, _, ok := readHome(t, home, never); ok {
			t.Errorf("%s was copied", never)
		}
	}
	var skipped []string
	for _, s := range r.Skipped {
		skipped = append(skipped, strings.SplitN(s, ":", 2)[0])
	}
	for _, want := range []string{"docs/notes/key-link", "docs/notes/dir-link", "docs/notes/.hidden", "linked-guide.md", "outside/key", ".github/ci.yml", "missing.md"} {
		if !slices.Contains(skipped, want) {
			t.Errorf("%s isn't reported as skipped: %v", want, r.Skipped)
		}
	}
	if s, _, ok := readHome(t, home, seedMarker); !ok || !strings.Contains(s, "docs/style-guide.md") {
		t.Errorf("marker: %q %v", s, ok)
	}

	// Later changes in the project aren't copied again.
	if err := os.WriteFile(project+"/docs/new.md", []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = runSeed(t, project, home, "1000000", "docs")
	if err != nil || !r.Already || len(r.Copied) != 0 {
		t.Fatalf("second seeding: %+v, %v", r, err)
	}
	if _, _, ok := readHome(t, home, "docs/new.md"); ok {
		t.Error("a second start copied again")
	}
}

// New files only: what the agent already has is kept, and a link it made
// in its home is never written through.
func TestSeedHomeNeverReplaces(t *testing.T) {
	project, home, outside := seedFixture(t)
	if err := os.MkdirAll(home+"/docs", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home+"/docs/style-guide.md", []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, home+"/docs/notes"); err != nil {
		t.Fatal(err)
	}
	r, err := runSeed(t, project, home, "1000000", "docs/style-guide.md", "docs/notes")
	if err != nil {
		t.Fatal(err)
	}
	if s, _, _ := readHome(t, home, "docs/style-guide.md"); s != "mine\n" {
		t.Errorf("the agent's own file became %q", s)
	}
	if b, _ := os.ReadFile(outside + "/key"); string(b) != "the owner's secret\n" {
		t.Error("seeding wrote through the agent's link")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 {
		t.Errorf("seeding wrote through the agent's link: %v", ents)
	}
	if len(r.Copied) != 0 || len(r.Skipped) < 2 {
		t.Errorf("result %+v", r)
	}
}

// Past the caps or the quota, nothing is copied and no marker is left, so
// the next start tries again.
func TestSeedHomeCapsAndQuota(t *testing.T) {
	project, home, _ := seedFixture(t)
	if _, err := runSeed(t, project, home, "5", "docs"); !errors.Is(err, errHomeFull) {
		t.Errorf("over quota: %v, want errHomeFull", err)
	}
	oldB, oldF := maxSeedBytes, maxSeedFiles
	t.Cleanup(func() { maxSeedBytes, maxSeedFiles = oldB, oldF })
	maxSeedBytes = 4
	if _, err := runSeed(t, project, home, "1000000", "docs"); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("over the size cap: %v", err)
	}
	maxSeedBytes, maxSeedFiles = oldB, 2
	if _, err := runSeed(t, project, home, "1000000", "docs"); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("over the file cap: %v", err)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Errorf("a refused seeding left %v", ents)
	}
	// Bad arguments.
	for _, args := range [][]string{{"0", "docs"}, {"x", "docs"}} {
		if _, err := runSeed(t, project, home, args[0], args[1:]...); err == nil {
			t.Errorf("%v: allowed", args)
		}
	}
	// A path that leaves the project is skipped, never followed.
	r, err := runSeed(t, project, home, "1000000", "../secret/key")
	if err != nil || len(r.Copied) != 0 || len(r.Skipped) != 1 {
		t.Errorf("a path out of the project: %+v, %v", r, err)
	}
}
