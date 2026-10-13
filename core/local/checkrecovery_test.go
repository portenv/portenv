// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/portenv/portenv/core/keys/recovery"
)

// TestCheckRecoveryPasswords: a typed recovery key is checked against the
// workspaces here that carry a recovery key, before the app refills its
// pool from it (ADR 0019, "When it runs out"): the right key opens one, a
// different valid key opens none, and with no such workspace there's
// nothing to check against. Nothing is written to any repository.
func TestCheckRecoveryPasswords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newCreateEnv(t)
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	if _, err := e.CreateBox(ctx, req, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// A box without the recovery flag (as portenv init's) isn't checked,
	// even when its repository would open: here it does.
	oldReq := createRequest(t, "old", "box-0123456789ac", "")
	if _, err := e.CreateBox(ctx, oldReq, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveBox(BoxConfig{ID: oldReq.BoxID, Name: "old", Image: "img"}); err != nil {
		t.Fatal(err)
	}
	before := treeSum(t, repoOf(e, req))

	right := map[string][]byte{req.BoxID: req.RecoveryPassword}
	res, err := e.CheckRecoveryPasswords(ctx, right)
	if err != nil || res.Checked != 1 || !res.Opened {
		t.Fatalf("the right key: %+v, %v", res, err)
	}

	other, err := recovery.Generate(bytes.NewReader(bytes.Repeat([]byte{7}, 23)))
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := recovery.Password(other, req.BoxID)
	res, err = e.CheckRecoveryPasswords(ctx, map[string][]byte{req.BoxID: []byte(pw)})
	if err != nil || res.Checked != 1 || res.Opened {
		t.Fatalf("another valid key: %+v, %v", res, err)
	}

	// No workspace with a recovery key among those asked about.
	res, err = e.CheckRecoveryPasswords(ctx, map[string][]byte{"box-0123456789ac": vectorPassword(t, "box-0123456789ac")})
	if err != nil || res.Checked != 0 || res.Opened {
		t.Fatalf("nothing to check against: %+v, %v", res, err)
	}
	if treeSum(t, repoOf(e, req)) != before {
		t.Fatal("checking changed the repository")
	}

	// Bad input: a password that isn't a derived one, or a bad box ID.
	if _, err := e.CheckRecoveryPasswords(ctx, map[string][]byte{req.BoxID: []byte("K7QF-M2XA-9TRD-HV4P-W8LC-3NZ5")}); kindOf(err) != ErrInvalid {
		t.Fatalf("the recovery key itself: %v, want invalid", err)
	}
	if _, err := e.CheckRecoveryPasswords(ctx, map[string][]byte{"../x": req.RecoveryPassword}); kindOf(err) != ErrInvalid {
		t.Fatalf("a bad box ID: %v, want invalid", err)
	}
	assertNoSecrets(t, e.Dir, nil, req.RecoveryPassword, []byte(pw))
}

// TestCheckRecoveryPasswordsSkipsUnreachableStorage: a workspace whose
// drive isn't connected can't be checked, and isn't counted.
func TestCheckRecoveryPasswordsSkipsUnreachableStorage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newCreateEnv(t)
	drive := filepath.Join(t.TempDir(), "drive")
	if err := os.MkdirAll(drive, 0o700); err != nil {
		t.Fatal(err)
	}
	req := createRequest(t, "acme-api", "box-0123456789ab", drive)
	if _, err := e.CreateBox(ctx, req, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(drive); err != nil {
		t.Fatal(err)
	}
	res, err := e.CheckRecoveryPasswords(ctx, map[string][]byte{req.BoxID: req.RecoveryPassword})
	if err != nil || res.Checked != 0 || res.Opened {
		t.Fatalf("an unplugged drive: %+v, %v", res, err)
	}
}

// TestCheckRecoveryPasswordsIsBounded (R-0164): each entry can cost a
// restic open, so more than 64, or more than this Mac has workspaces with a
// recovery key, is refused before restic runs at all.
func TestCheckRecoveryPasswordsIsBounded(t *testing.T) {
	ctx := context.Background()
	e := newCreateEnv(t)
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	if _, err := e.CreateBox(ctx, req, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Two entries, one workspace with a recovery key here.
	two := map[string][]byte{req.BoxID: req.RecoveryPassword, "box-0123456789ac": vectorPassword(t, "box-0123456789ac")}
	t.Setenv("PORTENV_RESTIC", "/nonexistent/restic") // any restic run would fail differently
	if _, err := e.CheckRecoveryPasswords(ctx, two); kindOf(err) != ErrInvalid {
		t.Fatalf("more than this Mac's workspaces with a recovery key: %v, want invalid", err)
	}
	many := map[string][]byte{}
	for i := range 65 {
		id := fmt.Sprintf("box-%012x", i)
		many[id] = vectorPassword(t, id)
	}
	if _, err := e.CheckRecoveryPasswords(ctx, many); kindOf(err) != ErrInvalid {
		t.Fatalf("65 entries: %v, want invalid", err)
	}
}

// TestRecoveryWorkspacesAreCounted: the count the bound uses.
func TestRecoveryWorkspacesAreCounted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newCreateEnv(t)
	if n := e.RecoveryWorkspaces(); n != 0 {
		t.Fatalf("none yet: %d", n)
	}
	req := createRequest(t, "acme-api", "box-0123456789ab", "")
	if _, err := e.CreateBox(ctx, req, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.PublishConfig(BoxConfig{ID: "box-0123456789ac", Name: "old", Image: "img"}); err != nil {
		t.Fatal(err)
	}
	if n := e.RecoveryWorkspaces(); n != 1 {
		t.Fatalf("one with a recovery key: %d", n)
	}
}
