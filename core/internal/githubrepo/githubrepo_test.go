// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTakesOnlyAGitHubHTTPSRepository(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/acme-api":      "https://github.com/acme/acme-api",
		"https://github.com/acme/acme-api.git":  "https://github.com/acme/acme-api",
		"https://github.com/acme/acme-api/":     "https://github.com/acme/acme-api",
		" https://github.com/Acme-1/api.v2 ":    "https://github.com/Acme-1/api.v2",
		"https://github.com/acme/my_repo-2.git": "https://github.com/acme/my_repo-2",
	} {
		r, err := Parse(in)
		if err != nil || r.URL() != want {
			t.Errorf("%q: %v %q", in, err, r.URL())
		}
	}
	for _, bad := range []string{
		"", "http://github.com/acme/api", "git@github.com:acme/api.git", "ssh://git@github.com/acme/api",
		"file:///etc", "https://gitlab.com/acme/api", "https://github.com/acme", "https://github.com/acme/api/tree/main",
		"https://user:pw@github.com/acme/api", "https://x@github.com/acme/api", "https://github.com:8443/acme/api",
		"https://github.com/acme/..", "https://github.com/-acme/api", "https://github.com/acme/api?x=1",
		"https://github.com/acme/api#frag", "https://github.com/acme/a\nb", "https://github.com.evil.com/acme/api",
		"https://github.com/acme/" + string(make([]byte, 101)),
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q: not refused", bad)
		}
	}
}

func TestSizeAsksGitHubsAPI(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/repos/acme/api" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"size": 2048, "default_branch": "main", "private": false}`))
	}))
	defer srv.Close()
	r, _ := Parse("https://github.com/acme/api")
	n, err := Size(context.Background(), srv.Client(), srv.URL, r, "")
	if err != nil || n != 2048*1024 || auth != "" {
		t.Fatalf("%d %v %q", n, err, auth)
	}
	if _, err := Size(context.Background(), srv.Client(), srv.URL, Repo{Owner: "acme", Name: "gone"}, ""); err == nil {
		t.Fatal("a missing repository has no size")
	}
}
