// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/internal/bounded"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// Work's git identity from the Mac (findings 21 and 22): in a new
// workspace, git merge as work failed with "Committer identity unknown".
// At each open (a new workspace's first open is its Create), portenvd reads
// the owner's user.name and user.email from the Mac's git and the helper
// writes them into work's ~/.gitconfig, as work, each only where work has
// none of its own. They aren't secrets. Agents keep their own identities
// (ADR 0022); this never reaches them.
//
// The fallback the owner asked about, the signed-in GitHub account's login
// and its noreply address, needs a call to GitHub with the sign-in, which
// portenvd only ever holds in memory for one request (R-0391): it isn't
// used for this, and with no identity on the Mac nothing is set.

// macGitCandidates are where the Mac's git is looked for, in order:
// Homebrew's (Apple silicon, then Intel), then the Command Line Tools'.
var macGitCandidates = []string{"/opt/homebrew/bin/git", "/usr/local/bin/git", "/usr/bin/git"}

// findMacGit is the first candidate that exists. /usr/bin/git is a stub
// that offers to install the Command Line Tools when they're missing, so it
// is used only when toolsInstalled says they're there: portenvd never
// raises a dialog (CLAUDE.md: nothing in the background waits on the
// person).
func findMacGit(candidates []string, toolsInstalled func() bool) string {
	for _, c := range candidates {
		fi, err := os.Stat(c)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		if c == "/usr/bin/git" && !toolsInstalled() {
			continue
		}
		return c
	}
	return ""
}

// commandLineToolsInstalled is whether xcode-select names a developer
// folder (it prints the path and exits 0 only then; it never prompts).
func commandLineToolsInstalled() bool {
	cmd, err := bounded.Command(context.Background(), 2*time.Second, "/usr/bin/xcode-select", "-p")
	if err != nil {
		return false
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	_, err = os.Stat(strings.TrimSpace(string(out)))
	return err == nil
}

// readMacGitIdentity is git config --global user.name and user.email as
// the Mac user (HOME is their home) with a clean environment: nothing of
// portenvd's own (GIT_CONFIG_GLOBAL, GIT_CONFIG_COUNT, XDG_CONFIG_HOME, an
// author override) changes what's read. A value CheckGitIdentity refuses is
// dropped, on its own; an error reads as none.
func readMacGitIdentity(ctx context.Context, git, home string) (name, email string) {
	if git == "" || home == "" {
		return "", ""
	}
	get := func(key string) string {
		cmd, err := bounded.Command(ctx, 5*time.Second, git, "config", "--global", "--includes", "--get", key)
		if err != nil {
			return ""
		}
		cmd.Dir = home
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSuffix(string(out), "\n")
	}
	name, email = get("user.name"), get("user.email")
	if name != "" && agent.CheckGitIdentity(name, "") != nil {
		name = ""
	}
	if email != "" && agent.CheckGitIdentity("", email) != nil {
		email = ""
	}
	return name, email
}

// theMacGitIdentity is the Mac user's git identity, read afresh each time
// (the owner may set it between opens). portenv-runner, on a server, has
// no owner's Mac to read: none.
func theMacGitIdentity(ctx context.Context) (string, string) {
	if runtime.GOOS != "darwin" {
		return "", ""
	}
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return "", ""
	}
	return readMacGitIdentity(ctx, findMacGit(macGitCandidates, commandLineToolsInstalled), u.HomeDir)
}

// shareGitIdentity sends the Mac's git identity to the box's helper. It
// never fails an open: what happened is logged, without the name or
// address.
func (s *Server) shareGitIdentity(ctx context.Context, boxID string, h agentv1.AgentServiceClient) {
	if h == nil {
		return
	}
	read := s.macGitIdentity
	if read == nil {
		read = theMacGitIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	name, email := read(ctx)
	if name == "" && email == "" {
		s.log.Info("git identity: none on this Mac", "box_id", boxID)
		return
	}
	r, err := h.SetWorkGitIdentity(ctx, &agentv1.SetWorkGitIdentityRequest{Name: name, Email: email})
	switch {
	case status.Code(err) == codes.Unimplemented:
		s.log.Info("git identity: the workspace's helper is older", "box_id", boxID)
	case err != nil:
		s.log.Warn("git identity not set", "box_id", boxID, "code", status.Code(err).String())
	default:
		s.log.Info("git identity", "box_id", boxID, "name_set", r.GetNameSet(), "email_set", r.GetEmailSet())
	}
}
