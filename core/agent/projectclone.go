// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/githubrepo"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// New workspace's From GitHub (ADR 0025 §2). The helper clones as work,
// before any agent's user exists, into a temporary folder beside the
// project that's renamed into place only once the clone is whole. git runs
// hardened: https only, no submodules, no LFS, no credential helper, no
// hooks, and no config from the system, work's home or anywhere else. The
// workspace's room is checked against GitHub's size before git runs, and
// again while bytes arrive.

// cloneMargin is the room a clone leaves free (ADR 0025 §2).
const cloneMargin = 1_000_000_000

// cloneRunner runs git with env, in dir.
type cloneRunner func(ctx context.Context, env []string, dir string, args ...string) ([]byte, error)

type cloneRequest struct {
	Project string
	URL     string
	// Copies of the project the workspace will hold: work's and each
	// coding agent's.
	Copies   int
	Estimate int64
	// Tests: how often the size is looked at, and a file:// source in
	// place of the https address (the remote stays the address).
	sizeEvery  time.Duration
	testSource string
}

// CloneProjectResult is clone-project's answer.
type CloneProjectResult struct {
	Existed bool  `json:"existed"`
	Bytes   int64 `json:"bytes"`
}

type cloneErrorKind int

const (
	cloneFailed cloneErrorKind = iota
	cloneNeedsSignIn
	cloneTooBig
)

// cloneError is a refusal with its plain line.
type cloneError struct {
	kind cloneErrorKind
	line string
}

func (e *cloneError) Error() string { return e.line }

const needsSignInLine = "This repository needs a sign-in. Sign in with GitHub, then try again."

// gigabytes is "3.2 GB", in decimal units, as macOS shows sizes.
func gigabytes(n int64) string {
	g := float64(n) / 1e9
	if g >= 10 && g == float64(int64(g)) {
		return strconv.FormatInt(int64(g), 10) + " GB"
	}
	s := strconv.FormatFloat(g, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + " GB"
}

func tooBig(name string, each int64, copies int, free uint64) *cloneError {
	need := each*2*int64(copies) + cloneMargin
	return &cloneError{kind: cloneTooBig, line: fmt.Sprintf("%s needs about %s with its team (%d copies of %s, each with its checkout). This workspace has %s free.",
		name, gigabytes(need), copies, gigabytes(each), gigabytes(int64(free)))} // #nosec G115 -- a disk's free bytes
}

// cloneProject clones req.URL into req.Project, or does nothing when the
// project is already there.
func cloneProject(ctx context.Context, req cloneRequest, run cloneRunner, free func(string) (uint64, error)) (CloneProjectResult, error) {
	var res CloneProjectResult
	repo, err := githubrepo.Parse(req.URL)
	if err != nil {
		return res, &cloneError{kind: cloneFailed, line: err.Error()}
	}
	if !filepath.IsAbs(req.Project) || filepath.Clean(req.Project) != req.Project {
		return res, fmt.Errorf("%q isn't a project folder", req.Project)
	}
	copies := max(req.Copies, 1)
	name := filepath.Base(req.Project)
	switch _, err := os.Lstat(req.Project); {
	case err == nil:
		res.Existed = true
		return res, nil
	case !errors.Is(err, fs.ErrNotExist):
		return res, err
	}
	parent := filepath.Dir(req.Project)
	room, err := free(parent)
	if err != nil {
		return res, err
	}
	if req.Estimate > 0 && uint64(req.Estimate*2*int64(copies)+cloneMargin) > room { // #nosec G115 -- positive
		return res, tooBig(name, req.Estimate, copies, room)
	}

	tmp := filepath.Join(parent, ".portenv-clone-"+name)
	if err := os.RemoveAll(tmp); err != nil { // a try that stopped half-way
		return res, err
	}
	home, err := os.MkdirTemp("", "portenv-clone-home-")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(home) }()
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_ASKPASS=/bin/false"}
	source, protocol := repo.URL(), "-c protocol.https.allow=always"
	if req.testSource != "" {
		source, protocol = req.testSource, "-c protocol.file.allow=always"
	}
	args := []string{"-c", "protocol.allow=never", strings.Fields(protocol)[0], strings.Fields(protocol)[1],
		"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null",
		"clone", "--quiet", "--no-recurse-submodules", "--template=", "--", source, tmp}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	every := req.sizeEvery
	if every == 0 {
		every = 500 * time.Millisecond
	}
	outgrown := make(chan int64, 1)
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-tick.C:
				if n := treeSize(tmp); uint64(n*int64(copies)+cloneMargin) > room { // #nosec G115 -- positive
					outgrown <- n
					return
				}
			}
		}
	}()
	out, err := run(cctx, env, parent, args...)
	cancel()
	if err != nil {
		_ = os.RemoveAll(tmp)
		select {
		case n := <-outgrown:
			return res, tooBig(name, max(n, req.Estimate), copies, room)
		default:
		}
		return res, cloneFailure(repo, out, err)
	}
	if req.testSource != "" {
		if out, err := run(ctx, env, parent, "-C", tmp, "remote", "set-url", "origin", repo.URL()); err != nil {
			_ = os.RemoveAll(tmp)
			return res, cloneFailure(repo, out, err)
		}
	}
	res.Bytes = treeSize(tmp)
	if err := os.Rename(tmp, req.Project); err != nil {
		_ = os.RemoveAll(tmp)
		return res, err
	}
	return res, nil
}

// cloneFailure is git's failure as a plain line.
func cloneFailure(repo githubrepo.Repo, out []byte, err error) *cloneError {
	text := string(out)
	for _, s := range []string{"could not read Username", "Authentication failed", "Repository not found", "terminal prompts disabled", "returned error: 401", "returned error: 403"} {
		if strings.Contains(text, s) {
			return &cloneError{kind: cloneNeedsSignIn, line: needsSignInLine}
		}
	}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	if last == "" {
		last = err.Error()
	}
	last = strings.TrimPrefix(last, "fatal: ")
	if len(last) > 200 {
		last = last[:200]
	}
	return &cloneError{kind: cloneFailed, line: "Portenv couldn’t clone " + repo.String() + ": " + last}
}

// gitCloneRunner runs git by its path, with only env, and no terminal.
func gitCloneRunner(git string) cloneRunner {
	return func(ctx context.Context, env []string, dir string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, git, args...) // #nosec G204 -- git by its path; arguments made here
		cmd.Dir = dir
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.Bytes(), err
	}
}

// freeBytes is the room left on dir's filesystem for an unprivileged user.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil // #nosec G115 -- the filesystem's own counts
}

// cloneStatus is a clone's refusal as the code the app tells apart.
func cloneStatus(err error) error {
	var ce *cloneError
	if !errors.As(err, &ce) {
		return status.Errorf(codes.Internal, "clone the project: %v", err)
	}
	code := map[cloneErrorKind]codes.Code{cloneNeedsSignIn: codes.Unauthenticated, cloneTooBig: codes.ResourceExhausted, cloneFailed: codes.FailedPrecondition}[ce.kind]
	return status.Error(code, ce.line)
}

// teamIOCloneProject is "team-io clone-project PROJECT URL COPIES ESTIMATE",
// as work: the result as JSON, or the refusal as "kind line" on stderr's
// last line.
func teamIOCloneProject(args []string, stdout io.Writer) error {
	copies, _ := strconv.Atoi(args[2])
	est, _ := strconv.ParseInt(args[3], 10, 64)
	res, err := cloneProject(context.Background(), cloneRequest{Project: args[0], URL: args[1], Copies: copies, Estimate: est}, gitCloneRunner(agentGitPath), freeBytes)
	if err != nil {
		var ce *cloneError
		if errors.As(err, &ce) {
			return json.NewEncoder(stdout).Encode(map[string]any{"error_kind": int(ce.kind), "error_line": ce.line})
		}
		return err
	}
	return json.NewEncoder(stdout).Encode(res)
}

// CloneProject clones New workspace's GitHub repository as work.
func (s *apiServer) CloneProject(ctx context.Context, req *agentv1.CloneProjectRequest) (*agentv1.CloneProjectResponse, error) {
	prefix := "/home/" + s.cfg.User + "/"
	p := req.GetProjectDir()
	name := strings.TrimPrefix(p, prefix)
	if !strings.HasPrefix(p, prefix) || name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") || filepath.Clean(p) != p {
		return nil, status.Errorf(codes.InvalidArgument, "%q isn't a project folder in %s", p, prefix)
	}
	if _, err := githubrepo.Parse(req.GetUrl()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out, err := s.runTeamIO(ctx, []string{"clone-project", filepath.Join(s.cfg.HomeRoot, s.cfg.User, name), req.GetUrl(),
		strconv.Itoa(int(req.GetCopies())), strconv.FormatInt(req.GetEstimateBytes(), 10)}, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "clone the project: %v", err)
	}
	var r struct {
		CloneProjectResult
		ErrorKind *int   `json:"error_kind"`
		ErrorLine string `json:"error_line"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, status.Errorf(codes.Internal, "clone the project: %v", err)
	}
	if r.ErrorKind != nil {
		return nil, cloneStatus(&cloneError{kind: cloneErrorKind(*r.ErrorKind), line: r.ErrorLine})
	}
	return &agentv1.CloneProjectResponse{Existed: r.Existed, Bytes: r.Bytes}, nil
}
