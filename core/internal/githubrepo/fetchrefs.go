// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Fetch through portenvd (R-0392): git fetch, pull and ls-remote in the
// workspace go through git-remote-portenv; portenvd reads the remote here
// with the owner's sign-in (or none, for a public repository) and hands in
// a bundle of only what the workspace lacks: GitHub's pack for the refs
// git wants, negotiated against the commits the workspace has, which the
// bundle names as its prerequisites. The pack is GitHub's, unparsed; the
// workspace's git checks every object as it unbundles.

// maxFetchHaves caps the commits a fetch says the workspace has.
const maxFetchHaves = 256

// MaxFetchBytes caps one fetch's pack.
const MaxFetchBytes = 2 << 30

func uploadSession(c *http.Client, url, token string) (transport.UploadPackSession, error) {
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return nil, err
	}
	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}
	s, err := githttp.NewClient(c).NewUploadPackSession(ep, auth)
	if err != nil {
		return nil, plain(err)
	}
	return s, nil
}

// ListForFetch is the remote's branches and tags, and its default branch,
// as upload-pack advertises them (git's list, for fetch and ls-remote).
func ListForFetch(ctx context.Context, c *http.Client, url, token string) ([]Ref, string, error) {
	s, err := uploadSession(c, url, token)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = s.Close() }()
	adv, err := s.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", plain(err)
	}
	var refs []Ref
	for name, h := range adv.References {
		if CheckRefName(name) && !h.IsZero() {
			refs = append(refs, Ref{Name: name, SHA: h.String()})
		}
	}
	head := ""
	for _, v := range adv.Capabilities.Get(capability.SymRef) {
		if h, target, ok := strings.Cut(v, ":"); ok && h == "HEAD" && CheckRefName(target) && strings.HasPrefix(target, "refs/heads/") {
			head = target
		}
	}
	return refs, head, nil
}

// FetchRefs writes a bundle of the wanted refs to w, without what the
// haves already hold, at most limit bytes of pack. Every want must be a
// branch or tag the remote has at that object.
func FetchRefs(ctx context.Context, c *http.Client, url, token string, wants []Ref, haves []string, w io.Writer, limit int64) error {
	if len(wants) == 0 || len(wants) > maxPushUpdates {
		return fmt.Errorf("a fetch names 1 to %d refs", maxPushUpdates)
	}
	if len(haves) > maxFetchHaves {
		return fmt.Errorf("a fetch names at most %d commits it has", maxFetchHaves)
	}
	for _, h := range haves {
		if !shaRE.MatchString(h) {
			return errors.New("a fetch named a commit Portenv doesn’t take")
		}
	}
	s, err := uploadSession(c, url, token)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	adv, err := s.AdvertisedReferencesContext(ctx)
	if err != nil {
		return plain(err)
	}
	req := packp.NewUploadPackRequest()
	seen := map[string]bool{}
	for _, want := range wants {
		if !CheckRefName(want.Name) || !shaRE.MatchString(want.SHA) {
			return fmt.Errorf("Portenv fetches only branches and tags, not %.80q", want.Name) //nolint:staticcheck // ST1005: a plain line
		}
		if h, ok := adv.References[want.Name]; !ok || h.String() != want.SHA {
			return fmt.Errorf("%s isn’t at %.7s on GitHub now; fetch again", want.Name, want.SHA)
		}
		if !seen[want.SHA] {
			seen[want.SHA] = true
			req.Wants = append(req.Wants, plumbing.NewHash(want.SHA))
		}
	}
	for _, h := range haves {
		req.Haves = append(req.Haves, plumbing.NewHash(h))
	}
	for _, cp := range []capability.Capability{capability.OFSDelta, capability.NoProgress} {
		if adv.Capabilities.Supports(cp) {
			_ = req.Capabilities.Set(cp)
		}
	}
	resp, err := s.UploadPack(ctx, req)
	if err != nil {
		return plain(err)
	}
	defer func() { _ = resp.Close() }()
	var header strings.Builder
	header.WriteString("# v2 git bundle\n")
	for _, h := range haves {
		fmt.Fprintf(&header, "-%s\n", h)
	}
	for _, want := range wants {
		fmt.Fprintf(&header, "%s %s\n", want.SHA, want.Name)
	}
	header.WriteString("\n")
	if _, err := io.WriteString(w, header.String()); err != nil {
		return err
	}
	n, err := io.Copy(w, io.LimitReader(resp, limit+1))
	if err != nil {
		return plain(err)
	}
	if n > limit {
		return ErrTooBig
	}
	return nil
}
