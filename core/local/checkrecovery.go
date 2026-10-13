// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"errors"
	"slices"
	"strings"

	boxsync "github.com/portenv/portenv/core/sync"
)

// RecoveryCheck is what checking a typed recovery key found.
type RecoveryCheck struct {
	// Checked is how many workspaces here have a recovery key and could be
	// reached to check it.
	Checked int
	// Opened: the key's password opened one of them.
	Opened bool
}

// CheckRecoveryPasswords checks recovery-derived passwords, one per box ID,
// against the workspaces here that carry a recovery key (ADR 0019): before
// the app refills its pool from a typed key, it proves the key is the one
// these workspaces were made with. Read-only: restic opens each repository
// with the password and writes nothing. Workspaces whose storage isn't on
// this Mac or a connected folder, or has no repository, aren't counted. It
// stops at the first one that opens. Never logged.
func (e *Env) CheckRecoveryPasswords(ctx context.Context, passwords map[string][]byte) (RecoveryCheck, error) {
	var res RecoveryCheck
	ids := make([]string, 0, len(passwords))
	for id, pw := range passwords {
		if !boxIDRE.MatchString(id) {
			return res, createErr(ErrInvalid, "A box ID is box- and 12 lowercase hex digits.")
		}
		if !recoveryPasswordRE.Match(pw) {
			return res, createErr(ErrInvalid, "The recovery password must be the 64 hex characters derived from the recovery key.")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c, ok := e.boxWithID(id)
		if !ok || !c.Recovery {
			continue
		}
		// Server and bucket storage need their keys; not in 1.5.
		if strings.HasPrefix(c.Storage, "sftp:") || strings.HasPrefix(c.Storage, "s3:") {
			continue
		}
		if c.Storage != "" && !fileExists(c.Storage) { // #nosec G703 -- an absolute folder from the box's own config
			continue
		}
		b, err := e.repoEngine(c, passwords[id])
		if err != nil {
			return res, err
		}
		ok, err = b.Opens(ctx)
		switch {
		case errors.Is(err, boxsync.ErrNoRepository):
			continue
		case err != nil:
			return res, resticFailed("check the recovery key", err)
		}
		res.Checked++
		if ok {
			res.Opened = true
			return res, nil
		}
	}
	return res, nil
}
