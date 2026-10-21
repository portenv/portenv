// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// The fetch on the Mac (R-0391): portenvd asks GitHub's smart-HTTP
// upload-pack for every branch and tag, with the owner's sign-in when the
// repository needs one, and writes a whole git bundle (no prerequisites):
// its header, then the pack exactly as GitHub sent it. Nothing here parses
// the pack; the helper's git checks every object when it clones from the
// bundle, inside the workspace, with no network. The sign-in never leaves
// this process: it isn't in the bundle, on disk or in a log.

// ErrNeedsSignIn is a repository GitHub won't serve without a sign-in (or
// with this one): private, gone, or the sign-in has ended.
var ErrNeedsSignIn = errors.New("this repository needs a sign-in")

// ErrTooBig is a pack bigger than the room it was given.
var ErrTooBig = errors.New("the repository is bigger than the room for it")

// Fetched is what FetchBundle brought.
type Fetched struct {
	// Head is the remote's default branch (refs/heads/…), empty if it has
	// none.
	Head string
	// Empty: the repository has no branches or tags; nothing was written.
	Empty bool
	// Refs is how many branches and tags the bundle holds.
	Refs int
}

// FetchBundle writes the repository at url as a git bundle to w, at most
// limit bytes of pack. token is empty for a public repository.
func FetchBundle(ctx context.Context, c *http.Client, url, token string, w io.Writer, limit int64) (Fetched, error) {
	var res Fetched
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return res, err
	}
	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}
	sess, err := githttp.NewClient(c).NewUploadPackSession(ep, auth)
	if err != nil {
		return res, plain(err)
	}
	defer func() { _ = sess.Close() }()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		res.Empty = true
		return res, nil
	}
	if err != nil {
		return res, plain(err)
	}
	type ref struct {
		name string
		hash plumbing.Hash
	}
	var refs []ref
	for name, h := range adv.References {
		if (strings.HasPrefix(name, "refs/heads/") || strings.HasPrefix(name, "refs/tags/")) && !strings.HasSuffix(name, "^{}") && !h.IsZero() {
			refs = append(refs, ref{name, h})
		}
	}
	if len(refs) == 0 {
		res.Empty = true
		return res, nil
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	for _, v := range adv.Capabilities.Get(capability.SymRef) {
		if head, target, ok := strings.Cut(v, ":"); ok && head == "HEAD" && strings.HasPrefix(target, "refs/heads/") {
			res.Head = target
		}
	}
	if res.Head == "" && adv.Head != nil {
		for _, r := range refs {
			if r.hash == *adv.Head && strings.HasPrefix(r.name, "refs/heads/") {
				res.Head = r.name
				break
			}
		}
	}

	req := packp.NewUploadPackRequest()
	seen := map[plumbing.Hash]bool{}
	for _, r := range refs {
		if !seen[r.hash] {
			seen[r.hash] = true
			req.Wants = append(req.Wants, r.hash)
		}
	}
	// No side-band: the pack follows the server's NAK as it is.
	for _, cp := range []capability.Capability{capability.OFSDelta, capability.NoProgress} {
		if adv.Capabilities.Supports(cp) {
			_ = req.Capabilities.Set(cp)
		}
	}
	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		return res, plain(err)
	}
	defer func() { _ = resp.Close() }()

	var header strings.Builder
	header.WriteString("# v2 git bundle\n")
	for _, r := range refs {
		fmt.Fprintf(&header, "%s %s\n", r.hash, r.name)
	}
	if res.Head != "" {
		for _, r := range refs {
			if r.name == res.Head {
				fmt.Fprintf(&header, "%s HEAD\n", r.hash)
			}
		}
	}
	header.WriteString("\n")
	if _, err := io.WriteString(w, header.String()); err != nil {
		return res, err
	}
	n, err := io.Copy(w, io.LimitReader(resp, limit+1))
	if err != nil {
		return res, plain(err)
	}
	if n > limit {
		return res, ErrTooBig
	}
	if n == 0 {
		return res, errors.New("GitHub sent no pack")
	}
	res.Refs = len(refs)
	return res, nil
}

// plain is a transport error with no URL or sign-in in it.
func plain(err error) error {
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed), errors.Is(err, transport.ErrRepositoryNotFound):
		return ErrNeedsSignIn
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	var se *githttp.Err
	if errors.As(err, &se) && se.Response != nil {
		switch se.Response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return ErrNeedsSignIn
		}
		return fmt.Errorf("GitHub answered %d", se.Response.StatusCode)
	}
	return errors.New("GitHub couldn’t be reached")
}
