// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

func needMacGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	return git
}

// macHome is a Mac user's home with this global git config.
func macHome(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	if config != "" {
		if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// Findings 21 and 22: portenvd reads the owner's git name and email from
// the Mac's global config, with a clean environment: nothing of its own
// environment (another config, an author override) changes what's read.
func TestTheMacGitIdentityIsReadWithACleanEnvironment(t *testing.T) {
	git := needMacGit(t)
	home := macHome(t, "[user]\n\tname = Example Person\n\temail = person@example.com\n")
	other := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(other, []byte("[user]\n\tname = Not The Owner\n\temail = other@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", other)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "Injected")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	name, email := readMacGitIdentity(context.Background(), git, home)
	if name != "Example Person" || email != "person@example.com" {
		t.Fatalf("read %q <%q>", name, email)
	}
}

// A Mac with no git identity, or one that isn't fit to write, gives
// nothing; each value is checked on its own.
func TestABadOrMissingMacGitIdentityGivesNothing(t *testing.T) {
	git := needMacGit(t)
	for _, c := range []struct {
		config, name, email string
	}{
		{"", "", ""},
		{"[user]\n\temail = person@example.com\n", "", "person@example.com"},
		{"[user]\n\tname = \"Example\\tPerson\"\n\temail = person@example.com\n", "", "person@example.com"},
		{"[user]\n\tname = Example Person\n\temail = \"<x@example.com>\"\n", "Example Person", ""},
		{"[user]\n\tname = " + strings.Repeat("x", 200) + "\n", "", ""},
		{"[user\n", "", ""},
	} {
		name, email := readMacGitIdentity(context.Background(), git, macHome(t, c.config))
		if name != c.name || email != c.email {
			t.Errorf("%q: read %q <%q>, want %q <%q>", c.config, name, email, c.name, c.email)
		}
	}
}

// No git on the Mac is no identity, and no git means no prompt: the
// Command Line Tools' /usr/bin/git stub, which offers to install them, is
// run only when they're installed.
func TestMacGitIsFoundWithoutAnInstallPrompt(t *testing.T) {
	dir := t.TempDir()
	brew := filepath.Join(dir, "brew-git")
	if err := os.WriteFile(brew, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := findMacGit([]string{filepath.Join(dir, "missing"), brew}, func() bool { t.Fatal("asked about the tools"); return false }); got != brew {
		t.Fatalf("found %q", got)
	}
	if got := findMacGit([]string{filepath.Join(dir, "missing"), "/usr/bin/git"}, func() bool { return false }); got != "" {
		t.Fatalf("the stub was chosen without the tools: %q", got)
	}
	if got := findMacGit([]string{"/usr/bin/git"}, func() bool { return true }); got != "/usr/bin/git" {
		t.Fatalf("with the tools: %q", got)
	}
}

// fakeIdentityAgent records SetWorkGitIdentity calls.
type fakeIdentityAgent struct {
	agentv1.UnimplementedAgentServiceServer
	mu    sync.Mutex
	calls []*agentv1.SetWorkGitIdentityRequest
}

func (f *fakeIdentityAgent) SetWorkGitIdentity(_ context.Context, r *agentv1.SetWorkGitIdentityRequest) (*agentv1.SetWorkGitIdentityResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	return &agentv1.SetWorkGitIdentityResponse{NameSet: r.GetName() != "", EmailSet: r.GetEmail() != ""}, nil
}

func identityAgent(t *testing.T) (*fakeIdentityAgent, agentv1.AgentServiceClient) {
	t.Helper()
	fake := &fakeIdentityAgent{}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return fake, agentv1.NewAgentServiceClient(conn)
}

// At each open, the Mac's identity goes to the helper (which writes it
// only where work has none); with none on the Mac, nothing is sent. The log
// says what happened, never the name or address.
func TestEachOpenSharesTheMacGitIdentity(t *testing.T) {
	fake, client := identityAgent(t)
	var logs bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&logs, nil))}
	s.macGitIdentity = func(context.Context) (string, string) { return "Example Person", "person@example.com" }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.shareGitIdentity(ctx, "box-1", client)
	if len(fake.calls) != 1 || fake.calls[0].GetName() != "Example Person" || fake.calls[0].GetEmail() != "person@example.com" {
		t.Fatalf("calls %v", fake.calls)
	}
	s.macGitIdentity = func(context.Context) (string, string) { return "", "" }
	s.shareGitIdentity(ctx, "box-1", client)
	if len(fake.calls) != 1 {
		t.Fatalf("called with no identity: %v", fake.calls)
	}
	if strings.Contains(logs.String(), "Example Person") || strings.Contains(logs.String(), "person@example.com") {
		t.Fatalf("the identity is in the log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "git identity") {
		t.Fatalf("nothing logged:\n%s", logs.String())
	}
	// No helper channel: nothing to do, and the open goes on.
	s.macGitIdentity = func(context.Context) (string, string) { return "Example Person", "" }
	s.shareGitIdentity(ctx, "box-1", nil)
}
