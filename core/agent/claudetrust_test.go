// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

func trustHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "builder")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func readClaudeJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return m
}

func trusted(m map[string]any, p string) bool {
	projects, _ := m["projects"].(map[string]any)
	entry, _ := projects[p].(map[string]any)
	return entry["hasTrustDialogAccepted"] == true
}

func noTemps(t *testing.T, home string) {
	t.Helper()
	ents, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".claude.json.") {
			t.Fatalf("left %s behind", e.Name())
		}
	}
}

// No file yet: a new one, 0600, with only the trust for the start folder.
func TestClaudeTrustNewFile(t *testing.T) {
	home := trustHome(t)
	clone := filepath.Join(home, "acme-api")
	if err := claudeTrust(home, clone, io.Discard); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(home, ".claude.json"))
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("mode %v, %v", fi.Mode(), err)
	}
	m := readClaudeJSON(t, home)
	if !trusted(m, clone) || len(m) != 1 || len(m["projects"].(map[string]any)) != 1 {
		t.Fatalf("wrote %v", m)
	}
	noTemps(t, home)
}

// An existing file (the agent's login state, other projects, this
// project's other settings) keeps every other key and value.
func TestClaudeTrustMergesWithoutClobbering(t *testing.T) {
	home := trustHome(t)
	clone := filepath.Join(home, "acme-api")
	before := `{
  "numStartups": 3,
  "oauthAccount": {"emailAddress": "x@example.com", "organizationUuid": "o-1"},
  "userID": "abc",
  "bigNumber": 12345678901234567890,
  "projects": {
    "/home/builder/other": {"hasTrustDialogAccepted": false, "allowedTools": ["Bash"]},
    "` + clone + `": {"allowedTools": [], "lastCost": 0.25, "hasTrustDialogAccepted": false}
  }
}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := claudeTrust(home, clone, io.Discard); err != nil {
		t.Fatal(err)
	}
	m := readClaudeJSON(t, home)
	if !trusted(m, clone) {
		t.Fatalf("not trusted: %v", m)
	}
	if trusted(m, "/home/builder/other") {
		t.Fatal("another project was trusted")
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	for _, keep := range []string{`"numStartups":3`, `"emailAddress":"x@example.com"`, `"userID":"abc"`,
		`"bigNumber":12345678901234567890`, `"lastCost":0.25`, `"allowedTools":["Bash"]`} {
		if !bytes.Contains(compactJSON(t, b), []byte(keep)) {
			t.Errorf("lost %s: %s", keep, b)
		}
	}
	if fi, _ := os.Lstat(filepath.Join(home, ".claude.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode())
	}
	noTemps(t, home)
}

func compactJSON(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// Already trusted: the file isn't rewritten.
func TestClaudeTrustAlreadySet(t *testing.T) {
	home := trustHome(t)
	body := `{"projects":{"` + home + `":{"hasTrustDialogAccepted":true}},"z":1}`
	p := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	if err := claudeTrust(home, home, io.Discard); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != body || !os.SameFile(before, after) {
		t.Fatalf("rewritten: %s", b)
	}
}

// A file Portenv can't parse, or one shaped differently, is left exactly
// as it is: Claude Code just asks, as before.
func TestClaudeTrustLeavesAnUnparsableFile(t *testing.T) {
	for _, body := range []string{
		`{"projects": {`,                // cut short
		`[]`,                            // not an object
		`{"projects": []}`,              // projects isn't an object
		`{"projects": {"HOME": "yes"}}`, // the entry isn't an object
		`{"a":1} {"b":2}`,               // two values
		`{"projects":{},"projects":{}}`, // a key twice
		"",                              // empty
		`{"a": NaN}`,
	} {
		home := trustHome(t)
		p := filepath.Join(home, ".claude.json")
		body = strings.ReplaceAll(body, "HOME", home)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		err := claudeTrust(home, home, io.Discard)
		if !errors.Is(err, errClaudeJSONUnparsable) {
			t.Errorf("%q: err = %v", body, err)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Errorf("%q changed to %q", body, b)
		}
		noTemps(t, home)
	}
}

// A planted link is never followed: not as .claude.json, not as the home.
func TestClaudeTrustNeverFollowsALink(t *testing.T) {
	home := trustHome(t)
	elsewhere := filepath.Join(t.TempDir(), "victim.json")
	if err := os.WriteFile(elsewhere, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if err := claudeTrust(home, home, io.Discard); err == nil {
		t.Fatal("wrote through a link")
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != `{"keep":true}` {
		t.Fatalf("the link's target changed: %s", b)
	}
	if fi, _ := os.Lstat(filepath.Join(home, ".claude.json")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}

	real := trustHome(t)
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	if err := claudeTrust(linked, linked, io.Discard); err == nil {
		t.Fatal("followed a linked home")
	}
	if _, err := os.Lstat(filepath.Join(real, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote into the linked home")
	}
}

// Only the agent's home or one folder directly in it.
func TestClaudeTrustOnlyTheStartFolder(t *testing.T) {
	home := trustHome(t)
	for _, p := range []string{
		"/", "/home/work/acme-api", filepath.Dir(home), filepath.Join(home, "a", "b"),
		filepath.Join(home, ".."), filepath.Join(home, "..", "work"), home + "/", "acme-api",
		filepath.Join(home, ".claude"), filepath.Join(home, "x\ny"), "",
	} {
		if err := claudeTrust(home, p, io.Discard); err == nil {
			t.Errorf("trusted %q", p)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused path still wrote the file")
	}
}

// A file over the limit is left alone.
func TestClaudeTrustTooBig(t *testing.T) {
	home := trustHome(t)
	big := `{"x":"` + strings.Repeat("a", maxClaudeJSON) + `"}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claudeTrust(home, home, io.Discard); err == nil {
		t.Fatal("rewrote a file over the limit")
	}
}

// At the start, the helper writes the trust as the agent for exactly the
// folder the runner starts in, only for a runner with a trust setting.
func TestStartTeamAgentWritesClaudeTrust(t *testing.T) {
	cases := []struct {
		name, setting string
		want          [][]string
	}{
		{"claude, no clone: its home", "claude", [][]string{{"claude-trust", "/home/builder", "/home/builder"}}},
		{"another runner: nothing", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, _, started, _ := newStartServer(t)
			var calls [][]string
			var uids []int
			s.runHomeIO = func(_ context.Context, uid int, _ []uint32, args []string, _ io.Reader) ([]byte, error) {
				calls = append(calls, slices.Clone(args))
				uids = append(uids, uid)
				return nil, nil
			}
			if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{
				AgentId: "builder", Argv: []string{"claude"}, AgentsMd: "x\n", HomeQuotaBytes: 2 << 30, TrustSetting: c.setting,
			}); err != nil {
				t.Fatal(err)
			}
			if !slices.EqualFunc(calls, c.want, slices.Equal) {
				t.Fatalf("calls %v, want %v", calls, c.want)
			}
			for _, u := range uids {
				if u != 2001 {
					t.Fatalf("ran as uid %d, not the agent's", u)
				}
			}
			if len(*started) != 1 {
				t.Fatal("the runner didn't start")
			}
		})
	}
}

// An unknown setting is refused before anything is made; a failed write
// never stops the start (Claude Code asks, as before).
func TestStartTeamAgentTrustSettingChecks(t *testing.T) {
	s, users, _, _, _ := newStartServer(t)
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{
		AgentId: "builder", Argv: []string{"claude"}, AgentsMd: "x\n", HomeQuotaBytes: 2 << 30, TrustSetting: "codex",
	}); err == nil || len(users.got) != 0 {
		t.Fatalf("unknown trust setting: %v (users %v)", err, users.got)
	}
	s, _, _, started, _ := newStartServer(t)
	s.runHomeIO = func(context.Context, int, []uint32, []string, io.Reader) ([]byte, error) {
		return nil, errClaudeJSONUnparsable
	}
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{
		AgentId: "builder", Argv: []string{"claude"}, AgentsMd: "x\n", HomeQuotaBytes: 2 << 30, TrustSetting: "claude",
	}); err != nil || len(*started) != 1 {
		t.Fatalf("a failed trust write stopped the start: %v", err)
	}
}

// team-io claude-trust runs claudeTrust.
func TestTeamIOClaudeTrust(t *testing.T) {
	home := trustHome(t)
	if err := TeamIO([]string{"claude-trust", home, home}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !trusted(readClaudeJSON(t, home), home) {
		t.Fatal("not trusted")
	}
	if err := TeamIO([]string{"claude-trust", home}, nil, io.Discard); err == nil {
		t.Fatal("accepted a missing path")
	}
}

// The runner starts in its clone when it has one, else in its home.
func TestRunnerStartFolder(t *testing.T) {
	if got := runnerStartFolder("/home/builder", "/home/builder/acme-api"); got != "/home/builder/acme-api" {
		t.Errorf("with a clone: %q", got)
	}
	if got := runnerStartFolder("/home/builder", ""); got != "/home/builder" {
		t.Errorf("without: %q", got)
	}
}
