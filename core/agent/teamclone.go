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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Each coding agent's own clone (ADR 0022 §2a point 2, R-0315): a full
// local clone of the owner's project at /home/<id>/<workspace>, on branch
// agent/<id>, with the remote "owner". The helper makes it as the agent's
// user ("portenv-agent as-agent … -- team-io clone-own", which drops every
// capability and sets no_new_privs before git runs), at the agent's start
// if it's missing. Agents read the owner's project and never write it, so
// nothing an agent does changes what the owner's git reads.

// agentGitPath is the git the clone and the hand-back run, by its absolute
// path (never through an agent's PATH).
const agentGitPath = "/usr/bin/git"

// CloneResult is clone-own's answer: where the clone is, whether this
// start made it, and its branch when it did.
type CloneResult struct {
	Dir    string `json:"dir"`
	Made   bool   `json:"made"`
	Branch string `json:"branch,omitempty"`
}

// gitFunc runs git with args in dir and returns its output (stdout and
// stderr together).
type gitFunc func(ctx context.Context, dir string, args ...string) ([]byte, error)

// homeGit runs git by its absolute path as the calling process's user, in
// home's environment only: no terminal, no inherited variables.
func homeGit(home string) gitFunc {
	return func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, agentGitPath, args...) // #nosec G204 -- git by its path; arguments made here
		cmd.Dir = dir
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0"}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.Bytes(), err
	}
}

// AgentBranch is an agent's branch in its clone.
func AgentBranch(id string) string { return "agent/" + id }

// cloneOwn is team-io clone-own PROJECT HOME ID QUOTA, run as the agent:
// if HOME/<project's name> is missing, it clones the project there (a
// full copy, --no-hardlinks, remote "owner") and switches to agent/<ID>,
// from the owner's agent/<ID> if the project has one, else from the
// owner's current branch. A project bigger than the agent's free space is
// refused with a plain line. An existing folder there is left as it is.
func cloneOwn(ctx context.Context, project, home, id, quota string, git gitFunc, w io.Writer) error {
	name := filepath.Base(project)
	switch {
	case !filepath.IsAbs(project) || filepath.Clean(project) != project || name == "" || strings.HasPrefix(name, "."):
		return fmt.Errorf("%q isn't a project folder", project)
	case !filepath.IsAbs(home) || filepath.Clean(home) != home:
		return fmt.Errorf("%q isn't a home folder", home)
	case !agentIDRE.MatchString(id):
		return fmt.Errorf("%q isn't an agent id", id)
	}
	limit, err := strconv.ParseInt(quota, 10, 64)
	if err != nil || limit <= 0 {
		return errors.New("no quota given")
	}
	if err := ownHome(home); err != nil {
		return err
	}
	dest := filepath.Join(home, name)
	res := CloneResult{Dir: dest}
	if fi, err := os.Lstat(dest); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("~/%s in %s's home isn't a folder, so Portenv can't put %s's copy of the project there. Move it, then choose Start", name, id, id)
		}
		return json.NewEncoder(w).Encode(res)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if fi, err := os.Lstat(filepath.Join(project, ".git")); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s needs its own copy of the project, but %s isn't a git repository. Run git init there, or set checkout: none in its agent file", id, project)
	}
	need := treeSize(filepath.Join(project, ".git"))
	used, err := homeSize(home)
	if err != nil {
		return err
	}
	if used+need > limit {
		return fmt.Errorf("%s's files can't hold a copy of the project (%s; its limit is %s). Raise its limit in Settings", id, humanBytes(need), humanBytes(limit))
	}
	// git's local clone runs upload-pack in the project with command-line
	// settings removed, so the agent's own ~/.gitconfig names the project
	// as safe, once; only this agent's git reads it. Never the system
	// config, which work's git reads too.
	for _, dir := range []string{project, filepath.Join(project, ".git")} {
		if err := ensureSafeDir(ctx, git, home, dir); err != nil {
			return err
		}
	}
	tmp := filepath.Join(home, ".portenv-clone."+name)
	if err := os.RemoveAll(tmp); err != nil { // a start that stopped half-way
		return err
	}
	if out, err := git(ctx, home, "clone", "--quiet", "--no-hardlinks", "--origin", "owner", "--", project, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return cmdError("copy the project into "+id+"'s home", out, err)
	}
	branch := AgentBranch(id)
	var out []byte
	switch {
	case gitOK(ctx, git, tmp, "rev-parse", "--quiet", "--verify", "refs/remotes/owner/"+branch+"^{commit}"):
		out, err = git(ctx, tmp, "switch", "--quiet", "--create", branch, "--track", "owner/"+branch)
	case gitOK(ctx, git, tmp, "rev-parse", "--quiet", "--verify", "HEAD^{commit}"):
		out, err = git(ctx, tmp, "switch", "--quiet", "--create", branch)
	default: // an empty project: the branch starts with the first commit
		out, err = git(ctx, tmp, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return cmdError("make "+branch+" in "+id+"'s copy", out, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	res.Made, res.Branch = true, branch
	return json.NewEncoder(w).Encode(res)
}

// ownHome checks the calling process owns home: clone-own runs only as the
// agent whose home it is.
func ownHome(home string) error {
	fi, err := os.Lstat(home)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s isn't this user's home", home)
	}
	return nil
}

func gitOK(ctx context.Context, git gitFunc, dir string, args ...string) bool {
	_, err := git(ctx, dir, args...)
	return err == nil
}

// ensureSafeDir adds dir to the user's global safe.directory, once.
func ensureSafeDir(ctx context.Context, git gitFunc, home, dir string) error {
	out, _ := git(ctx, home, "config", "--global", "--get-all", "safe.directory")
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == dir {
			return nil
		}
	}
	if out, err := git(ctx, home, "config", "--global", "--add", "safe.directory", dir); err != nil {
		return cmdError("let git read the project", out, err)
	}
	return nil
}

// treeSize is the size of the regular files under dir, never following a
// link; what it can't read counts as nothing.
func treeSize(dir string) int64 {
	n, _ := homeSize(dir)
	return n
}

// humanBytes is a size as the app writes it: "3.1 GB", "640 MB".
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatInt(n>>20, 10) + " MB"
	case n >= 1<<10:
		return strconv.FormatInt(n>>10, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// projectName is the workspace's project folder's name, from
// /home/<user>/<name>, or an error.
func (s *apiServer) projectName(projectDir string) (string, error) {
	prefix := "/home/" + s.cfg.User + "/"
	name, ok := strings.CutPrefix(projectDir, prefix)
	if !ok || name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") || filepath.Clean(projectDir) != projectDir {
		return "", fmt.Errorf("%q isn't a project folder in %s", projectDir, prefix)
	}
	return name, nil
}

// ensureClone makes the agent's clone if it's missing, as the agent, and
// returns its folder (ADR 0022 §2a point 2).
func (s *apiServer) ensureClone(ctx context.Context, id string, u AgentUser, projectDir string, quota int64) (string, error) {
	name, err := s.projectName(projectDir)
	if err != nil {
		return "", status.Error(codes.InvalidArgument, err.Error())
	}
	if quota <= 0 {
		return "", status.Error(codes.InvalidArgument, "no home quota given")
	}
	project := filepath.Join(s.cfg.HomeRoot, s.cfg.User, name)
	out, err := s.teamIOAsAgent(ctx, u.UID, u.Home, []string{"clone-own", project, u.Home, id, strconv.FormatInt(quota, 10)}, nil)
	if err != nil {
		return "", status.Error(codes.FailedPrecondition, err.Error())
	}
	var r CloneResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		return "", status.Errorf(codes.Internal, "%s's copy of the project: %v", id, err)
	}
	if want := filepath.Join(u.Home, name); r.Dir != want {
		return "", status.Errorf(codes.Internal, "%s's copy of the project is at %q, not %s", id, r.Dir, want)
	}
	return r.Dir, nil
}

// teamIOAsAgent runs team-io as an agent's user: as-agent's switch (its
// group and team, no capabilities, no_new_privs). Tests replace it.
func (s *apiServer) teamIOAsAgent(ctx context.Context, uid int, home string, args []string, stdin io.Reader) ([]byte, error) {
	if s.asAgentIO != nil {
		return s.asAgentIO(ctx, uid, home, args, stdin)
	}
	return runTeamIOAsAgent(ctx, uid, home, args, stdin)
}
