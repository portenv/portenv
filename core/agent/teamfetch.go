// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// Hand-back by branch (ADR 0022 §2a point 3, R-0315). Two halves, each in
// its own process with no terminal, a fixed environment and no shell:
//
//  1. as the agent, in its clone: git bundle create - refs/heads/agent/<id>
//     --not --remotes=owner (team-io bundle-own, through as-agent);
//  2. as work, in the owner's project: the bundle written to
//     ~/.portenv/team/handoff/<id>.bundle (work's, 0600, in a 0700 folder),
//     fetched from that file alone with only the file protocol allowed, no
//     hooks or fsmonitor, every object checked, no tags or submodules, into
//     refs/remotes/agents/<id>, and deleted (team-io fetch-agent).
//
// work's git reads only its own repository's config and that file, never
// the agent's config, hooks or metadata.

// errNothingToShare is the plain line when the agent's branch has no
// commit the owner doesn't have.
var errNothingToShare = errors.New("has no commits the owner doesn't have yet")

// errMissingPrerequisite is fetch-agent's line when the owner's repository
// lacks a commit the thin bundle builds on: the helper sends the whole
// branch instead.
var errMissingPrerequisite = errors.New("the owner's repository lacks a commit the branch builds on")

// bundleOwn is team-io bundle-own HOME NAME ID thin|full, run as the agent:
// its clone's agent/<id> as a bundle on stdout, thin (without the commits
// the owner's branches already have) or full.
func bundleOwn(ctx context.Context, home, name, id, mode string, git func(ctx context.Context, dir string, stdout io.Writer, args ...string) ([]byte, error), w io.Writer) error {
	switch {
	case !filepath.IsAbs(home) || filepath.Clean(home) != home:
		return fmt.Errorf("%q isn't a home folder", home)
	case name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, "."):
		return fmt.Errorf("%q isn't a project name", name)
	case !agentIDRE.MatchString(id):
		return fmt.Errorf("%q isn't an agent id", id)
	case mode != "thin" && mode != "full":
		return errors.New("bundle-own needs thin or full")
	}
	if err := ownHome(home); err != nil {
		return err
	}
	clone := filepath.Join(home, name)
	if fi, err := os.Lstat(clone); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s has no copy of the project at ~/%s", id, name)
	}
	args := []string{"bundle", "create", "--quiet", "-", "refs/heads/" + AgentBranch(id)}
	if mode == "thin" {
		args = append(args, "--not", "--remotes=owner")
	}
	out, err := git(ctx, clone, w, args...)
	if err != nil {
		if bytes.Contains(out, []byte("empty bundle")) {
			return fmt.Errorf("%s's branch %s %w", id, AgentBranch(id), errNothingToShare)
		}
		if bytes.Contains(out, []byte("unknown revision")) || bytes.Contains(out, []byte("bad revision")) {
			return fmt.Errorf("%s has no branch %s in its copy of the project", id, AgentBranch(id))
		}
		return cmdError("bundle "+id+"'s branch", out, err)
	}
	return nil
}

// streamGit runs git by its path in home's environment only, with its
// standard output to stdout; it returns what git said on stderr.
func streamGit(home string) func(ctx context.Context, dir string, stdout io.Writer, args ...string) ([]byte, error) {
	return func(ctx context.Context, dir string, stdout io.Writer, args ...string) ([]byte, error) {
		cmd := gitCommand(ctx, home, dir, args...)
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = stdout, &stderr
		err := cmd.Run()
		return stderr.Bytes(), err
	}
}

// FetchResult is fetch-agent's answer.
type FetchResult struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

// handoffDir is where fetch-agent keeps a bundle while it fetches: work's,
// 0700.
const handoffDir = ".portenv/team/handoff"

var commitRE = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)

// fetchAgent is team-io fetch-agent PROJECT ID MAXBYTES, run as work: the
// bundle on stdin is written to ~/.portenv/team/handoff/<id>.bundle (at
// most MAXBYTES), fetched into PROJECT as refs/remotes/agents/<id> with
// the safe fetch, and deleted.
func fetchAgent(ctx context.Context, project, id, maxBytes string, git gitFunc, r io.Reader, w io.Writer) error {
	name := filepath.Base(project)
	switch {
	case !filepath.IsAbs(project) || filepath.Clean(project) != project || name == "" || strings.HasPrefix(name, "."):
		return fmt.Errorf("%q isn't a project folder", project)
	case !agentIDRE.MatchString(id):
		return fmt.Errorf("%q isn't an agent id", id)
	}
	limit, err := strconv.ParseInt(maxBytes, 10, 64)
	if err != nil || limit <= 0 {
		return errors.New("no limit given")
	}
	work := filepath.Dir(project)
	if err := ownHome(work); err != nil {
		return err
	}
	if fi, err := os.Lstat(filepath.Join(project, ".git")); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s isn't a git repository, so it can't take %s's branch", project, id)
	}
	dir := filepath.Join(work, handoffDir)
	dfd, err := openDirs(work, strings.Split(handoffDir, "/"), true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	if err := unix.Fchmod(dfd, 0o700); err != nil {
		return err
	}
	file := id + ".bundle"
	if err := unix.Unlinkat(dfd, file, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := unix.Openat(dfd, file, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Unlinkat(dfd, file, 0) }()
	f := os.NewFile(uintptr(fd), file) // #nosec G115 -- a descriptor
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s's branch is over %s, its files' limit", id, humanBytes(limit))
	}
	if n == 0 {
		return fmt.Errorf("%s's branch came empty", id)
	}
	ref := "refs/remotes/agents/" + id
	out, err := git(ctx, project,
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		filepath.Join(dir, file), "+refs/heads/"+AgentBranch(id)+":"+ref)
	if err != nil {
		if bytes.Contains(out, []byte("lacks these prerequisite")) {
			return errMissingPrerequisite
		}
		return cmdError("fetch "+id+"'s branch", out, err)
	}
	out, err = git(ctx, project, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	commit := strings.TrimSpace(string(out))
	if err != nil || !commitRE.MatchString(commit) {
		return cmdError("read "+ref, out, err)
	}
	return json.NewEncoder(w).Encode(FetchResult{Ref: ref, Commit: commit})
}

// fetchLens is team-io fetch-lens HOME NAME WRITER MAXBYTES, run as a
// lens (ADR 0022 §2a point 4, R-0412 item 16): fetch-agent's half, into
// the lens's own clone HOME/NAME as refs/remotes/agents/<writer>, with the
// bundle kept in the lens's own home while it fetches. The lens's git
// reads only its own repository's config and that file, never the
// writer's.
func fetchLens(ctx context.Context, home, name, writer, maxBytes string, git gitFunc, r io.Reader, w io.Writer) error {
	switch {
	case !filepath.IsAbs(home) || filepath.Clean(home) != home:
		return fmt.Errorf("%q isn't a home folder", home)
	case name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, "."):
		return fmt.Errorf("%q isn't a project name", name)
	case !agentIDRE.MatchString(writer):
		return fmt.Errorf("%q isn't an agent id", writer)
	}
	return fetchAgent(ctx, filepath.Join(home, name), writer, maxBytes, git, r, w)
}

// FetchAgentBranch is the owner's Fetch, in the helper: the agent's
// bundle (as the agent) piped into fetch-agent (as work). A thin bundle
// first; if the owner's repository lacks what it builds on, the whole
// branch. With into_agent, a lens's fetch instead: the same bundle piped
// into fetch-lens, as the lens, in its own clone.
func (s *apiServer) FetchAgentBranch(ctx context.Context, req *agentv1.FetchAgentBranchRequest) (*agentv1.FetchAgentBranchResponse, error) {
	id := req.GetAgentId()
	uid, home, err := s.agentHome(id)
	if err != nil {
		return nil, err
	}
	name, err := s.projectName(req.GetProjectDir())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.GetMaxBytes() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "no limit given")
	}
	limit := strconv.FormatInt(req.GetMaxBytes(), 10)
	// The second half: as work into the project, or as the lens into its
	// own clone.
	fetch := func(bundle io.Reader) ([]byte, error) {
		return s.teamIOAs(ctx, []string{"fetch-agent", filepath.Join(s.cfg.HomeRoot, s.cfg.User, name), id, limit}, bundle)
	}
	if into := req.GetIntoAgent(); into != "" {
		if into == id {
			return nil, status.Errorf(codes.InvalidArgument, "%s doesn't fetch its own branch", id)
		}
		lensUID, lensHome, err := s.agentHome(into)
		if err != nil {
			return nil, err
		}
		fetch = func(bundle io.Reader) ([]byte, error) {
			return s.teamIOAsAgent(ctx, lensUID, lensHome, []string{"fetch-lens", lensHome, name, id, limit}, bundle)
		}
	}
	for _, mode := range []string{"thin", "full"} {
		bundle, wait, err := s.agentStream(ctx, uid, home, []string{"bundle-own", home, name, id, mode})
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		out, ferr := fetch(bundle)
		_ = bundle.Close()
		if werr := wait(); werr != nil {
			return nil, status.Error(codes.FailedPrecondition, werr.Error())
		}
		if ferr != nil {
			if mode == "thin" && strings.Contains(ferr.Error(), errMissingPrerequisite.Error()) {
				continue
			}
			return nil, status.Error(codes.FailedPrecondition, ferr.Error())
		}
		var r FetchResult
		if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
			return nil, status.Errorf(codes.Internal, "fetch %s's branch: %v", id, err)
		}
		return &agentv1.FetchAgentBranchResponse{Ref: r.Ref, Commit: r.Commit}, nil
	}
	return nil, status.Errorf(codes.FailedPrecondition, "%s's branch couldn't be fetched", id)
}

// agentStream starts team-io as the agent with its output on the returned
// reader; wait ends it and says what went wrong. Tests replace it.
func (s *apiServer) agentStream(ctx context.Context, uid int, home string, args []string) (io.ReadCloser, func() error, error) {
	if s.asAgentStream != nil {
		return s.asAgentStream(ctx, uid, home, args)
	}
	return streamTeamIOAsAgent(ctx, uid, home, args)
}

// gitEnv is git's fixed environment for a user whose home is home.
func gitEnv(home string) []string {
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0"}
}
