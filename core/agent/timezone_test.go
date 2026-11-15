// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// fakeZoneinfo is an image's tz database with a few zones, one of them a
// link inside it and one a link leaving it.
func fakeZoneinfo(t *testing.T) (etc, zoneinfo string) {
	t.Helper()
	root := t.TempDir()
	etc, zoneinfo = filepath.Join(root, "etc"), filepath.Join(root, "zoneinfo")
	for _, d := range []string{etc, filepath.Join(zoneinfo, "Europe"), filepath.Join(zoneinfo, "Etc")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"UTC", "Europe/Paris", "Etc/GMT-2"} {
		if err := os.WriteFile(filepath.Join(zoneinfo, f), []byte("TZif2"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("Paris", filepath.Join(zoneinfo, "Europe", "Monaco")); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(zoneinfo, "Europe", "Away")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(zoneinfo, "Europe", "Folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	return etc, zoneinfo
}

func linkOf(t *testing.T, etc string) string {
	t.Helper()
	got, err := os.Readlink(filepath.Join(etc, "localtime"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Init links /etc/localtime to a zone that's in the image's tz database,
// and anything else to UTC.
func TestLinkLocaltime(t *testing.T) {
	cases := []struct{ zone, want string }{
		{"Europe/Paris", "Europe/Paris"},
		{"Etc/GMT-2", "Etc/GMT-2"},
		{"Europe/Monaco", "Europe/Monaco"}, // a link inside the database
		{"UTC", "UTC"},
		{"Mars/Olympus", "UTC"},  // not in the database
		{"Europe/Away", "UTC"},   // a link leaving it
		{"Europe/Folder", "UTC"}, // not a file
		{"Local", "UTC"},         // never "Local"
		{"../secret", "UTC"},     // traversal
		{"Europe/../../secret", "UTC"},
		{"/etc/passwd", "UTC"},
		{"Europe/Paris; rm -rf /", "UTC"},
		{"", "UTC"},
	}
	for _, c := range cases {
		t.Run(c.zone, func(t *testing.T) {
			etc, zoneinfo := fakeZoneinfo(t)
			got, err := linkLocaltime(etc, zoneinfo, c.zone)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("applied %q, want %q", got, c.want)
			}
			if l := linkOf(t, etc); l != filepath.Join(zoneinfo, c.want) {
				t.Fatalf("/etc/localtime -> %q, want %q", l, filepath.Join(zoneinfo, c.want))
			}
		})
	}
}

// A new start replaces the link at once (the owner travelled), and never
// leaves a temporary link behind.
func TestLinkLocaltimeReplaces(t *testing.T) {
	etc, zoneinfo := fakeZoneinfo(t)
	// The image's own /etc/localtime is a link to UTC, or a plain file.
	if err := os.WriteFile(filepath.Join(etc, "localtime"), []byte("TZif2"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, z := range []string{"Europe/Paris", "Etc/GMT-2"} {
		if _, err := linkLocaltime(etc, zoneinfo, z); err != nil {
			t.Fatal(err)
		}
		if l := linkOf(t, etc); l != filepath.Join(zoneinfo, z) {
			t.Fatalf("/etc/localtime -> %q after %s", l, z)
		}
	}
	ents, err := os.ReadDir(etc)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "localtime" {
		t.Fatalf("etc holds %v", ents)
	}
}

// With no tz database at all (an older image), the clock stays UTC and
// /etc/localtime is left as it is.
func TestLinkLocaltimeNoDatabase(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.Mkdir(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := linkLocaltime(etc, filepath.Join(root, "none"), "Europe/Paris")
	if err != nil || got != "UTC" {
		t.Fatalf("got %q, %v; want UTC", got, err)
	}
	if _, err := os.Lstat(filepath.Join(etc, "localtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("/etc/localtime made without a database: %v", err)
	}
}

// The API process checks the name's shape, relays to init, and keeps
// init's answer for every later shell and agent runner.
func TestSetTimeZoneRelaysAndKeepsTheZone(t *testing.T) {
	t.Cleanup(func() { setWorkspaceZone("") })
	var asked []string
	init := func(_ context.Context, zone string) (string, error) {
		asked = append(asked, zone)
		if zone == "Mars/Olympus" {
			return "UTC", nil
		}
		return zone, nil
	}
	s := &apiServer{setLocaltime: init}
	for _, c := range []struct{ zone, ask, want string }{
		{"Europe/Paris", "Europe/Paris", "Europe/Paris"},
		{"Mars/Olympus", "Mars/Olympus", "UTC"}, // init doesn't know it
		{"../../etc/shadow", "UTC", "UTC"},      // never reaches init
		{"Local", "UTC", "UTC"},
		{"Europe/Paris\nX=1", "UTC", "UTC"},
	} {
		asked = nil
		r, err := s.SetTimeZone(context.Background(), &agentv1.SetTimeZoneRequest{Zone: c.zone})
		if err != nil {
			t.Fatalf("%q: %v", c.zone, err)
		}
		if r.GetZone() != c.want || !slices.Equal(asked, []string{c.ask}) {
			t.Errorf("%q: applied %q (asked init %v), want %q (asked %q)", c.zone, r.GetZone(), asked, c.want, c.ask)
		}
		if got := zoneEnv(); !slices.Equal(got, []string{"TZ=" + c.want}) {
			t.Errorf("%q: zoneEnv = %v", c.zone, got)
		}
	}
}

// When init fails, the zone isn't changed and the error says so.
func TestSetTimeZoneInitFails(t *testing.T) {
	t.Cleanup(func() { setWorkspaceZone("") })
	setWorkspaceZone("Europe/Paris")
	s := &apiServer{setLocaltime: func(context.Context, string) (string, error) { return "", errors.New("read-only file system") }}
	if _, err := s.SetTimeZone(context.Background(), &agentv1.SetTimeZoneRequest{Zone: "Asia/Tokyo"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	if got := zoneEnv(); !slices.Equal(got, []string{"TZ=Europe/Paris"}) {
		t.Fatalf("zoneEnv = %v", got)
	}
	if _, err := (&apiServer{}).SetTimeZone(context.Background(), &agentv1.SetTimeZoneRequest{Zone: "UTC"}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("without init: %v", err)
	}
}

// Before any SetTimeZone, nothing is added: the image's own clock.
func TestZoneEnvUnset(t *testing.T) {
	setWorkspaceZone("")
	if got := zoneEnv(); len(got) != 0 {
		t.Fatalf("zoneEnv = %v", got)
	}
}

// work's shells get TZ.
func TestEnvForHasTheZone(t *testing.T) {
	t.Cleanup(func() { setWorkspaceZone("") })
	setWorkspaceZone("Europe/Paris")
	env := envFor(DefaultConfig())
	if !slices.Contains(env, "TZ=Europe/Paris") || !slices.Contains(env, "HOME=/home/work") {
		t.Fatalf("env = %v", env)
	}
}

// Every agent's runner gets TZ.
func TestStartTeamAgentPassesTheZone(t *testing.T) {
	t.Cleanup(func() { setWorkspaceZone("") })
	setWorkspaceZone("America/New_York")
	s, _, _, started, _ := newStartServer(t)
	if _, err := s.StartTeamAgent(context.Background(), &agentv1.StartTeamAgentRequest{
		AgentId: "builder", Argv: []string{"claude"}, AgentsMd: "x\n", HomeQuotaBytes: 2 << 30,
	}); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 || !slices.Contains((*started)[0].Env, "TZ=America/New_York") {
		t.Fatalf("started %+v", *started)
	}
}

// Init's SetTimeZone, as root, uses its fixed paths.
func TestInitSetTimeZone(t *testing.T) {
	etc, zoneinfo := fakeZoneinfo(t)
	s := &server{etcDir: etc, zoneinfoDir: zoneinfo}
	r, err := s.SetTimeZone(context.Background(), &agentv1.SetTimeZoneRequest{Zone: "Europe/Paris"})
	if err != nil || r.GetZone() != "Europe/Paris" {
		t.Fatalf("%v, %v", r, err)
	}
	if l := linkOf(t, etc); l != filepath.Join(zoneinfo, "Europe/Paris") {
		t.Fatalf("link %q", l)
	}
}
