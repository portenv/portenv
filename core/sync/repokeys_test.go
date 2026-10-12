// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// hostBox opens a host-only engine on repo with password, as CreateBox
// does: no home, every command on this machine.
func hostBox(t *testing.T, restic, repo, password, state string) *Box {
	t.Helper()
	ex := LocalExecutor{Bin: restic, CacheDir: filepath.Join(state, "cache")}
	b, err := Open(Config{
		Executor: ex, MetaExecutor: ex, Repository: repo, MetaRepository: repo,
		Password: []byte(password), BoxID: "box-0123456789ab", MachineID: "mac-1",
		HomeDir: "/nonexistent", StateDir: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestOpensTellsMissingWrongAndRight: CreateBox needs to tell three
// repositories apart before it touches anything: none at the path, one this
// password opens, and one it doesn't.
func TestOpensTellsMissingWrongAndRight(t *testing.T) {
	t.Parallel()
	bin := resticBinary(t)
	root := t.TempDir()
	repo := filepath.Join(root, "boxes", "box-0123456789ab")
	ctx := context.Background()
	right := hostBox(t, bin, repo, "right-password", filepath.Join(root, "s1"))
	wrong := hostBox(t, bin, repo, "wrong-password", filepath.Join(root, "s2"))

	if _, err := right.Opens(ctx); !errors.Is(err, ErrNoRepository) {
		t.Fatalf("no repository yet: %v, want ErrNoRepository", err)
	}
	if err := right.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := right.Opens(ctx); err != nil || !ok {
		t.Fatalf("its own password: %v, %v", ok, err)
	}
	if ok, err := wrong.Opens(ctx); err != nil || ok {
		t.Fatalf("another password: %v, %v, want false and no error", ok, err)
	}
	// Creating over an existing repository fails rather than replacing it.
	if err := wrong.Create(ctx); err == nil {
		t.Fatal("init over an existing repository succeeded")
	}
	if ok, err := right.Opens(ctx); err != nil || !ok {
		t.Fatalf("after a refused init: %v, %v", ok, err)
	}
}

// TestKeyHostsListsEachKeysLabel: the recovery key is added with its own
// host label, and a retry can count how many there are.
func TestKeyHostsListsEachKeysLabel(t *testing.T) {
	t.Parallel()
	bin := resticBinary(t)
	root := t.TempDir()
	repo := filepath.Join(root, "boxes", "box-0123456789ab")
	ctx := context.Background()
	b := hostBox(t, bin, repo, "repository-key", filepath.Join(root, "s1"))
	if err := b.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.AddKey(ctx, []byte("recovery-password"), "portenv-recovery"); err != nil {
		t.Fatal(err)
	}
	hosts, err := b.KeyHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, h := range hosts {
		if h == "portenv-recovery" {
			n++
		}
	}
	if len(hosts) != 2 || n != 1 {
		t.Fatalf("key hosts %v, want two keys with one portenv-recovery", hosts)
	}
}
