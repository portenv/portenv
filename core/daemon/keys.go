// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/keys"
	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// keyError turns a key that needs the person's approval into its one plain
// line (FailedPrecondition): portenvd never raises a Keychain prompt; the
// app reads the key in front of the person and hands it over (ProvideKeys).
func keyError(err error) error {
	if errors.Is(err, keys.ErrNeedsApproval) {
		return status.Error(codes.FailedPrecondition, keys.ErrNeedsApproval.Error())
	}
	return err
}

// ProvideKeys keeps keys the app read from the Keychain, in memory only,
// for a box's own key IDs.
func (s *Server) ProvideKeys(_ context.Context, req *daemonv1.ProvideKeysRequest) (*daemonv1.ProvideKeysResponse, error) {
	c, err := s.env.LoadBox(req.GetName())
	if err != nil {
		return nil, err
	}
	allowed := local.KeyIDs(c)
	for id, key := range req.GetKeys() {
		if !slices.Contains(allowed, id) {
			return nil, status.Errorf(codes.InvalidArgument, "%s is not one of %s's keys", id, req.GetName())
		}
		if len(key) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "empty key %s", id)
		}
	}
	for id, key := range req.GetKeys() {
		_ = s.env.Provided.Put(id, key)
	}
	return &daemonv1.ProvideKeysResponse{}, nil
}
