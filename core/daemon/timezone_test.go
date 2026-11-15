// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

type zoneAgent struct {
	agentv1.AgentServiceClient
	got []string
	err error
}

func (a *zoneAgent) SetTimeZone(_ context.Context, req *agentv1.SetTimeZoneRequest, _ ...grpc.CallOption) (*agentv1.SetTimeZoneResponse, error) {
	a.got = append(a.got, req.GetZone())
	if a.err != nil {
		return nil, a.err
	}
	return &agentv1.SetTimeZoneResponse{Zone: req.GetZone()}, nil
}

// Each start sends the Mac's zone as it is now, so a travelling owner's
// workspace follows.
func TestSendTimeZoneFollowsTheMac(t *testing.T) {
	a := &zoneAgent{}
	for _, z := range []string{"Europe/Paris", "America/New_York"} {
		applied, err := sendTimeZone(context.Background(), a, func() string { return z })
		if err != nil || applied != z {
			t.Fatalf("%s: applied %q, %v", z, applied, err)
		}
	}
	if len(a.got) != 2 || a.got[0] != "Europe/Paris" || a.got[1] != "America/New_York" {
		t.Fatalf("sent %v", a.got)
	}
}

// A helper too old to know SetTimeZone isn't an error: the clock stays
// the image's.
func TestSendTimeZoneOlderHelper(t *testing.T) {
	a := &zoneAgent{err: status.Error(codes.Unimplemented, "unknown method SetTimeZone")}
	applied, err := sendTimeZone(context.Background(), a, func() string { return "Europe/Paris" })
	if err != nil || applied != "" {
		t.Fatalf("applied %q, %v", applied, err)
	}
	a.err = status.Error(codes.FailedPrecondition, "read-only file system")
	if _, err := sendTimeZone(context.Background(), a, func() string { return "Europe/Paris" }); err == nil {
		t.Fatal("a failed SetTimeZone wasn't reported")
	}
}
