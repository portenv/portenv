// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fetch through portenvd (R-0392): the remote's refs with its default
// branch, and a bundle of only what the workspace lacks.

func TestListForFetchHasTheDefaultBranch(t *testing.T) {
	r := newPushRig(t, "tok")
	refs, head, err := ListForFetch(context.Background(), http.DefaultClient, r.url, "tok")
	if err != nil || head != "refs/heads/main" || len(refs) != 1 || refs[0].SHA != r.remoteRef("main") {
		t.Fatalf("%v %q %v", refs, head, err)
	}
	if _, _, err := ListForFetch(context.Background(), http.DefaultClient, r.url, ""); !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("no sign-in: %v", err)
	}
}

// The remote moved on; the workspace has the old commit. The bundle holds
// only the new objects, names the old commit as its prerequisite, and
// unbundles in the workspace.
func TestFetchRefsIsOnlyWhatTheWorkspaceLacks(t *testing.T) {
	r := newPushRig(t, "tok")
	old := r.remoteRef("main")
	// Someone pushes to GitHub: a new commit on main, and a tag.
	other := filepath.Join(t.TempDir(), "other")
	r.run("", "clone", "-q", r.remote, other)
	n := r.commit(other, "two")
	r.run(other, "tag", "v2")
	r.run(other, "push", "-q", "origin", "main", "v2")

	var b bytes.Buffer
	err := FetchRefs(context.Background(), http.DefaultClient, r.url, "tok",
		[]Ref{{Name: "refs/heads/main", SHA: n}, {Name: "refs/tags/v2", SHA: n}}, []string{old}, &b, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := bytes.Cut(b.Bytes(), []byte("\n\n"))
	h := string(header)
	if !strings.HasPrefix(h, "# v2 git bundle\n-"+old) || !strings.Contains(h, n+" refs/heads/main") || !strings.Contains(h, n+" refs/tags/v2") {
		t.Fatalf("header:\n%s", h)
	}
	file := filepath.Join(t.TempDir(), "f.bundle")
	if err := os.WriteFile(file, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run(r.local, "bundle", "verify", "-q", file)
	r.run(r.local, "bundle", "unbundle", file)
	r.run(r.local, "cat-file", "-e", n)

	// Without the haves, it's the whole history: bigger.
	var whole bytes.Buffer
	if err := FetchRefs(context.Background(), http.DefaultClient, r.url, "tok", []Ref{{Name: "refs/heads/main", SHA: n}}, nil, &whole, 1<<20); err != nil {
		t.Fatal(err)
	}
	if whole.Len() <= b.Len()-len(header)+40 {
		t.Fatalf("the thin bundle (%d) isn't smaller than the whole one (%d)", b.Len(), whole.Len())
	}
}

func TestFetchRefsRefuses(t *testing.T) {
	r := newPushRig(t, "tok")
	main := r.remoteRef("main")
	for name, tc := range map[string]struct {
		wants []Ref
		haves []string
	}{
		"nothing":            {nil, nil},
		"a pull ref":         {[]Ref{{Name: "refs/pull/1/head", SHA: main}}, nil},
		"not on the remote":  {[]Ref{{Name: "refs/heads/nope", SHA: main}}, nil},
		"another object":     {[]Ref{{Name: "refs/heads/main", SHA: strings.Repeat("1", 40)}}, nil},
		"a bad have":         {[]Ref{{Name: "refs/heads/main", SHA: main}}, []string{"zz"}},
		"a ref with ..":      {[]Ref{{Name: "refs/heads/../main", SHA: main}}, nil},
		"too many haves":     {[]Ref{{Name: "refs/heads/main", SHA: main}}, bytes2haves(300)},
	} {
		var b bytes.Buffer
		if err := FetchRefs(context.Background(), http.DefaultClient, r.url, "tok", tc.wants, tc.haves, &b, 1<<20); err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	var b bytes.Buffer
	if err := FetchRefs(context.Background(), http.DefaultClient, r.url, "tok", []Ref{{Name: "refs/heads/main", SHA: main}}, nil, &b, 10); !errors.Is(err, ErrTooBig) {
		t.Fatalf("the cap: %v", err)
	}
}

func bytes2haves(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, strings.Repeat("a", 39)+string(rune('0'+i%10)))
	}
	return out
}
