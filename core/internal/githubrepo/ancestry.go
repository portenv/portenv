// SPDX-License-Identifier: Apache-2.0

package githubrepo

import (
	"bufio"
	"bytes"
	"crypto/sha1" // #nosec G505 -- git's object names, not security
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

// Fast-forwards are proven, never taken on trust (R-0393). A bundle's
// prerequisites are what the workspace says it has; they prove nothing
// about history. An update is a fast-forward only if walking the new
// commit's parents through the commits in the bundle's own pack reaches
// the remote's current commit. Anything not proven (an unrelated commit, a
// commit stored as a delta, a pack that doesn't read) is treated as a
// force: the owner is asked, and without the yes it's refused.

// maxAncestryCommits caps the commits read from one pack.
const maxAncestryCommits = 1 << 20

// packParents reads the pack after the bundle's header and returns each
// whole commit's parents, by the commit's own name (worked out from its
// bytes, so a commit can't claim another's name).
func packParents(bundle io.ReadSeeker) (map[string][]string, error) {
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(bundle, 64<<10)
	if _, _, err := readBundleHeader(br); err != nil {
		return nil, err
	}
	sc := packfile.NewScanner(br)
	_, count, err := sc.Header()
	if err != nil {
		return nil, err
	}
	parents := map[string][]string{}
	for i := uint32(0); i < count; i++ {
		h, err := sc.NextObjectHeader()
		if err != nil {
			return nil, err
		}
		if h.Type != plumbing.CommitObject {
			if _, _, err := sc.NextObject(io.Discard); err != nil {
				return nil, err
			}
			continue
		}
		var content bytes.Buffer
		if _, _, err := sc.NextObject(&content); err != nil {
			return nil, err
		}
		if len(parents) >= maxAncestryCommits {
			return nil, fmt.Errorf("the pack holds over %d commits", maxAncestryCommits)
		}
		sum := sha1.New() // #nosec G401 -- git's object name
		fmt.Fprintf(sum, "commit %d\x00", content.Len())
		sum.Write(content.Bytes())
		name := hex.EncodeToString(sum.Sum(nil))
		var ps []string
		for _, line := range strings.Split(content.String(), "\n") {
			if line == "" {
				break // the end of the headers
			}
			if p, ok := strings.CutPrefix(line, "parent "); ok && shaRE.MatchString(p) {
				ps = append(ps, p)
			}
		}
		parents[name] = ps
	}
	return parents, nil
}

// isAncestor is whether old is reached from new through parents.
func isAncestor(parents map[string][]string, old, new string) bool {
	seen := map[string]bool{}
	queue := []string{new}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == old {
			return true
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		queue = append(queue, parents[c]...)
	}
	return false
}

// FastForwards says, for each update with an old value and a new one,
// whether the bundle proves it's a fast-forward.
func FastForwards(bundle io.ReadSeeker, updates []Update) map[string]bool {
	out := map[string]bool{}
	if bundle == nil {
		return out
	}
	parents, err := packParents(bundle)
	if err != nil {
		return out
	}
	for _, u := range updates {
		if u.Old != "" && u.New != "" {
			out[u.Dst] = isAncestor(parents, u.Old, u.New)
		}
	}
	return out
}
