// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// git fetch, pull and ls-remote through git-remote-portenv (R-0392). git's
// fetch asks for objects by name; this sends the wants and the commits the
// workspace has to work's own socket, and gets back, after the answer, a
// bundle portenvd fetched on the Mac. The bundle is checked before
// anything is unbundled: a v2 header, prerequisites that are object names,
// refs only under refs/heads and refs/tags with safe names and exactly the
// wanted ones, its size, then git bundle verify; then git unbundles it
// with every object checked. git itself updates the refs.

// maxFetchBundle is a fetch's limit.
const maxFetchBundle = 2 << 30

// maxHaves caps the commits a fetch says the workspace has.
const maxHaves = 256

var (
	hexRE     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	safeRefRE = regexp.MustCompile(`^refs/(?:heads|tags)/[A-Za-z0-9._/+@-]{1,240}$`)
)

func safeRef(ref string) bool {
	if !safeRefRE.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") ||
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

// checkFetchHeader reads a fetch bundle's header: it must hold exactly the
// wanted refs at the wanted objects.
func checkFetchHeader(r io.Reader, wants []*agentv1.RemoteRef) error {
	br := bufio.NewReader(io.LimitReader(r, 16<<20))
	line, err := br.ReadString('\n')
	if err != nil || line != "# v2 git bundle\n" {
		return errors.New("it isn’t a version 2 git bundle")
	}
	want := map[string]string{}
	for _, w := range wants {
		want[w.GetName()] = w.GetSha()
	}
	got := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return errors.New("its header doesn’t end")
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			break
		}
		if p, ok := strings.CutPrefix(line, "-"); ok {
			sha, _, _ := strings.Cut(p, " ")
			if !hexRE.MatchString(sha) {
				return errors.New("it has a prerequisite Portenv doesn’t take")
			}
			continue
		}
		sha, ref, ok := strings.Cut(line, " ")
		if !ok || !hexRE.MatchString(sha) || !safeRef(ref) || want[ref] != sha {
			return fmt.Errorf("it has a ref Portenv didn’t ask for (%.80q)", line)
		}
		got++
	}
	if got != len(want) {
		return errors.New("it lacks a ref git asked for")
	}
	return nil
}

// fetchBatch fetches the wanted refs' objects into the repository.
func fetchBatch(asked []*agentv1.RemoteRef, env remoteHelperEnv) error {
	// git may name a ref twice (a pull's refspec and its remote-tracking
	// one): once each.
	var wants []*agentv1.RemoteRef
	named := map[string]bool{}
	for _, w := range asked {
		if !named[w.GetName()] {
			named[w.GetName()] = true
			wants = append(wants, w)
		}
	}
	for _, w := range wants {
		if !hexRE.MatchString(w.GetSha()) || !safeRef(w.GetName()) {
			return fmt.Errorf("Portenv fetches only branches and tags, not %.80q", w.GetName()) //nolint:staticcheck // ST1005: a plain line
		}
	}
	var haves []string
	if out, err := gitIn(env.gitDir, "for-each-ref", "--format=%(objectname)", "refs/heads", "refs/remotes"); err == nil {
		seen := map[string]bool{}
		for _, h := range strings.Fields(out) {
			if hexRE.MatchString(h) && !seen[h] && len(haves) < maxHaves {
				seen[h] = true
				haves = append(haves, h)
			}
		}
	}
	f, err := os.CreateTemp("", "portenv-fetch-*.bundle")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	err = fetchCall(env.socket, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Fetch{Fetch: &agentv1.FetchRefs{Wants: wants, Haves: haves}}}, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	hf, err := os.Open(name) // #nosec G304 -- the file just written
	if err != nil {
		return err
	}
	herr := checkFetchHeader(hf, wants)
	_ = hf.Close()
	if herr != nil {
		return errors.New("Portenv refused the fetch’s bundle: " + herr.Error()) //nolint:staticcheck // ST1005: a plain line
	}
	if out, err := gitIn(env.gitDir, "bundle", "verify", "--quiet", name); err != nil {
		return errors.New("the fetch’s bundle didn’t verify: " + out) //nolint:staticcheck // ST1005: a plain line
	}
	if out, err := gitIn(env.gitDir, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "bundle", "unbundle", name); err != nil {
		return errors.New("the fetch’s bundle couldn’t be unbundled: " + out) //nolint:staticcheck // ST1005: a plain line
	}
	for _, w := range wants {
		if _, err := gitIn(env.gitDir, "cat-file", "-e", w.GetSha()); err != nil {
			return fmt.Errorf("the fetch didn’t bring %s", w.GetName())
		}
	}
	return nil
}

// fetchCall sends the fetch to work's socket and writes the bundle that
// follows the answer to w.
func fetchCall(sock string, call *agentv1.TeamCallsResponse, w io.Writer) error {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return errors.New("Portenv isn’t reachable from here, so this fetch can’t go. Fetch from your own terminal in Portenv’s window") //nolint:staticcheck // ST1005: a plain line
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(pushWait))
	b, err := protojson.Marshal(call)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return errors.New("Portenv refused the fetch") //nolint:staticcheck // ST1005: a plain line
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	line, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return errors.New("no answer from Portenv")
	}
	var r agentv1.TeamCallsRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(line, &r); err != nil {
		return errors.New("Portenv’s answer couldn’t be read") //nolint:staticcheck // ST1005: a plain line
	}
	if !r.GetOk() {
		if r.GetLine() == "" {
			return errors.New("Portenv refused the fetch, without saying why") //nolint:staticcheck // ST1005: a plain line
		}
		return errors.New(r.GetLine())
	}
	n := r.GetFetch().GetBundleBytes()
	if n <= 0 || n > maxFetchBundle {
		return errors.New("the fetch’s bundle has a size Portenv doesn’t take")
	}
	got, err := io.Copy(w, io.LimitReader(br, n))
	if err != nil || got != n {
		return errors.New("the fetch’s bundle came short. Fetch again.") //nolint:staticcheck // ST1005: a plain line
	}
	return nil
}
