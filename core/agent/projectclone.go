// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
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
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/githubrepo"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// New workspace's From GitHub (ADR 0025 §2, R-0391). No sign-in ever
// reaches the workspace: portenvd fetches on the Mac and streams a whole
// git bundle. Here, as work, the bundle is written to a 0600 file in a
// 0700 folder of work's, checked (its size; a v2 header with no
// prerequisites; refs only under refs/heads and refs/tags; git bundle
// verify), and cloned from with no network and no git config but the new
// repository's own: a fresh empty HOME, no system or global config, no
// template, no hooks, every object checked. The clone is made beside the
// project and renamed into place once whole; origin is the plain https
// address. Nothing in the repository is run.

// cloneDir is where clone-bundle keeps the bundle while it clones.
const cloneDir = ".portenv/clone"

// pushURL is origin's push address: git runs git-remote-portenv for it.
const pushURL = "portenv::github"

// maxBundleHeader caps the bundle's ref list.
const maxBundleHeader = 16 << 20

type cloneErrorKind int

const (
	cloneFailed cloneErrorKind = iota
	cloneTooBig
)

// cloneError is a refusal with its plain line.
type cloneError struct {
	kind cloneErrorKind
	line string
}

func (e *cloneError) Error() string { return e.line }

type cloneBundleRequest struct {
	Project, URL string
	MaxBytes     int64
	// Head is the branch to check out (refs/heads/…), empty for none.
	Head  string
	Empty bool
}

// CloneProjectResult is clone-bundle's answer.
type CloneProjectResult struct {
	Existed bool `json:"existed"`
}

// bundleRunner runs git with env, in dir.
type bundleRunner func(ctx context.Context, env []string, dir string, args ...string) ([]byte, error)

// bundleGit runs git by its path, with only env, and no terminal.
func bundleGit(git string) bundleRunner {
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

var (
	bundleRefRE  = regexp.MustCompile(`^([0-9a-f]{40}) (HEAD|refs/(?:heads|tags)/[A-Za-z0-9._/+@-]{1,240})$`)
	headBranchRE = regexp.MustCompile(`^refs/heads/[A-Za-z0-9._/+@-]{1,240}$`)
)

// checkRefName refuses what git would refuse, and anything that could
// climb out of refs/: "..", "//", a part starting with "." or ending in
// ".lock", and a trailing "/" or ".".
func checkRefName(ref string) bool {
	if strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// checkBundleHeader reads the bundle's header: "# v2 git bundle", then
// "<sha> <ref>" lines (no "-<sha>" prerequisites), then an empty line.
func checkBundleHeader(r io.Reader) error {
	br := bufio.NewReader(io.LimitReader(r, maxBundleHeader))
	line, err := br.ReadString('\n')
	if err != nil || line != "# v2 git bundle\n" {
		return errors.New("it isn't a version 2 git bundle")
	}
	refs := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return errors.New("its list of branches doesn't end")
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "-") {
			return errors.New("it isn't a whole repository")
		}
		m := bundleRefRE.FindStringSubmatch(line)
		if m == nil || (m[2] != "HEAD" && !checkRefName(m[2])) {
			return fmt.Errorf("it has a reference Portenv doesn’t take (%.80q)", line)
		}
		refs++
	}
	if refs == 0 {
		return errors.New("it has no branches")
	}
	return nil
}

// cloneFromBundle is team-io clone-bundle, as work: the bundle on r.
func cloneFromBundle(ctx context.Context, req cloneBundleRequest, r io.Reader, run bundleRunner) (CloneProjectResult, error) {
	var res CloneProjectResult
	repo, err := githubrepo.Parse(req.URL)
	if err != nil || repo.URL() != req.URL {
		return res, &cloneError{kind: cloneFailed, line: githubrepo.ErrNotAGitHubURL.Error()}
	}
	name := filepath.Base(req.Project)
	switch {
	case !filepath.IsAbs(req.Project) || filepath.Clean(req.Project) != req.Project || name == "" || strings.HasPrefix(name, "."):
		return res, fmt.Errorf("%q isn't a project folder", req.Project)
	case req.Head != "" && (!headBranchRE.MatchString(req.Head) || !checkRefName(req.Head)):
		return res, &cloneError{kind: cloneFailed, line: "The repository’s default branch has a name Portenv doesn’t take."}
	}
	failed := func(why string) error {
		return &cloneError{kind: cloneFailed, line: "Portenv couldn’t clone " + repo.String() + ": " + why + "."}
	}
	work := filepath.Dir(req.Project)
	if err := ownHome(work); err != nil {
		return res, err
	}
	switch _, err := os.Lstat(req.Project); {
	case err == nil:
		res.Existed = true
		return res, nil
	case !errors.Is(err, fs.ErrNotExist):
		return res, err
	}

	home, err := os.MkdirTemp("", "portenv-clone-home-")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(home) }()
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "GIT_LFS_SKIP_SMUDGE=1"}
	safe := []string{"-c", "protocol.allow=never", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "credential.helper=", "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true"}
	git := func(dir string, args ...string) ([]byte, error) {
		return run(ctx, env, dir, append(slicesClone(safe), args...)...)
	}

	tmp := filepath.Join(work, ".portenv-clone-"+name)
	if err := os.RemoveAll(tmp); err != nil { // a try that stopped half-way
		return res, err
	}
	branch := "main"
	if req.Head != "" {
		branch = strings.TrimPrefix(req.Head, "refs/heads/")
	}
	if out, err := git(work, "init", "--quiet", "--template=", "--initial-branch="+branch, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return res, cmdError("start the project", out, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	if out, err := git(tmp, "remote", "add", "origin", repo.URL()); err != nil {
		return res, cmdError("set the project's origin", out, err)
	}
	// Pushes go through Portenv (R-0391): git-remote-portenv, then portenvd
	// on the Mac with the owner's sign-in. Fetches use the address.
	if out, err := git(tmp, "config", "remote.origin.pushurl", pushURL); err != nil {
		return res, cmdError("set the project's push address", out, err)
	}
	if req.Empty {
		if err := os.Rename(tmp, req.Project); err != nil {
			return res, err
		}
		ok = true
		return res, nil
	}

	// The bundle: work's own 0700 folder, a 0600 file, never through a link.
	dfd, err := openDirs(work, strings.Split(cloneDir, "/"), true)
	if err != nil {
		return res, err
	}
	defer func() { _ = unix.Close(dfd) }()
	if err := unix.Fchmod(dfd, 0o700); err != nil {
		return res, err
	}
	file := name + ".bundle"
	if err := unix.Unlinkat(dfd, file, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return res, err
	}
	fd, err := unix.Openat(dfd, file, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return res, err
	}
	defer func() { _ = unix.Unlinkat(dfd, file, 0) }()
	f := os.NewFile(uintptr(fd), file) // #nosec G115 -- a descriptor
	n, err := io.Copy(f, io.LimitReader(r, req.MaxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return res, err
	}
	if n > req.MaxBytes {
		return res, &cloneError{kind: cloneTooBig, line: fmt.Sprintf("%s is bigger than this workspace has room for (over %s).", repo, humanBytes(req.MaxBytes))}
	}
	path := filepath.Join(work, cloneDir, file)
	hf, err := os.Open(path) // #nosec G304 -- the file just written, in work's own folder
	if err != nil {
		return res, err
	}
	herr := checkBundleHeader(hf)
	_ = hf.Close()
	if herr != nil {
		return res, failed("its bundle was refused: " + herr.Error())
	}
	if out, err := git(tmp, "bundle", "verify", "--quiet", path); err != nil {
		return res, failed("its bundle didn’t verify (" + lastLine(out, err) + ")")
	}
	if out, err := git(tmp, "-c", "protocol.file.allow=always", "fetch", "--quiet", "--no-write-fetch-head", "--no-recurse-submodules", path,
		"+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return res, failed(lastLine(out, err))
	}
	if req.Head != "" {
		if out, err := git(tmp, "reset", "--quiet", "--hard", "refs/remotes/origin/"+branch); err != nil {
			return res, failed(lastLine(out, err))
		}
		if out, err := git(tmp, "branch", "--quiet", "--set-upstream-to=origin/"+branch); err != nil {
			return res, failed(lastLine(out, err))
		}
		if out, err := git(tmp, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch); err != nil {
			return res, failed(lastLine(out, err))
		}
	}
	if err := os.Rename(tmp, req.Project); err != nil {
		return res, err
	}
	ok = true
	return res, nil
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

// lastLine is git's last line, without "fatal: ".
func lastLine(out []byte, err error) string {
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	if last == "" {
		last = err.Error()
	}
	last = strings.TrimPrefix(strings.TrimPrefix(last, "fatal: "), "error: ")
	if len(last) > 200 {
		last = last[:200]
	}
	return last
}

// freeBytes is the room left on dir's filesystem for an unprivileged user.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil // #nosec G115 -- the filesystem's own counts
}

// cloneStatus is a clone's refusal as the code portenvd tells apart.
func cloneStatus(err error) error {
	var ce *cloneError
	if !errors.As(err, &ce) {
		return status.Errorf(codes.Internal, "clone the project: %v", err)
	}
	if ce.kind == cloneTooBig {
		return status.Error(codes.ResourceExhausted, ce.line)
	}
	return status.Error(codes.FailedPrecondition, ce.line)
}

// teamIOCloneBundle is "team-io clone-bundle PROJECT URL MAXBYTES HEAD|-
// [empty]", as work, the bundle on stdin: the result as JSON, a refusal as
// {"error_kind","error_line"}.
func teamIOCloneBundle(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 4 && len(args) != 5 {
		return errors.New("clone-bundle PROJECT URL MAXBYTES HEAD|- [empty]")
	}
	project, url, max, head := args[0], args[1], args[2], args[3]
	limit, err := strconv.ParseInt(max, 10, 64)
	if err != nil {
		return errors.New("no limit given")
	}
	if head == "-" {
		head = ""
	}
	empty := false
	if len(args) == 5 {
		empty = args[4] == "empty"
	}
	req := cloneBundleRequest{Project: project, URL: url, MaxBytes: limit, Head: head, Empty: empty}
	res, err := cloneFromBundle(context.Background(), req, stdin, bundleGit(agentGitPath))
	if err != nil {
		var ce *cloneError
		if errors.As(err, &ce) {
			return json.NewEncoder(stdout).Encode(map[string]any{"error_kind": int(ce.kind), "error_line": ce.line})
		}
		return err
	}
	return json.NewEncoder(stdout).Encode(res)
}

func (s *apiServer) projectPath(p string) (string, error) {
	prefix := "/home/" + s.cfg.User + "/"
	name := strings.TrimPrefix(p, prefix)
	if !strings.HasPrefix(p, prefix) || name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") || filepath.Clean(p) != p {
		return "", status.Errorf(codes.InvalidArgument, "%q isn't a project folder in %s", p, prefix)
	}
	return filepath.Join(s.cfg.HomeRoot, s.cfg.User, name), nil
}

// CloneProject clones New workspace's repository from the bundle portenvd
// streams, as work.
func (s *apiServer) CloneProject(stream agentv1.AgentService_CloneProjectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	st := first.GetStart()
	if st == nil {
		return status.Error(codes.InvalidArgument, "the first message names the project")
	}
	project, err := s.projectPath(st.GetProjectDir())
	if err != nil {
		return err
	}
	if repo, err := githubrepo.Parse(st.GetUrl()); err != nil || repo.URL() != st.GetUrl() {
		return status.Error(codes.InvalidArgument, githubrepo.ErrNotAGitHubURL.Error())
	}
	if st.GetMaxBytes() <= 0 && !st.GetEmpty() {
		return status.Error(codes.InvalidArgument, "no limit given")
	}
	head := st.GetHead()
	if head == "" {
		head = "-"
	}
	args := []string{"clone-bundle", project, st.GetUrl(), strconv.FormatInt(st.GetMaxBytes(), 10), head}
	if st.GetEmpty() {
		args = append(args, "empty")
	}
	pr, pw := io.Pipe()
	go func() {
		for {
			m, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				_ = pw.Close()
				return
			}
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write(m.GetChunk()); err != nil {
				return
			}
		}
	}()
	out, err := s.runTeamIO(stream.Context(), args, pr)
	_ = pr.Close()
	if err != nil {
		return status.Errorf(codes.Internal, "clone the project: %v", err)
	}
	var r struct {
		CloneProjectResult
		ErrorKind *int   `json:"error_kind"`
		ErrorLine string `json:"error_line"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		return status.Errorf(codes.Internal, "clone the project: %v", err)
	}
	if r.ErrorKind != nil {
		return cloneStatus(&cloneError{kind: cloneErrorKind(*r.ErrorKind), line: r.ErrorLine})
	}
	return stream.SendAndClose(&agentv1.CloneProjectResponse{Existed: r.Existed})
}

// GetProjectRoom is whether the project is there, and the room for it.
func (s *apiServer) GetProjectRoom(_ context.Context, req *agentv1.GetProjectRoomRequest) (*agentv1.GetProjectRoomResponse, error) {
	project, err := s.projectPath(req.GetProjectDir())
	if err != nil {
		return nil, err
	}
	out := &agentv1.GetProjectRoomResponse{}
	if _, err := os.Lstat(project); err == nil {
		out.Exists = true
	}
	free, err := freeBytes(filepath.Dir(project))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "the workspace's room: %v", err)
	}
	out.FreeBytes = free
	return out, nil
}
