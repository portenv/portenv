// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// lensRecorder is fake lensOps: which files got which readers.
type lensRecorder struct {
	home string
	got  map[string]string
}

func (l *lensRecorder) ops() lensOps {
	return lensOps{set: func(f *os.File, dir bool, readers []uint32, traverseOnly bool) error {
		rel, _ := filepath.Rel(l.home, f.Name())
		kind := "read"
		if traverseOnly {
			kind = "traverse"
		}
		var r []string
		for _, u := range readers {
			r = append(r, strconv.Itoa(int(u)))
		}
		l.got[rel] = kind + ":" + strings.Join(r, ",")
		return nil
	}}
}

func TestLensGrantWalksTheCloneOnly(t *testing.T) {
	home := agentHomeDir(t)
	for _, d := range []string{"shop/src", "notes"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"shop/src/a.go", "shop/README", "notes/secret.txt"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "shop", "out")); err != nil {
		t.Fatal(err)
	}
	l := &lensRecorder{home: home, got: map[string]string{}}
	if err := lensGrant(home, "shop", "2002", l.ops()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{".": "traverse:2002", "shop": "read:2002", "shop/src": "read:2002", "shop/src/a.go": "read:2002", "shop/README": "read:2002"}
	for p, w := range want {
		if l.got[p] != w {
			t.Errorf("%s: %q, want %q", p, l.got[p], w)
		}
	}
	for p := range l.got {
		if _, ok := want[p]; !ok {
			t.Errorf("touched %s, which isn't the home or the clone", p)
		}
	}
	// No clone yet: the home only.
	l = &lensRecorder{home: home, got: map[string]string{}}
	if err := lensGrant(home, "other", "-", l.ops()); err != nil || len(l.got) != 1 || l.got["."] != "traverse:" {
		t.Errorf("no clone: %v %v", l.got, err)
	}
	for _, bad := range [][2]string{{"shop", "1000"}, {"shop", "root"}, {"../x", "2002"}, {"shop", ""}} {
		if err := lensGrant(home, bad[0], bad[1], l.ops()); err == nil {
			t.Errorf("lens-grant %v accepted", bad)
		}
	}
}

// Every agent user is visited as itself, with exactly the readers granted
// on its clone now ("-" for none), so a grant that went is taken away.
func TestApplyLensGrantsVisitsEveryAgent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".portenv", "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".portenv", "team", "users.json"), []byte(`{"version":1,"users":[{"id":"builder","uid":2001},{"id":"reviewer","uid":2002},{"id":"lead","uid":2003},{"id":"gone","uid":2004,"removed":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	s := &apiServer{
		cfg: Config{User: "work", UID: 1000, HomeRoot: root},
		asAgentIO: func(_ context.Context, uid int, home string, args []string, _ io.Reader) ([]byte, error) {
			calls = append(calls, strings.Join(append([]string{filepath.Base(home)}, args[0], args[2], args[3]), " "))
			return nil, nil
		},
	}
	err := s.applyLensGrants(context.Background(), "/home/work/shop", []*agentv1.CloneReaders{
		{Writer: "builder", Readers: []string{"reviewer", "stranger", "builder"}},
		{Writer: "lead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"builder lens-grant shop 2002", "reviewer lens-grant shop -", "lead lens-grant shop -"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls %q, want %q", calls, want)
	}
	if err := s.applyLensGrants(context.Background(), "/home/lead/shop", nil); err == nil {
		t.Error("another home's project accepted")
	}
}
