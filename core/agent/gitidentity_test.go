// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// workGitConfig reads key as work's git does outside a repository
// (~/.gitconfig and ~/.config/git/config), or "" when it's unset.
func workGitConfig(t *testing.T, home, key string) string {
	t.Helper()
	cmd := exec.Command(needGit(t), "config", "--includes", "--get", key) // #nosec G204 -- the test's own check
	cmd.Dir = home                                                        // not a repository: the user's files only
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Findings 21 and 22: a new workspace's git as work has the owner's name
// and email from the Mac, so git merge and git commit work.
func TestWorkGetsTheMacGitIdentity(t *testing.T) {
	home := t.TempDir()
	got, err := setWorkGitIdentity(context.Background(), bundleGit(needGit(t)), home, "Example Person", "person@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !got.NameSet || !got.EmailSet {
		t.Fatalf("%+v", got)
	}
	if n, e := workGitConfig(t, home, "user.name"), workGitConfig(t, home, "user.email"); n != "Example Person" || e != "person@example.com" {
		t.Fatalf("work's git identity: %q <%q>", n, e)
	}
	// The next open sets nothing again.
	got, err = setWorkGitIdentity(context.Background(), bundleGit(needGit(t)), home, "Other Person", "other@example.com")
	if err != nil || got.NameSet || got.EmailSet {
		t.Fatalf("%+v %v", got, err)
	}
	if n := workGitConfig(t, home, "user.name"); n != "Example Person" {
		t.Fatalf("work's name was replaced: %q", n)
	}
}

// What the owner set in the workspace is never overwritten: in ~/.gitconfig
// or in ~/.config/git/config, and each value on its own.
func TestWorksOwnGitIdentityIsKept(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Set In The Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := setWorkGitIdentity(context.Background(), bundleGit(needGit(t)), home, "Example Person", "person@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.NameSet || !got.EmailSet {
		t.Fatalf("%+v", got)
	}
	if n, e := workGitConfig(t, home, "user.name"), workGitConfig(t, home, "user.email"); n != "Set In The Box" || e != "person@example.com" {
		t.Fatalf("work's git identity: %q <%q>", n, e)
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, ".config", "git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, ".config", "git", "config"), []byte("[user]\n\temail = box@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = setWorkGitIdentity(context.Background(), bundleGit(needGit(t)), xdg, "Example Person", "person@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !got.NameSet || got.EmailSet {
		t.Fatalf("%+v", got)
	}
	if e := workGitConfig(t, xdg, "user.email"); e != "box@example.com" {
		t.Fatalf("work's email was replaced: %q", e)
	}
}

// A config git can't read is left alone, never written over.
func TestABrokenGitConfigIsntWrittenOver(t *testing.T) {
	home := t.TempDir()
	broken := []byte("[user\n\tname = half\n")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setWorkGitIdentity(context.Background(), bundleGit(needGit(t)), home, "Example Person", "person@example.com"); err == nil {
		t.Fatal("a broken config was written")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".gitconfig")); !bytes.Equal(b, broken) {
		t.Fatalf("the config changed: %q", b)
	}
}

// Only a sane name and email are written: one line each, no control or
// bidi characters, no angle brackets, bounded.
func TestCheckGitIdentity(t *testing.T) {
	good := [][2]string{
		{"Example Person", "person@example.com"},
		{"Zoë Ångström", "12345+example@users.noreply.github.com"},
		{"", "person@example.com"}, // one of the two is enough
		{"Example Person", ""},
	}
	for _, g := range good {
		if err := CheckGitIdentity(g[0], g[1]); err != nil {
			t.Errorf("%q <%q>: %v", g[0], g[1], err)
		}
	}
	bad := [][2]string{
		{"", ""},
		{"Example\nPerson", "person@example.com"},
		{"Example Person", "person@example.com\n[core]\n\tpager = sh"},
		{"Example\rPerson", ""},
		{"Example\tPerson", ""},
		{"Example\x1b[31mPerson", ""},
		{"Example\u202ePerson", ""},
		{"Example\u0085Person", ""},
		{"Example <evil@example.com>", ""},
		{"", "<person@example.com>"},
		{" Example Person", ""},
		{strings.Repeat("x", 129), ""},
		{"", strings.Repeat("x", 250) + "@example.com"},
		{"Example \xff Person", ""},
	}
	for _, b := range bad {
		if err := CheckGitIdentity(b[0], b[1]); err == nil {
			t.Errorf("%q <%q> passed", b[0], b[1])
		}
	}
}

// The RPC runs team-io as work and refuses a bad identity before it does.
func TestSetWorkGitIdentityRPC(t *testing.T) {
	needGit(t)
	s, root := newBoardServer(t)
	var gotArgs []string
	var gotIn []byte
	s.runTeamIO = func(_ context.Context, args []string, stdin io.Reader) ([]byte, error) {
		gotArgs = args
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(stdin); err != nil {
			return nil, err
		}
		gotIn = buf.Bytes()
		return []byte(`{"name_set":true,"email_set":false}` + "\n"), nil
	}
	r, err := s.SetWorkGitIdentity(context.Background(), &agentv1.SetWorkGitIdentityRequest{Name: "Example Person", Email: "person@example.com"})
	if err != nil || !r.GetNameSet() || r.GetEmailSet() {
		t.Fatalf("%v %v", r, err)
	}
	if want := []string{"git-identity", filepath.Join(root, "work")}; strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("team-io %q, want %q", gotArgs, want)
	}
	var in gitIdentity
	if err := json.Unmarshal(gotIn, &in); err != nil || in.Name != "Example Person" || in.Email != "person@example.com" {
		t.Fatalf("stdin %q: %v", gotIn, err)
	}
	gotArgs = nil
	if _, err := s.SetWorkGitIdentity(context.Background(), &agentv1.SetWorkGitIdentityRequest{Name: "a\nb"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a bad name: %v", err)
	}
	if gotArgs != nil {
		t.Fatal("team-io ran for a bad name")
	}
}

// team-io git-identity, as work: the identity on stdin, checked again.
func TestTeamIOGitIdentity(t *testing.T) {
	needGit(t)
	home := t.TempDir()
	var out bytes.Buffer
	gitIdentityGit = needGit(t)
	t.Cleanup(func() { gitIdentityGit = agentGitPath })
	in := strings.NewReader(`{"name":"Example Person","email":"person@example.com"}`)
	if err := TeamIO([]string{"git-identity", home}, in, &out); err != nil {
		t.Fatal(err)
	}
	var r GitIdentityResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || !r.NameSet || !r.EmailSet {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := TeamIO([]string{"git-identity", home}, strings.NewReader(`{"name":"a\nb"}`), &out); err == nil {
		t.Fatal("a bad name passed team-io")
	}
}
