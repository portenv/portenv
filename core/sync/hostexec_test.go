// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// recordingExecutor records the restic commands it runs.
type recordingExecutor struct {
	Executor
	mu   sync.Mutex
	cmds []string
}

func (r *recordingExecutor) Restic(ctx context.Context, args []string, cred Credentials) (ExecResult, error) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--repo" || args[i] == "--cache-dir" {
			i++
			continue
		}
		r.mu.Lock()
		r.cmds = append(r.cmds, args[i])
		r.mu.Unlock()
		break
	}
	return r.Executor.Restic(ctx, args, cred)
}

// TestOnlyBackupAndRestoreRunInTheBox: with a host executor configured, the
// box runs only the commands that need /home; listing, tags and init run on
// the host.
func TestOnlyBackupAndRestoreRunInTheBox(t *testing.T) {
	w := newWorld(t)
	a, b := w.machine("a"), w.machine("b")
	for _, m := range []*machine{a, b} {
		box := &recordingExecutor{Executor: m.cfg.Executor}
		host := &recordingExecutor{Executor: LocalExecutor{Bin: w.restic, CacheDir: filepath.Join(w.root, m.id, "hostcache")}}
		m.cfg.Executor, m.restic.exec, m.restic.meta, m.restic.metaRepo = box, box, host, w.repo
		defer func(m *machine, box, host *recordingExecutor) {
			for _, c := range box.cmds {
				if c != "backup" && c != "restore" {
					t.Errorf("%s: %q ran in the box", m.id, c)
				}
			}
			for _, c := range host.cmds {
				if c == "backup" || c == "restore" {
					t.Errorf("%s: %q ran on the host", m.id, c)
				}
			}
			if !slices.Contains(host.cmds, "snapshots") {
				t.Errorf("%s: host ran %v, want the listing there", m.id, host.cmds)
			}
			// Saves are created already tagged, so a (which only saves)
			// never tags; b takes the lease on resume, on the host.
			if m.id == "a" && slices.Contains(host.cmds, "tag") {
				t.Errorf("a: host ran %v; saves must not run a tag step", host.cmds)
			}
			if m.id == "b" && !slices.Contains(host.cmds, "tag") {
				t.Errorf("b: host ran %v, want b's lease tag there", host.cmds)
			}
		}(m, box, host)
	}
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveRelease)
	mustResume(t, b, ResumeOptions{}) // restore, in b's box
	equalFiles(t, "b's home", b.files(), map[string]string{"f": "1"})
}

// TestAddKeyGivesAMachineItsOwnKey: a key added on a machine opens the
// repository by itself, so that machine never needs another's password.
func TestAddKeyGivesAMachineItsOwnKey(t *testing.T) {
	w := newWorld(t)
	a := w.machine("a")
	mustResume(t, a, ResumeOptions{})
	a.write("f", "1")
	mustSave(t, a, SaveRelease)

	a.restic.meta = LocalExecutor{Bin: w.restic, CacheDir: filepath.Join(w.root, "a", "hostcache")}
	a.restic.metaRepo = w.repo
	own := []byte("server-own-password-" + token())
	if err := a.AddKey(context.Background(), own, "server"); err != nil {
		t.Fatal(err)
	}

	w.password = own // a new machine that only knows its own key
	s := w.machine("server")
	res := mustResume(t, s, ResumeOptions{})
	if res.Rule != 5 {
		t.Fatalf("rule %d, want 5", res.Rule)
	}
	equalFiles(t, "restored with its own key", s.files(), map[string]string{"f": "1"})
}
