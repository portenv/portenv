// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/tzname"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// macZone is this Mac's time zone as an IANA name, or UTC (tests replace
// it).
var macZone = tzname.Mac

// sendTimeZone gives a workspace that just started the Mac's time zone
// as it is now (R-0411): never fixed at Create, so a travelling owner's
// clock follows. It returns the zone the workspace applied, or "" for a
// helper too old to set one.
func sendTimeZone(ctx context.Context, c agentv1.AgentServiceClient, zone func() string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := c.SetTimeZone(ctx, &agentv1.SetTimeZoneRequest{Zone: zone()})
	if status.Code(err) == codes.Unimplemented {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.GetZone(), nil
}
