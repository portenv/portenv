// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNoRepository: there is no repository at the box's address.
var ErrNoRepository = errors.New("no repository at this address")

// restic's exit codes (0.17 and later).
const (
	exitNoRepository = 10
	exitWrongKey     = 12
)

// Opens reports whether this engine's password opens the repository: true
// when it does, false when restic finds no key for it (or none at all), and
// ErrNoRepository when nothing is there. It never writes to the repository.
// It runs on the host when there is a host executor.
func (b *Box) Opens(ctx context.Context) (bool, error) {
	_, stderr, err := b.restic.runWithin(ctx, 0, "cat", "config", "--no-lock")
	if err == nil {
		return true, nil
	}
	var de *DeadlineError
	if errors.As(err, &de) {
		return false, err
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		switch exit.Code {
		case exitNoRepository:
			return false, ErrNoRepository
		case exitWrongKey:
			return false, nil
		}
	}
	return false, resticError([]string{"cat"}, stderr, err)
}

// Create makes a new repository with this engine's password. It fails if
// one already exists: restic init never replaces a repository.
func (b *Box) Create(ctx context.Context) error {
	_, stderr, err := b.restic.runWithin(ctx, 0, "init")
	if err != nil {
		var de *DeadlineError
		if errors.As(err, &de) {
			return err
		}
		return resticError([]string{"init"}, stderr, err)
	}
	return nil
}

// KeyHosts lists the host label of each of the repository's keys (the
// recovery key's is portenv-recovery). The labels aren't secret.
func (b *Box) KeyHosts(ctx context.Context) ([]string, error) {
	out, err := b.restic.run(ctx, "key", "list", "--json", "--no-lock")
	if err != nil {
		return nil, err
	}
	var ks []struct {
		Host string `json:"hostName"`
	}
	if err := json.Unmarshal(out, &ks); err != nil {
		return nil, fmt.Errorf("parse restic key list: %w", err)
	}
	hosts := make([]string, len(ks))
	for i, k := range ks {
		hosts[i] = k.Host
	}
	return hosts, nil
}
