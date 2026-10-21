// SPDX-License-Identifier: Apache-2.0

// Package githubrepo is New workspace's "From GitHub" (ADR 0025 §2): which
// repository addresses Portenv clones (https://github.com/OWNER/REPO and
// nothing else), and the repository's size from GitHub's API, for the size
// check before any byte arrives.
package githubrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Repo is one GitHub repository.
type Repo struct {
	Owner, Name string
}

// URL is its https address, the one cloned and kept as the remote.
func (r Repo) URL() string { return "https://github.com/" + r.Owner + "/" + r.Name }

// String is "github.com/owner/name", for plain lines.
func (r Repo) String() string { return "github.com/" + r.Owner + "/" + r.Name }

var repoRE = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))/([A-Za-z0-9._-]{1,100})/?$`)

// ErrNotAGitHubURL is Parse's refusal.
var ErrNotAGitHubURL = errors.New("use a GitHub repository's https address, like https://github.com/acme/acme-api")

// Parse takes https://github.com/OWNER/REPO, with or without .git or a
// trailing slash, and nothing else: no user or password, port, query,
// other host, or other protocol.
func Parse(s string) (Repo, error) {
	m := repoRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Repo{}, ErrNotAGitHubURL
	}
	name := strings.TrimSuffix(m[2], ".git")
	if name == "" || name == "." || name == ".." {
		return Repo{}, ErrNotAGitHubURL
	}
	return Repo{Owner: m[1], Name: name}, nil
}

// API is GitHub's REST API.
const API = "https://api.github.com"

// Size is the repository's packed size in bytes, from GitHub's API (which
// gives kilobytes). token is empty for a public repository.
func Size(ctx context.Context, c *http.Client, base string, r Repo, token string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+r.Owner+"/"+r.Name, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GitHub answered %d for %s", resp.StatusCode, r)
	}
	var v struct {
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return 0, err
	}
	if v.Size < 0 || v.Size > 1<<40 {
		return 0, fmt.Errorf("GitHub gave %s a size of %d KB", r, v.Size)
	}
	return v.Size * 1024, nil
}
