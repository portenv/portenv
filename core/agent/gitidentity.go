// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// Work's git identity (findings 21 and 22): portenvd reads the owner's
// user.name and user.email from the Mac's git and the helper writes them
// into work's ~/.gitconfig, as work, each only when work's git has none of
// its own. Agents keep their own (ADR 0022, R-0328): this never touches
// their homes or their clones' config.

// Bounds on a git identity, in characters (name) and bytes (email; an
// address is at most 254).
const (
	maxGitName  = 128
	maxGitEmail = 254
)

// CheckGitIdentity reports whether name and email are fit to write into a
// git config: at least one given, each one line of valid UTF-8 with no
// control or bidi characters, no angle brackets (git's own delimiters for
// an address), no leading or trailing space, and bounded. They aren't
// secrets, but they come from outside the workspace.
func CheckGitIdentity(name, email string) error {
	if name == "" && email == "" {
		return errors.New("no git name or email")
	}
	if name != "" {
		if err := checkGitValue(name); err != nil {
			return fmt.Errorf("the git name %w", err)
		}
		if utf8.RuneCountInString(name) > maxGitName {
			return fmt.Errorf("the git name is over %d characters", maxGitName)
		}
	}
	if email != "" {
		if err := checkGitValue(email); err != nil {
			return fmt.Errorf("the git email %w", err)
		}
		if len(email) > maxGitEmail {
			return fmt.Errorf("the git email is over %d bytes", maxGitEmail)
		}
	}
	return nil
}

func checkGitValue(v string) error {
	if !utf8.ValidString(v) {
		return errors.New("isn't valid UTF-8")
	}
	if strings.TrimSpace(v) != v {
		return errors.New("starts or ends with a space")
	}
	for _, r := range v {
		switch {
		case unicode.IsControl(r), r == 0x2028, r == 0x2029:
			return errors.New("has a control character")
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
			return errors.New("has a bidi control")
		case r == '<', r == '>':
			return errors.New("has an angle bracket")
		}
	}
	return nil
}

// gitIdentity is team-io git-identity's stdin.
type gitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// GitIdentityResult says which of the two were written.
type GitIdentityResult struct {
	NameSet  bool `json:"name_set"`
	EmailSet bool `json:"email_set"`
}

// gitIdentityGit is the git team-io git-identity runs (tests replace it).
var gitIdentityGit = agentGitPath

// setWorkGitIdentity writes name and email into home's ~/.gitconfig with
// git config --file (arguments, never a shell), each only when the user's
// git has none (in ~/.gitconfig, ~/.config/git/config or a file either
// includes; hasGitValue). A config git can't read is an error and isn't written. Run as
// the user whose home it is.
func setWorkGitIdentity(ctx context.Context, run bundleRunner, home, name, email string) (GitIdentityResult, error) {
	var res GitIdentityResult
	if err := CheckGitIdentity(name, email); err != nil {
		return res, err
	}
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1"}
	for _, kv := range []struct {
		key, value string
		set        *bool
	}{{"user.name", name, &res.NameSet}, {"user.email", email, &res.EmailSet}} {
		if kv.value == "" {
			continue
		}
		own, err := hasGitValue(ctx, run, env, home, kv.key)
		if err != nil {
			return res, err
		}
		if own {
			continue // work's own: kept
		}
		if out, err := run(ctx, env, home, "config", "--file", filepath.Join(home, ".gitconfig"), "--", kv.key, kv.value); err != nil {
			return res, fmt.Errorf("set work's %s: %s", kv.key, lastLine(out, err))
		}
		*kv.set = true
	}
	return res, nil
}

// hasGitValue reports whether the user's own git config sets key: each
// file git reads for --global, by name (git 2.50 reads only ~/.gitconfig
// for --global once that exists), with the files they include.
func hasGitValue(ctx context.Context, run bundleRunner, env []string, home, key string) (bool, error) {
	for _, f := range []string{filepath.Join(home, ".config", "git", "config"), filepath.Join(home, ".gitconfig")} {
		out, err := run(ctx, env, home, "config", "--file", f, "--includes", "--get", key)
		var exit *exec.ExitError
		switch {
		case err == nil && strings.TrimSpace(string(out)) != "":
			return true, nil
		case err == nil, errors.As(err, &exit) && exit.ExitCode() == 1:
			// Not in this file (or empty, or no file).
		default:
			return false, fmt.Errorf("read work's %s: %s", key, lastLine(out, err))
		}
	}
	return false, nil
}

// teamIOGitIdentity is "team-io git-identity HOME", as work, the identity
// as JSON on stdin; the result as JSON.
func teamIOGitIdentity(home string, stdin io.Reader, stdout io.Writer) error {
	if err := ownHome(home); err != nil {
		return err
	}
	var in gitIdentity
	if err := json.NewDecoder(io.LimitReader(stdin, 4096)).Decode(&in); err != nil {
		return fmt.Errorf("the git identity: %w", err)
	}
	res, err := setWorkGitIdentity(context.Background(), bundleGit(gitIdentityGit), home, in.Name, in.Email)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(res)
}

// SetWorkGitIdentity writes the owner's git name and email into work's
// ~/.gitconfig, as work, where work has none of its own.
func (s *apiServer) SetWorkGitIdentity(ctx context.Context, req *agentv1.SetWorkGitIdentityRequest) (*agentv1.SetWorkGitIdentityResponse, error) {
	if err := CheckGitIdentity(req.GetName(), req.GetEmail()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	in, err := json.Marshal(gitIdentity{Name: req.GetName(), Email: req.GetEmail()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "the git identity: %v", err)
	}
	home := filepath.Join(s.cfg.HomeRoot, s.cfg.User)
	out, err := s.runTeamIO(ctx, []string{"git-identity", home}, strings.NewReader(string(in)))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "set work's git identity: %v", err)
	}
	var r GitIdentityResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &r); err != nil {
		return nil, status.Errorf(codes.Internal, "set work's git identity: %v", err)
	}
	return &agentv1.SetWorkGitIdentityResponse{NameSet: r.NameSet, EmailSet: r.EmailSet}, nil
}
