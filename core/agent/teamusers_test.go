// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// fakeSystem answers the user and group commands init runs (getent, id,
// groupadd, useradd, usermod, userdel, groupdel) from in-memory tables, the
// way a box's /etc/passwd and /etc/group would.
type fakeSystem struct {
	users   map[string]int             // name → uid
	groups  map[string]int             // name → gid
	members map[string]map[string]bool // group → users with it as a supplementary group
	busy    map[string]bool            // userdel fails: the user has processes
	primary map[string]int             // user → primary gid
	calls   []string
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		users:   map[string]int{"root": 0, "work": 1000, "portenv-agent": 991},
		groups:  map[string]int{"root": 0, "work": 1000, "portenv-agent": 991, "portenv-sync": 990},
		members: map[string]map[string]bool{},
		busy:    map[string]bool{},
		primary: map[string]int{"root": 0, "work": 1000, "portenv-agent": 991},
	}
}

func (f *fakeSystem) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	missing := func() ([]byte, error) { return nil, errors.New("exit status 2") }
	lookup := func(table map[string]int, key string) (string, bool) {
		if n, err := strconv.Atoi(key); err == nil {
			for k, v := range table {
				if v == n {
					return k, true
				}
			}
			return "", false
		}
		_, ok := table[key]
		return key, ok
	}
	switch name {
	case "getent":
		table := f.users
		if args[0] == "group" {
			table = f.groups
		}
		if k, ok := lookup(table, args[1]); ok {
			return []byte(fmt.Sprintf("%s:x:%d:\n", k, table[k])), nil
		}
		return missing()
	case "id":
		user := args[len(args)-1]
		uid, ok := f.users[user]
		if !ok {
			return []byte("id: '" + user + "': no such user"), errors.New("exit status 1")
		}
		if args[0] == "-g" {
			return []byte(strconv.Itoa(f.primary[user]) + "\n"), nil
		}
		if args[0] == "-nG" {
			gs := []string{user}
			for g, m := range f.members {
				if m[user] {
					gs = append(gs, g)
				}
			}
			return []byte(strings.Join(gs, " ") + "\n"), nil
		}
		return []byte(strconv.Itoa(uid) + "\n"), nil
	case "groupadd":
		gid, _ := strconv.Atoi(args[1])
		g := args[2]
		if _, ok := f.groups[g]; ok {
			return []byte("groupadd: group '" + g + "' already exists"), errors.New("exit status 9")
		}
		f.groups[g] = gid
		return nil, nil
	case "useradd":
		u := args[len(args)-1]
		if _, ok := f.users[u]; ok {
			return []byte("useradd: user '" + u + "' already exists"), errors.New("exit status 9")
		}
		uid, _ := strconv.Atoi(args[slices.Index(args, "--uid")+1])
		f.users[u] = uid
		gid, _ := strconv.Atoi(args[slices.Index(args, "--gid")+1])
		f.primary[u] = gid
		if i := slices.Index(args, "--groups"); i >= 0 {
			for _, g := range strings.Split(args[i+1], ",") {
				if f.members[g] == nil {
					f.members[g] = map[string]bool{}
				}
				f.members[g][u] = true
			}
		}
		return nil, nil
	case "usermod": // usermod -a -G group user
		g, u := args[2], args[3]
		if f.members[g] == nil {
			f.members[g] = map[string]bool{}
		}
		f.members[g][u] = true
		return nil, nil
	case "userdel":
		u := args[len(args)-1]
		if f.busy[u] {
			return []byte("userdel: user " + u + " is currently used by process 4242"), errors.New("exit status 8")
		}
		delete(f.users, u)
		for _, m := range f.members {
			delete(m, u)
		}
		return nil, nil
	case "groupdel":
		delete(f.groups, args[len(args)-1])
		return nil, nil
	}
	return nil, fmt.Errorf("fakeSystem: unexpected command %q", call)
}

func (f *fakeSystem) ran(prefix string) bool {
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

type chownCall struct {
	path     string
	uid, gid int
}

// newTestTeam returns TeamUsers over a temporary /home and a fake system;
// chown is recorded, not performed (tests don't run as root).
func newTestTeam(t *testing.T) (*TeamUsers, *fakeSystem, *[]chownCall) {
	t.Helper()
	root := t.TempDir()
	sys := newFakeSystem()
	var chowns []chownCall
	team := &TeamUsers{
		Cfg: Config{User: "work", UID: 1000, HomeRoot: filepath.Join(root, "home")},
		Run: sys,
		Chown: func(path string, uid, gid int) error {
			chowns = append(chowns, chownCall{path, uid, gid})
			return nil
		},
		RunDir: filepath.Join(root, "run", "portenv-team"),
		fchown: func(f *os.File, uid, gid int) error {
			chowns = append(chowns, chownCall{f.Name(), uid, gid})
			return nil
		},
		teamDirACL: func(string, int) error { return nil },
	}
	if err := os.MkdirAll(filepath.Join(team.Cfg.HomeRoot, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	return team, sys, &chowns
}

func readStore(t *testing.T, team *TeamUsers) teamUsersFile {
	t.Helper()
	b, err := os.ReadFile(team.storePath())
	if err != nil {
		t.Fatal(err)
	}
	var f teamUsersFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAgentIDRules(t *testing.T) {
	for _, id := range []string{"builder", "lead", "a", "design-keeper", "r2", strings.Repeat("a", 32)} {
		if err := CheckAgentID(id, "work"); err != nil {
			t.Errorf("CheckAgentID(%q) = %v, want ok", id, err)
		}
	}
	for _, id := range []string{
		"", "Builder", "2lead", "-x", "x y", "../x", "x/y", "x.y", "x_y", "é",
		strings.Repeat("a", 33), "team", "work", "portenv", "portenv-agent", "portenvx",
		"lead\n", "lead\x00",
	} {
		err := CheckAgentID(id, "work")
		if err == nil {
			t.Errorf("CheckAgentID(%q) = nil, want an error", id)
			continue
		}
		if !errors.Is(err, ErrInvalidAgentID) && !errors.Is(err, ErrReservedAgentID) {
			t.Errorf("CheckAgentID(%q) = %v, want ErrInvalidAgentID or ErrReservedAgentID", id, err)
		}
	}
}

func TestEnsureMakesTheUserItsHomeAndItsRecord(t *testing.T) {
	team, sys, chowns := newTestTeam(t)
	got, err := team.Ensure(context.Background(), "builder")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(team.Cfg.HomeRoot, "builder")
	if got.UID != 2001 || got.Home != home || !got.Created {
		t.Fatalf("Ensure = %+v, want uid 2001, home %s, created", got, home)
	}
	if sys.users["builder"] != 2001 || sys.groups["builder"] != 2001 {
		t.Errorf("user and group: %d, %d; want 2001, 2001", sys.users["builder"], sys.groups["builder"])
	}
	if sys.groups["team"] != TeamGID || !sys.members["team"]["builder"] || !sys.members["team"]["work"] {
		t.Errorf("group team: gid %d, members %v; want %d with builder and work", sys.groups["team"], sys.members["team"], TeamGID)
	}
	if !sys.ran("useradd --uid 2001 --gid 2001 --groups team --no-create-home --home-dir " + home + " --shell /bin/bash builder") {
		t.Errorf("useradd not run as expected: %q", sys.calls)
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("home: %v, %v; want a 0700 directory", info, err)
	}
	if !slices.Contains(*chowns, chownCall{home, 2001, 2001}) {
		t.Errorf("home not given to 2001: %v", *chowns)
	}
	if info, err := os.Lstat(filepath.Join(team.Cfg.HomeRoot, "team")); err != nil || info.Mode()&os.ModeSetgid == 0 {
		t.Errorf("/home/team with the first agent: %v, %v", info, err)
	}
	f := readStore(t, team)
	if f.Version != 1 || len(f.Users) != 1 || f.Users[0] != (agentUserRecord{ID: "builder", UID: 2001}) {
		t.Errorf("users.json = %+v", f)
	}
	if info, err := os.Stat(team.storePath()); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("users.json mode: %v, %v; want 0644", info, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(team.storePath()), "*.tmp*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestEnsureTwiceChangesNothing(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	sys.calls = nil
	got, err := team.Ensure(ctx, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != 2001 || got.Created {
		t.Errorf("second Ensure = %+v, want uid 2001, not created", got)
	}
	if sys.ran("useradd") || sys.ran("groupadd") {
		t.Errorf("second Ensure added again: %q", sys.calls)
	}
}

func TestEnsureTakesTheLowestFreeUID(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	ctx := context.Background()
	sys.users["someone"] = 2001 // taken in the box, not by an agent
	sys.groups["other"] = 2002  // a group holds 2002
	got, err := team.Ensure(ctx, "lead")
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != 2003 {
		t.Errorf("uid %d, want 2003 (2001 and 2002 are taken)", got.UID)
	}
}

func TestTheTeamIsFullAtEight(t *testing.T) {
	team, _, _ := newTestTeam(t)
	ctx := context.Background()
	for i := range AgentUIDLast - AgentUIDFirst + 1 {
		if _, err := team.Ensure(ctx, fmt.Sprintf("agent%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := team.Ensure(ctx, "ninth"); !errors.Is(err, ErrTeamFull) {
		t.Fatalf("ninth agent: %v, want ErrTeamFull", err)
	}
}

func TestEnsureRefusesANameTheBoxAlreadyHas(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	ctx := context.Background()
	sys.users["postgres"] = 999
	sys.groups["docker"] = 998
	for _, id := range []string{"postgres", "docker", "root"} {
		if _, err := team.Ensure(ctx, id); !errors.Is(err, ErrReservedAgentID) {
			t.Errorf("Ensure(%q) = %v, want ErrReservedAgentID", id, err)
		}
	}
	if sys.ran("useradd") {
		t.Errorf("a user was added: %q", sys.calls)
	}
}

func TestEnsureRefusesAUserWhoseUIDMoved(t *testing.T) {
	team, sys, chowns := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	sys.users["builder"] = 1500 // someone changed it by hand
	*chowns = nil
	if _, err := team.Ensure(ctx, "builder"); err == nil {
		t.Fatal("Ensure accepted builder with uid 1500, recorded as 2001")
	}
	for _, c := range *chowns {
		if c.path == filepath.Join(team.Cfg.HomeRoot, "builder") {
			t.Errorf("builder's home chowned despite the mismatch: %v", c)
		}
	}
}

func TestRemoveKeepsTheHomeAndTheUID(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	ctx := context.Background()
	got, err := team.Ensure(ctx, "builder")
	if err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(got.Home, "notes.md")
	if err := os.WriteFile(note, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := team.Remove(ctx, "builder", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := sys.users["builder"]; ok {
		t.Error("builder is still a user")
	}
	if b, err := os.ReadFile(note); err != nil || string(b) != "keep me" {
		t.Fatalf("the home's files: %q, %v; want them kept", b, err)
	}
	f := readStore(t, team)
	if len(f.Users) != 1 || !f.Users[0].Removed || f.Users[0].UID != 2001 {
		t.Fatalf("users.json = %+v, want builder kept as removed with uid 2001", f)
	}
	// Its uid stays taken while its files exist.
	other, err := team.Ensure(ctx, "lead")
	if err != nil || other.UID == 2001 {
		t.Fatalf("lead = %+v, %v; want a uid other than 2001", other, err)
	}
	// Adding it back gives the same uid and the same files.
	back, err := team.Ensure(ctx, "builder")
	if err != nil || back.UID != 2001 {
		t.Fatalf("builder again = %+v, %v; want uid 2001", back, err)
	}
	if b, err := os.ReadFile(note); err != nil || string(b) != "keep me" {
		t.Errorf("the home's files after adding it back: %q, %v", b, err)
	}
}

func TestRemoveRefusesARunningAgent(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	sys.busy["builder"] = true
	if err := team.Remove(ctx, "builder", true); err == nil {
		t.Fatal("Remove succeeded while builder has processes")
	}
	if _, err := os.Stat(filepath.Join(team.Cfg.HomeRoot, "builder")); err != nil {
		t.Errorf("home touched after a failed remove: %v", err)
	}
	if f := readStore(t, team); f.Users[0].Removed {
		t.Errorf("recorded as removed after a failed remove: %+v", f)
	}
}

func TestRemoveWithItsFilesFreesTheUID(t *testing.T) {
	team, _, _ := newTestTeam(t)
	ctx := context.Background()
	got, err := team.Ensure(ctx, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(got.Home, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := team.Remove(ctx, "builder", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(got.Home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("home still there: %v", err)
	}
	if f := readStore(t, team); len(f.Users) != 0 {
		t.Errorf("users.json = %+v, want empty", f)
	}
	next, err := team.Ensure(ctx, "lead")
	if err != nil || next.UID != 2001 {
		t.Errorf("lead = %+v, %v; want the freed uid 2001", next, err)
	}
}

func TestDeletingFilesNeverFollowsASymlink(t *testing.T) {
	team, _, _ := newTestTeam(t)
	ctx := context.Background()
	got, err := team.Ensure(ctx, "builder")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "precious")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(got.Home); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, got.Home); err != nil {
		t.Fatal(err)
	}
	if err := team.Remove(ctx, "builder", true); err == nil {
		t.Error("Remove deleted through a symlinked home")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Errorf("the symlink's target was touched: %v", err)
	}
}

func TestEnsureRefusesASymlinkedHome(t *testing.T) {
	team, _, chowns := newTestTeam(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(team.Cfg.HomeRoot, "builder")); err != nil {
		t.Fatal(err)
	}
	if _, err := team.Ensure(context.Background(), "builder"); err == nil {
		t.Fatal("Ensure accepted a symlink as builder's home")
	}
	for _, c := range *chowns {
		if c.path == filepath.Join(team.Cfg.HomeRoot, "builder") {
			t.Errorf("chowned the symlink: %v", c)
		}
	}
}

func TestABrokenStoreIsNeverOverwritten(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	if err := os.MkdirAll(filepath.Dir(team.storePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(team.storePath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := team.Ensure(context.Background(), "builder"); err == nil {
		t.Fatal("Ensure worked over an unreadable users.json")
	}
	if b, _ := os.ReadFile(team.storePath()); string(b) != "{not json" {
		t.Errorf("users.json rewritten: %q", b)
	}
	if sys.ran("useradd") {
		t.Errorf("a user was added: %q", sys.calls)
	}
}

func TestRestoreBringsAgentsBackInANewBox(t *testing.T) {
	team, sys, chowns := newTestTeam(t)
	ctx := context.Background()
	for _, id := range []string{"lead", "builder", "reviewer"} {
		if _, err := team.Ensure(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := team.Remove(ctx, "reviewer", false); err != nil {
		t.Fatal(err)
	}

	// A new box from the same /home: /etc is the image's again.
	fresh := newFakeSystem()
	team.Run = fresh
	*chowns = nil
	team.Restore(ctx)
	if err := team.RestoreErr(); err != nil {
		t.Fatal(err)
	}
	if fresh.users["lead"] != 2001 || fresh.users["builder"] != 2002 {
		t.Errorf("users after restore: %v", fresh.users)
	}
	if _, ok := fresh.users["reviewer"]; ok {
		t.Error("a removed agent came back as a user")
	}
	if !fresh.members["team"]["work"] || !fresh.members["team"]["lead"] {
		t.Errorf("team members: %v", fresh.members["team"])
	}
	teamDir := filepath.Join(team.Cfg.HomeRoot, "team")
	info, err := os.Lstat(teamDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSetgid == 0 || info.Mode().Perm() != 0o770 {
		t.Fatalf("/home/team: %v, %v; want a setgid 0770 directory", info, err)
	}
	if !slices.Contains(*chowns, chownCall{teamDir, 1000, TeamGID}) {
		t.Errorf("/home/team not work:team: %v", *chowns)
	}
	if info, err := os.Stat(team.RunDir); err != nil || info.Mode().Perm() != 0o711 {
		t.Errorf("run dir: %v, %v; want 0711", info, err)
	}
	// The removed agent's uid is still taken.
	got, err := team.Ensure(ctx, "designer")
	if err != nil || got.UID != 2004 {
		t.Errorf("designer = %+v, %v; want 2004 (2003 still belongs to reviewer's files)", got, err)
	}
	_ = sys
}

// A workspace without a team keeps /home as it was: only work's home
// (images/toolbox-node/test.sh checks the same in a real box).
func TestRestoreWithNoTeamAddsNothingToHome(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	team.Restore(context.Background())
	if err := team.RestoreErr(); err != nil {
		t.Fatal(err)
	}
	if sys.ran("useradd") || sys.ran("groupadd") || sys.ran("usermod") {
		t.Errorf("users or groups changed with no team: %q", sys.calls)
	}
	entries, err := os.ReadDir(team.Cfg.HomeRoot)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"work"}) {
		t.Errorf("/home holds %v, want only work", names)
	}
	if _, err := os.Stat(team.storePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("users.json written with no team: %v", err)
	}
}

func TestRestoreFailureWaitsForTheNextEnsure(t *testing.T) {
	team, _, _ := newTestTeam(t)
	if err := os.MkdirAll(filepath.Dir(team.storePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(team.storePath(), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	team.Restore(context.Background()) // never stops the box
	if team.RestoreErr() == nil {
		t.Fatal("RestoreErr = nil after an unreadable users.json")
	}
	if _, err := team.Ensure(context.Background(), "builder"); err == nil {
		t.Error("Ensure worked after a failed restore")
	}
}

func TestInitAnswersAgentUserCalls(t *testing.T) {
	team, _, _ := newTestTeam(t)
	s := &server{readiness: NewReadiness(), team: team}
	ctx := context.Background()
	got, err := s.EnsureAgentUser(ctx, &agentv1.EnsureAgentUserRequest{AgentId: "builder"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetUid() != 2001 || got.GetHome() != filepath.Join(team.Cfg.HomeRoot, "builder") || !got.GetCreated() {
		t.Errorf("EnsureAgentUser = %v", got)
	}
	for id, want := range map[string]codes.Code{"Bad": codes.InvalidArgument, "root": codes.InvalidArgument} {
		if _, err := s.EnsureAgentUser(ctx, &agentv1.EnsureAgentUserRequest{AgentId: id}); status.Code(err) != want {
			t.Errorf("EnsureAgentUser(%q) = %v, want %v", id, err, want)
		}
	}
	if _, err := s.RemoveAgentUser(ctx, &agentv1.RemoveAgentUserRequest{AgentId: "nobody"}); status.Code(err) != codes.NotFound {
		t.Errorf("RemoveAgentUser(nobody) = %v, want NotFound", err)
	}
	if _, err := s.RemoveAgentUser(ctx, &agentv1.RemoveAgentUserRequest{AgentId: "builder"}); err != nil {
		t.Errorf("RemoveAgentUser(builder) = %v", err)
	}
	if _, err := os.Stat(got.GetHome()); err != nil {
		t.Errorf("RemoveAgentUser without delete_files touched the home: %v", err)
	}
}

func TestOnlyInitServesAgentUsers(t *testing.T) {
	ctx := context.Background()
	noTeam := &server{readiness: NewReadiness()}
	if _, err := noTeam.EnsureAgentUser(ctx, &agentv1.EnsureAgentUserRequest{AgentId: "builder"}); status.Code(err) != codes.Unavailable {
		t.Errorf("without a team: %v, want Unavailable", err)
	}
	// The channel's server (the API process, uid 991) never makes users.
	if _, err := (&apiServer{}).EnsureAgentUser(ctx, &agentv1.EnsureAgentUserRequest{AgentId: "builder"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("the channel's server: %v, want Unimplemented", err)
	}
	if _, err := (&apiServer{}).RemoveAgentUser(ctx, &agentv1.RemoveAgentUserRequest{AgentId: "builder"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("the channel's server: %v, want Unimplemented", err)
	}
}

// R-0260 item 7: a team group with another gid is refused, never used.
func TestATeamGroupWithAnotherGIDIsRefused(t *testing.T) {
	team, sys, chowns := newTestTeam(t)
	sys.groups["team"] = 1999
	if _, err := team.Ensure(context.Background(), "builder"); err == nil || !strings.Contains(err.Error(), "1999") {
		t.Fatalf("Ensure = %v, want a refusal naming gid 1999", err)
	}
	for _, c := range *chowns {
		if c.gid == TeamGID {
			t.Errorf("chowned to %d while team is 1999: %v", TeamGID, c)
		}
	}
	if sys.ran("useradd") {
		t.Errorf("a user was added: %q", sys.calls)
	}
	// And gid 2000 held by another group.
	team2, sys2, _ := newTestTeam(t)
	sys2.groups["other"] = TeamGID
	if _, err := team2.Ensure(context.Background(), "builder"); err == nil {
		t.Error("Ensure made team while gid 2000 belongs to another group")
	}
}

// R-0260 item 8: the primary gid and team membership are checked too.
func TestEnsureChecksTheGroupsToo(t *testing.T) {
	team, sys, _ := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	sys.primary["builder"] = 1000 // someone made work's group its primary
	if _, err := team.Ensure(ctx, "builder"); err == nil {
		t.Error("Ensure accepted builder with primary gid 1000")
	}
	sys.primary["builder"] = 2001
	delete(sys.members["team"], "builder")
	if _, err := team.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	if !sys.members["team"]["builder"] {
		t.Error("builder's team membership wasn't put back")
	}
}

// R-0260 item 9: the store's folder is root's 0755, and symlinks in the
// way are refused.
func TestTheStoresFolderAndLinksInTheWay(t *testing.T) {
	team, _, _ := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(team.Cfg.HomeRoot, ".portenv"), filepath.Dir(team.storePath())} {
		info, err := os.Lstat(d)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
			t.Errorf("%s: %v, %v; want a 0755 folder", d, info, err)
		}
	}
	for name, setup := range map[string]func(t *testing.T, tu *TeamUsers){
		"home root": func(t *testing.T, tu *TeamUsers) {
			real := tu.Cfg.HomeRoot + "-real"
			if err := os.Rename(tu.Cfg.HomeRoot, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, tu.Cfg.HomeRoot); err != nil {
				t.Fatal(err)
			}
		},
		".portenv": func(t *testing.T, tu *TeamUsers) {
			if err := os.Symlink(t.TempDir(), filepath.Join(tu.Cfg.HomeRoot, ".portenv")); err != nil {
				t.Fatal(err)
			}
		},
		"run dir": func(t *testing.T, tu *TeamUsers) {
			if err := os.MkdirAll(filepath.Dir(tu.RunDir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), tu.RunDir); err != nil {
				t.Fatal(err)
			}
		},
	} {
		tu, _, _ := newTestTeam(t)
		setup(t, tu)
		_, ensureErr := tu.Ensure(ctx, "builder")
		tu.Restore(ctx)
		restoreErr := tu.RestoreErr()
		switch name {
		case "home root":
			if ensureErr == nil || restoreErr == nil {
				t.Errorf("home root as a symlink: Ensure %v, Restore %v; want both refused", ensureErr, restoreErr)
			}
		case ".portenv":
			if ensureErr == nil {
				t.Error(".portenv as a symlink: Ensure accepted it")
			}
		case "run dir":
			if restoreErr == nil {
				t.Error("the run dir as a symlink: Restore accepted it")
			}
		}
	}
}

// The agent's run folder is made by init, as root, at every Ensure
// (ADR 0022 §1; the owner demo, 2026-10-18: the helper couldn't make it).
func TestEnsureMakesTheAgentsRunFolder(t *testing.T) {
	ctx := context.Background()
	team, _, chowns := newTestTeam(t)
	got, err := team.Ensure(ctx, "builder")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(team.RunDir, "builder")
	if got.RunDir != base || got.TmuxDir != filepath.Join(base, "tmux") || got.TmpDir != filepath.Join(base, "tmp") {
		t.Fatalf("Ensure = %+v, want the run folder %s", got, base)
	}
	for path, want := range map[string]os.FileMode{
		team.RunDir: os.ModeDir | 0o711,
		base:        os.ModeDir | os.ModeSetgid | 0o710,
		got.TmuxDir: os.ModeDir | 0o700,
		got.TmpDir:  os.ModeDir | 0o700,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if info.Mode() != want {
			t.Errorf("%s: mode %v, want %v", path, info.Mode(), want)
		}
	}
	for _, want := range []chownCall{
		{base, ServeUID, 2001},
		{got.TmuxDir, 2001, 2001},
		{got.TmpDir, 2001, 2001},
	} {
		if !slices.Contains(*chowns, want) {
			t.Errorf("missing chown %v in %v", want, *chowns)
		}
	}
	// Lost with /run when the box stops, and made again at the next start.
	if err := os.RemoveAll(team.RunDir); err != nil {
		t.Fatal(err)
	}
	if _, err := team.Ensure(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(got.TmpDir); err != nil || info.Mode() != os.ModeDir|0o700 {
		t.Errorf("tmp after /run was lost: %v, %v", info, err)
	}
}

// A link in place of any of the agent's run folders is refused, never
// followed or chowned.
func TestEnsureRefusesALinkedRunFolder(t *testing.T) {
	for _, rel := range []string{"builder", "builder/tmux", "builder/tmp"} {
		t.Run(rel, func(t *testing.T) {
			team, _, chowns := newTestTeam(t)
			target := t.TempDir()
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(team.RunDir, rel)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := team.Ensure(context.Background(), "builder"); err == nil {
				t.Fatalf("Ensure accepted %s as a link", rel)
			}
			for _, c := range *chowns {
				if c.path == link || strings.HasPrefix(c.path, target) {
					t.Errorf("chowned through the link: %v", c)
				}
			}
			if info, err := os.Stat(target); err != nil || info.Mode() != before.Mode() {
				t.Errorf("the link's target changed: %v, %v", info, err)
			}
		})
	}
}
