// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// newHandoffServer has builder (2001) and lead (2002), with homes under a
// temporary /home, and runs team-io in this process, recording each uid.
func newHandoffServer(t *testing.T) (*apiServer, string, *[]int) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".portenv", "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".portenv", "team", "users.json"),
		[]byte(`{"version":1,"users":[{"id":"builder","uid":2001},{"id":"lead","uid":2002}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"builder/design/v2/parts", "lead", "work/shop/exports"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for f, c := range map[string]string{
		"builder/design/v2/cover.txt":     "cover",
		"builder/design/v2/parts/one.txt": "part one",
		"work/shop/exports/report.txt":    "report",
	} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var uids []int
	s := &apiServer{
		cfg: Config{User: "work", UID: 1000, HomeRoot: root},
		runHomeIO: func(_ context.Context, uid int, _ []uint32, args []string, stdin io.Reader) ([]byte, error) {
			uids = append(uids, uid)
			var out bytes.Buffer
			err := TeamIO(args, stdin, &out)
			return out.Bytes(), err
		},
		now: func() time.Time { return time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC) },
	}
	return s, root, &uids
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestAHandoffLandsInTheRecipientsInbox(t *testing.T) {
	s, root, uids := newHandoffServer(t)
	r, err := s.HandoffFiles(context.Background(), &agentv1.HandoffFilesRequest{
		FromAgent: "builder", ToAgent: "lead", SourcePath: "/home/builder/design/v2", RecipientQuotaBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := "inbox/builder/20261018T093000Z/v2"
	want := map[string]string{dest + "/cover.txt": "cover", dest + "/parts/one.txt": "part one"}
	if len(r.GetFiles()) != len(want) {
		t.Fatalf("files = %v", r.GetFiles())
	}
	for _, f := range r.GetFiles() {
		c, ok := want[f.GetPath()]
		if !ok || f.GetSha256() != sum(c) || f.GetSize() != int64(len(c)) {
			t.Errorf("file %v", f)
		}
		if b, err := os.ReadFile(filepath.Join(root, "lead", f.GetPath())); err != nil || string(b) != c {
			t.Errorf("%s in lead's home: %q, %v", f.GetPath(), b, err)
		}
	}
	// Read as the sender, written as the recipient.
	if fmt.Sprint(*uids) != "[2001 2002]" {
		t.Errorf("ran as %v, want [2001 2002]", *uids)
	}
	// From the project, one file.
	r, err = s.HandoffFiles(context.Background(), &agentv1.HandoffFilesRequest{
		FromAgent: "builder", ToAgent: "lead", SourcePath: "/home/work/shop/exports/report.txt", RecipientQuotaBytes: 1 << 20,
	})
	if err != nil || len(r.GetFiles()) != 1 || r.GetFiles()[0].GetSha256() != sum("report") {
		t.Errorf("from the project: %v, %v", r, err)
	}
}

func TestAHandoffRefusesLinksAndStrayPaths(t *testing.T) {
	s, root, _ := newHandoffServer(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "work", "secret"), []byte("owner's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "work", ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "work", ".ssh", "id_ed25519"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "work", "secret"), filepath.Join(root, "builder", "design", "v2", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "work"), filepath.Join(root, "builder", "w")); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"a link inside":     "/home/builder/design/v2",
		"a link on the way": "/home/builder/w/secret",
		"another's home":    "/home/lead/x",
		"work's own":        "/home/work/secret",
		"work's dotfiles":   "/home/work/.ssh/id_ed25519",
		"dot-dot":           "/home/builder/../work/secret",
		"outside":           "/etc/passwd",
		"relative":          "design/v2",
	} {
		_, err := s.HandoffFiles(ctx, &agentv1.HandoffFilesRequest{FromAgent: "builder", ToAgent: "lead", SourcePath: src, RecipientQuotaBytes: 1 << 20})
		if err == nil {
			t.Errorf("%s: handed over", name)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "lead")); len(entries) != 0 {
		t.Errorf("something landed in lead's home: %v", entries)
	}
	for _, req := range []*agentv1.HandoffFilesRequest{
		{FromAgent: "builder", ToAgent: "builder", SourcePath: "/home/builder/design", RecipientQuotaBytes: 1 << 20},
		{FromAgent: "builder", ToAgent: "nobody", SourcePath: "/home/builder/design", RecipientQuotaBytes: 1 << 20},
		{FromAgent: "../x", ToAgent: "lead", SourcePath: "/home/builder/design", RecipientQuotaBytes: 1 << 20},
	} {
		if _, err := s.HandoffFiles(ctx, req); status.Code(err) == codes.OK {
			t.Errorf("%v: accepted", req)
		}
	}
}

func TestAHandoffPastTheQuotaLandsNothing(t *testing.T) {
	s, root, _ := newHandoffServer(t)
	_, err := s.HandoffFiles(context.Background(), &agentv1.HandoffFilesRequest{
		FromAgent: "builder", ToAgent: "lead", SourcePath: "/home/builder/design/v2", RecipientQuotaBytes: 6,
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("err = %v, want ResourceExhausted", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "lead")); len(entries) != 0 {
		t.Errorf("something landed: %v", entries)
	}
	if !strings.Contains(err.Error(), "full") {
		t.Errorf("err %q doesn't say full", err)
	}
}

// agentGroups is the agent's own group, with group team only when asked.
func TestAgentGroups(t *testing.T) {
	if g := fmt.Sprint(agentGroups(2001, false)); g != "[2001]" {
		t.Errorf("own: %s", g)
	}
	if g := fmt.Sprint(agentGroups(2001, true)); g != fmt.Sprint([]uint32{2001, TeamGID}) {
		t.Errorf("team: %s", g)
	}
}

// A hand-off reads the project with group team, as the team reads it
// (CC-0507), and everything else with the agent's own group only: packing
// from the sender's home, and unpacking in the recipient's (R-0334).
func TestAHandoffHasTheTeamGroupOnlyToReadTheProject(t *testing.T) {
	s, _, _ := newHandoffServer(t)
	run := s.runHomeIO
	var calls []string
	s.runHomeIO = func(ctx context.Context, uid int, g []uint32, args []string, stdin io.Reader) ([]byte, error) {
		calls = append(calls, fmt.Sprintf("%s %d %v", args[0], uid, g))
		return run(ctx, uid, g, args, stdin)
	}
	team := fmt.Sprint(TeamGID)
	for _, c := range []struct {
		src  string
		want []string
	}{
		{"/home/work/shop/exports/report.txt", []string{"handoff-pack 2001 [2001 " + team + "]", "handoff-unpack 2002 [2002]"}},
		{"/home/builder/design/v2", []string{"handoff-pack 2001 [2001]", "handoff-unpack 2002 [2002]"}},
	} {
		calls = nil
		if _, err := s.HandoffFiles(context.Background(), &agentv1.HandoffFilesRequest{
			FromAgent: "builder", ToAgent: "lead", SourcePath: c.src, RecipientQuotaBytes: 1 << 20,
		}); err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if strings.Join(calls, "; ") != strings.Join(c.want, "; ") {
			t.Errorf("%s: calls %q, want %q", c.src, calls, c.want)
		}
	}
}
