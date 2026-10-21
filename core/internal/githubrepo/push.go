// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- git's pack checksum
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// The push on the Mac (R-0391): git-remote-portenv, in the workspace,
// bundles what's pushed (temporary refs refs/portenv-push/N for the new
// objects, --not the remote's current values); portenvd checks the bundle
// and the ref updates here and sends the pack with the ref commands to
// GitHub's receive-pack with the owner's sign-in. The sign-in never
// leaves this process.

// MaxPushBytes is GitHub's limit for one push.
const MaxPushBytes = 2 << 30

// maxPushUpdates caps the refs one push names.
const maxPushUpdates = 64

// Update is one ref the push changes.
type Update struct {
	// Dst is refs/heads/… or refs/tags/….
	Dst string
	// New is the new object; empty deletes Dst.
	New string
	// Old is what list for-push said Dst was; empty when it didn't exist.
	Old   string
	Force bool
}

// RefStatus is one ref's result, in git's words.
type RefStatus struct {
	Dst    string
	OK     bool
	Reason string
}

// Ref is one of the remote's refs.
type Ref struct {
	Name, SHA string
}

var (
	shaRE        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pushRefRE    = regexp.MustCompile(`^refs/(?:heads|tags)/[A-Za-z0-9._/+@-]{1,240}$`)
	bundleTempRE = regexp.MustCompile(`^refs/portenv-push/[0-9]{1,3}$`)
)

// CheckRefName refuses a ref name git would refuse, or one that could
// climb out of its folder.
func CheckRefName(ref string) bool {
	if !pushRefRE.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func receiveSession(c *http.Client, url, token string) (transport.ReceivePackSession, error) {
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return nil, err
	}
	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}
	s, err := githttp.NewClient(c).NewReceivePackSession(ep, auth)
	if err != nil {
		return nil, plain(err)
	}
	return s, nil
}

// ListRemote is the remote's branches and tags, as receive-pack
// advertises them (git's list for-push).
func ListRemote(ctx context.Context, c *http.Client, url, token string) ([]Ref, error) {
	s, err := receiveSession(c, url, token)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	adv, err := s.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return nil, nil
	}
	if err != nil {
		return nil, plain(err)
	}
	var refs []Ref
	for name, h := range adv.References {
		if CheckRefName(name) && !h.IsZero() {
			refs = append(refs, Ref{Name: name, SHA: h.String()})
		}
	}
	return refs, nil
}

// CheckUpdates refuses a push that names refs Portenv doesn't push, a
// bad object name, or the same ref twice.
func CheckUpdates(updates []Update) error {
	if len(updates) == 0 || len(updates) > maxPushUpdates {
		return fmt.Errorf("a push names 1 to %d refs", maxPushUpdates)
	}
	seen := map[string]bool{}
	for _, u := range updates {
		switch {
		case !CheckRefName(u.Dst):
			return fmt.Errorf("Portenv pushes only branches and tags, not %.80q", u.Dst) //nolint:staticcheck // ST1005: a plain line
		case seen[u.Dst]:
			return fmt.Errorf("%s is named twice", u.Dst)
		case u.New != "" && !shaRE.MatchString(u.New), u.Old != "" && !shaRE.MatchString(u.Old):
			return fmt.Errorf("%s has an object name Portenv doesn’t take", u.Dst)
		}
		seen[u.Dst] = true
	}
	return nil
}

// readBundleHeader reads a push bundle's header: its prerequisites, and
// its refs, which must be refs/portenv-push/N.
func readBundleHeader(br *bufio.Reader) (prereqs, tips map[string]bool, err error) {
	line, err := br.ReadString('\n')
	if err != nil || line != "# v2 git bundle\n" {
		return nil, nil, errors.New("it isn’t a version 2 git bundle")
	}
	prereqs, tips = map[string]bool{}, map[string]bool{}
	for n := 0; ; n++ {
		if n > 10000 {
			return nil, nil, errors.New("its header is too long")
		}
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, nil, errors.New("its header doesn’t end")
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			return prereqs, tips, nil
		}
		if rest, ok := strings.CutPrefix(line, "-"); ok {
			sha, _, _ := strings.Cut(rest, " ")
			if !shaRE.MatchString(sha) {
				return nil, nil, errors.New("it has a prerequisite Portenv doesn’t take")
			}
			prereqs[sha] = true
			continue
		}
		sha, ref, ok := strings.Cut(line, " ")
		if !ok || !shaRE.MatchString(sha) || !bundleTempRE.MatchString(ref) {
			return nil, nil, fmt.Errorf("it has a ref Portenv doesn’t take (%.80q)", line)
		}
		tips[sha] = true
	}
}

// countingReader stops at a limit.
type countingReader struct {
	r     io.Reader
	n     int64
	limit int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.limit {
		return n, ErrTooBig
	}
	return n, err
}

// Push checks the updates and the bundle, then pushes them to url with
// token. bundle is nil when every update is a delete.
func Push(ctx context.Context, c *http.Client, url, token string, updates []Update, bundle io.ReadSeeker) ([]RefStatus, error) {
	return pushWithLimit(ctx, c, url, token, updates, bundle, MaxPushBytes)
}

func pushWithLimit(ctx context.Context, c *http.Client, url, token string, updates []Update, bundle io.ReadSeeker, limit int64) ([]RefStatus, error) {
	if err := CheckUpdates(updates); err != nil {
		return nil, err
	}
	needsPack := false
	for _, u := range updates {
		if u.New != "" {
			needsPack = true
		}
	}
	var pack io.Reader
	// With no bundle, every new object must be on GitHub already: checked
	// once its refs are read, below.
	// Fast-forwards are proven from the pack (R-0393), before it's sent.
	var ff map[string]bool
	if needsPack && bundle != nil {
		ff = FastForwards(bundle, updates)
		if _, err := bundle.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		cr := &countingReader{r: bundle, limit: limit}
		br := bufio.NewReaderSize(cr, 64<<10)
		var tips map[string]bool
		var err error
		if _, tips, err = readBundleHeader(br); err != nil {
			if errors.Is(cr.lastErr(), ErrTooBig) {
				return nil, ErrTooBig
			}
			return nil, fmt.Errorf("the push’s bundle was refused: %w", err)
		}
		news := map[string]bool{}
		for _, u := range updates {
			if u.New != "" {
				news[u.New] = true
				if !tips[u.New] {
					return nil, fmt.Errorf("the push’s bundle doesn’t hold %s for %s", u.New[:7], u.Dst)
				}
			}
		}
		for t := range tips {
			if !news[t] {
				return nil, errors.New("the push’s bundle holds an object no update names")
			}
		}
		pack = br
	}

	s, err := receiveSession(c, url, token)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	adv, err := s.AdvertisedReferencesContext(ctx)
	if err != nil && !errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return nil, plain(err)
	}
	current := map[string]string{}
	if adv != nil {
		for name, h := range adv.References {
			current[name] = h.String()
		}
	}
	if bundle == nil {
		have := map[string]bool{}
		for _, v := range current {
			have[v] = true
		}
		for _, u := range updates {
			if u.New != "" && !have[u.New] {
				return nil, errors.New("the push came without its objects")
			}
		}
	}
	statuses := make([]RefStatus, len(updates))
	var commands []*packp.Command
	for i, u := range updates {
		statuses[i] = RefStatus{Dst: u.Dst}
		cur := current[u.Dst]
		switch {
		case cur != u.Old:
			statuses[i].Reason = "fetch first"
			continue
		case cur != "" && u.New != "" && !u.Force && strings.HasPrefix(u.Dst, "refs/tags/"):
			statuses[i].Reason = "already exists"
			continue
		case cur != "" && u.New != "" && !u.Force && !ff[u.Dst]:
			statuses[i].Reason = "non-fast-forward"
			continue
		}
		cmd := &packp.Command{Name: plumbing.ReferenceName(u.Dst), Old: plumbing.ZeroHash, New: plumbing.ZeroHash}
		if cur != "" {
			cmd.Old = plumbing.NewHash(cur)
		}
		if u.New != "" {
			cmd.New = plumbing.NewHash(u.New)
		}
		commands = append(commands, cmd)
	}
	if len(commands) == 0 {
		return statuses, nil
	}
	req := packp.NewReferenceUpdateRequestFromCapabilities(adv.Capabilities)
	if adv.Capabilities.Supports(capability.DeleteRefs) {
		_ = req.Capabilities.Set(capability.DeleteRefs)
	}
	req.Commands = commands
	hasNew := false
	for _, c := range commands {
		if !c.New.IsZero() {
			hasNew = true
		}
	}
	if hasNew {
		if pack == nil {
			pack = bytes.NewReader(emptyPack())
		}
		req.Packfile = io.NopCloser(pack)
	}
	report, err := s.ReceivePack(ctx, req)
	if err != nil && report == nil {
		if errors.Is(err, ErrTooBig) {
			return nil, ErrTooBig
		}
		return nil, plain(err)
	}
	byName := map[string]string{}
	if report != nil {
		for _, cs := range report.CommandStatuses {
			byName[cs.ReferenceName.String()] = cs.Status
		}
	}
	for i := range statuses {
		if statuses[i].Reason != "" {
			continue
		}
		switch st, ok := byName[statuses[i].Dst]; {
		case !ok && err == nil:
			statuses[i].OK = true
		case st == "ok":
			statuses[i].OK = true
		case !ok:
			statuses[i].Reason = "failed"
		default:
			statuses[i].Reason = strings.TrimSpace(st)
		}
	}
	return statuses, nil
}

func (c *countingReader) lastErr() error {
	if c.n > c.limit {
		return ErrTooBig
	}
	return nil
}

// emptyPack is a pack with no objects, which receive-pack takes when every
// new object is on the remote already.
func emptyPack() []byte {
	b := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0}
	sum := sha1.Sum(b) // #nosec G401 -- git's pack checksum, not security
	return append(b, sum[:]...)
}
