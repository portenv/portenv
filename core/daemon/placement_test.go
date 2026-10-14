// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/local"
	boxsync "github.com/portenv/portenv/core/sync"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// fakeSSH: each host answers `portenv machine-id` and `portenv app ping`
// as told ("ok", "fail" or "hang").
func fakeSSH(t *testing.T, hosts map[string][2]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nhost=$1; shift\ncase \"$host\" in\n")
	for host, v := range hosts {
		b.WriteString("  " + host + ") id=" + v[0] + "; ping=" + v[1] + " ;;\n")
	}
	b.WriteString("  *) exit 255 ;;\nesac\ncase \"$*\" in\n  *machine-id*) echo \"$id\" ;;\n")
	b.WriteString("  *'app ping'*) case $ping in ok) echo ok ;; fail) exit 1 ;; hang) sleep 30 ;; esac ;;\n")
	b.WriteString("  *'app state'*) echo '{\"state\":\"SAVE_STATE_SAVED\"}' ;;\n  *) exit 2 ;;\nesac\n")
	fake := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(fake, []byte(b.String()), 0o700); err != nil { // #nosec G306 -- test helper
		t.Fatal(err)
	}
	t.Setenv("PORTENV_SSH", fake)
}

// roomToAnswer: see local's (a loaded machine mustn't read an answering
// fake server as down); the hanging one is still cut at its deadline.
func roomToAnswer(t *testing.T) {
	saved := local.PingDeadline
	local.PingDeadline = 6 * time.Second
	t.Cleanup(func() { local.PingDeadline = saved })
}

// placementServer has servers in machine.json and one box, acme-api.
func placementServer(t *testing.T, servers ...string) *Server {
	t.Helper()
	dir := t.TempDir()
	e := &local.Env{Dir: dir, Machine: "mac-1"}
	quoted := make([]string, len(servers))
	for i, h := range servers {
		quoted[i] = `"` + h + `"`
	}
	if err := os.WriteFile(filepath.Join(dir, "machine.json"), []byte(`{"servers":[`+strings.Join(quoted, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.PublishConfig(local.BoxConfig{ID: "box-0123456789ab", Name: "acme-api", Image: "img"}); err != nil {
		t.Fatal(err)
	}
	return New(e, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// trailerStream records the trailer a handler sets.
type trailerStream struct {
	grpc.ServerTransportStream
	trailer metadata.MD
}

func (t *trailerStream) Method() string               { return "/test" }
func (t *trailerStream) SetHeader(metadata.MD) error  { return nil }
func (t *trailerStream) SendHeader(metadata.MD) error { return nil }
func (t *trailerStream) SetTrailer(md metadata.MD) error {
	t.trailer = metadata.Join(t.trailer, md)
	return nil
}

// TestOpenElsewhereIsMarked: OpenBox's refusal for a box open elsewhere is
// FailedPrecondition with the trailer portenv-refusal: open-elsewhere, so
// the app never reads the words to know (R-0168); and the lease is kept for
// GetPlacement.
func TestOpenElsewhereIsMarked(t *testing.T) {
	s := placementServer(t)
	stream := &trailerStream{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
	saved := time.Date(2026, 10, 13, 14, 58, 0, 0, time.UTC)
	err := s.refuseOpenElsewhere(ctx, "acme-api", &boxsync.LeaseHeldError{Lease: boxsync.Lease{Machine: "studio-1a2b3c", LastSeen: saved}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code %v", status.Code(err))
	}
	if got := stream.trailer.Get(RefusalTrailer); len(got) != 1 || got[0] != RefusalOpenElsewhere {
		t.Fatalf("trailer %v", stream.trailer)
	}
	p, err := s.GetPlacement(context.Background(), &daemonv1.GetPlacementRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if p.GetMachine() != "studio-1a2b3c" || !p.GetNewestSaveAt().AsTime().Equal(saved) {
		t.Fatalf("placement %v", p)
	}
}

// TestGetPlacement: a known server that answers, one that doesn't (within
// 2 s even when it hangs), a machine Portenv can't ask, and nobody.
func TestGetPlacement(t *testing.T) {
	roomToAnswer(t)
	fakeSSH(t, map[string][2]string{"me@up": {"up-111111", "ok"}, "me@down": {"down-222222", "fail"}, "me@hung": {"hung-333333", "hang"}})
	s := placementServer(t, "me@up", "me@down", "me@hung")
	ctx := context.Background()
	for _, tc := range []struct {
		name, machine string
		holder        daemonv1.Holder
		server        string // the address, which must not be in the response
		answers       daemonv1.Answers
	}{
		{"a known server that answers", "up-111111", daemonv1.Holder_HOLDER_KNOWN_SERVER, "me@up", daemonv1.Answers_ANSWERS_YES},
		{"a known server that doesn't", "down-222222", daemonv1.Holder_HOLDER_KNOWN_SERVER, "me@down", daemonv1.Answers_ANSWERS_NO},
		{"a known server that hangs", "hung-333333", daemonv1.Holder_HOLDER_KNOWN_SERVER, "me@hung", daemonv1.Answers_ANSWERS_NO},
		{"another Mac", "studio-1a2b3c", daemonv1.Holder_HOLDER_OTHER_MACHINE, "", daemonv1.Answers_ANSWERS_CANNOT_ASK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.holdElsewhere("acme-api", boxsync.Lease{Machine: tc.machine, LastSeen: time.Now()})
			start := time.Now()
			p, err := s.GetPlacement(ctx, &daemonv1.GetPlacementRequest{Name: "acme-api", Probe: true})
			if err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > 2*local.PingDeadline+2*time.Second {
				t.Fatalf("took %v: the probes are bounded, each at once", d)
			}
			if p.GetHolder() != tc.holder || p.GetAnswers() != tc.answers || p.GetMachine() != tc.machine {
				t.Fatalf("got %v", p)
			}
			wantID, wantName := "", ""
			if tc.server != "" {
				wantID, wantName = local.ServerID(tc.server), local.ServerDisplayName(tc.server)
			}
			if p.GetServerId() != wantID || p.GetServerName() != wantName {
				t.Fatalf("server %q %q, want %q %q", p.GetServerId(), p.GetServerName(), wantID, wantName)
			}
			// Servers show by name: no address anywhere in the answer.
			if tc.server != "" && strings.Contains(p.String(), tc.server) {
				t.Fatalf("the address %s is in the response: %v", tc.server, p)
			}
		})
	}
	s.openedHere("acme-api")
	p, err := s.GetPlacement(ctx, &daemonv1.GetPlacementRequest{Name: "acme-api"})
	if err != nil || p.GetHolder() != daemonv1.Holder_HOLDER_NONE {
		t.Fatalf("once opened here: %v, %v", p, err)
	}
	if _, err := s.GetPlacement(ctx, &daemonv1.GetPlacementRequest{Name: "nope"}); status.Code(err) != codes.NotFound {
		t.Fatalf("an unknown box: %v", err)
	}
}

// TestMoveHereClosesTheHoldingServer: "Move it to this Mac" closes it (save
// and release) on the known server that holds it, even one this Mac didn't
// put it on.
func TestMoveHereClosesTheHoldingServer(t *testing.T) {
	roomToAnswer(t)
	fakeSSH(t, map[string][2]string{"me@up": {"up-111111", "ok"}})
	s := placementServer(t, "me@up")
	if host := s.holdingServer(context.Background(), "acme-api"); host != "" {
		t.Fatalf("nobody holds it: %q", host)
	}
	s.holdElsewhere("acme-api", boxsync.Lease{Machine: "up-111111", LastSeen: time.Now()})
	if host := s.holdingServer(context.Background(), "acme-api"); host != "me@up" {
		t.Fatalf("held by me@up: %q", host)
	}
	s.holdElsewhere("acme-api", boxsync.Lease{Machine: "studio-1a2b3c", LastSeen: time.Now()})
	if host := s.holdingServer(context.Background(), "acme-api"); host != "" {
		t.Fatalf("another Mac can't be closed from here: %q", host)
	}
}

// TestPlacementWithoutAProbeIsAtOnce: the sheet names the place at once,
// then asks again with the probe (R-0140).
func TestPlacementWithoutAProbeIsAtOnce(t *testing.T) {
	fakeSSH(t, map[string][2]string{"me@hung": {"hung-333333", "hang"}})
	s := placementServer(t, "me@hung")
	s.env.RememberServerMachine("me@hung", "hung-333333")
	s.holdElsewhere("acme-api", boxsync.Lease{Machine: "hung-333333", LastSeen: time.Now()})
	start := time.Now()
	p, err := s.GetPlacement(context.Background(), &daemonv1.GetPlacementRequest{Name: "acme-api"})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v without a probe", d)
	}
	if p.GetHolder() != daemonv1.Holder_HOLDER_KNOWN_SERVER || p.GetAnswers() != daemonv1.Answers_ANSWERS_UNSPECIFIED || p.GetServerName() != "hung" {
		t.Fatalf("got %v", p)
	}
}

// TestConnectThere: Connect to it there attaches to the known server that
// holds it, leaving it running there; with no known server holding it,
// it's refused and nothing opens here.
func TestConnectThere(t *testing.T) {
	roomToAnswer(t)
	fakeSSH(t, map[string][2]string{"me@up": {"up-111111", "ok"}})
	s := placementServer(t, "me@up")
	ctx := context.Background()
	if _, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: "acme-api", ConnectThere: true}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nobody holds it: %v", err)
	}
	s.holdElsewhere("acme-api", boxsync.Lease{Machine: "studio-1a2b3c", LastSeen: time.Now()})
	if _, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: "acme-api", ConnectThere: true}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("another Mac: %v", err)
	}
	s.holdElsewhere("acme-api", boxsync.Lease{Machine: "up-111111", LastSeen: time.Now()})
	r, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: "acme-api", ConnectThere: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetLocation() != "me@up" || s.location("acme-api") != "me@up" {
		t.Fatalf("connected to %q (location %q)", r.GetLocation(), s.location("acme-api"))
	}
}
