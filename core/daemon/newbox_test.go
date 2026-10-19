// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/driver"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// New workspace says plainly when Docker Desktop isn't running or the disk
// is short, before anything is written (R-0323).
func TestCreateBoxChecksDockerAndDiskFirst(t *testing.T) {
	old := newBoxEngineWait
	newBoxEngineWait = 200 * time.Millisecond
	t.Cleanup(func() { newBoxEngineWait = old })
	for _, c := range []struct {
		name   string
		engine func(context.Context) error
		free   func(string) (uint64, error)
		code   codes.Code
		line   string
	}{
		{"Docker Desktop isn't running",
			func(context.Context) error {
				return fmt.Errorf("docker ping: %w", errors.New("Cannot connect to the Docker daemon at unix:///Users/someone/.docker/run/docker.sock"))
			},
			nil, codes.Unavailable, NewBoxEngineNotRunning},
		{"Docker Desktop doesn't answer",
			func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			nil, codes.Unavailable, NewBoxEngineUnresponsive},
		{"Docker Desktop says it's starting",
			func(context.Context) error { return fmt.Errorf("x: %w", driver.ErrEngineUnresponsive) },
			nil, codes.Unavailable, NewBoxEngineUnresponsive},
		{"the disk is short",
			nil, func(string) (uint64, error) { return 3_200_000_000, nil },
			codes.ResourceExhausted, "This Mac has 3.2 GB free, and a new workspace needs at least 5 GB. Free some space, then create the workspace again."},
		{"the disk is almost empty",
			nil, func(string) (uint64, error) { return 120_000_000, nil },
			codes.ResourceExhausted, "This Mac has 0.1 GB free, and a new workspace needs at least 5 GB. Free some space, then create the workspace again."},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, logs := loggingServer(t)
			if c.engine != nil {
				s.engine = c.engine
			}
			if c.free != nil {
				s.freeBytes = c.free
			}
			start := time.Now()
			_, err := s.CreateBox(context.Background(), createBoxRequest("acme-api", testBoxID))
			if st := status.Convert(err); st.Code() != c.code || st.Message() != c.line {
				t.Fatalf("CreateBox = %v %q\nwant %v %q", st.Code(), st.Message(), c.code, c.line)
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("the check took %s", took)
			}
			// Nothing written: no box, no repository, no pending creation.
			if ents, _ := os.ReadDir(s.env.Dir); len(ents) != 0 {
				var names []string
				for _, e := range ents {
					names = append(names, e.Name())
				}
				t.Errorf("CreateBox wrote %v before refusing", names)
			}
			if list, err := s.ListBoxes(context.Background(), &daemonv1.ListBoxesRequest{}); err != nil || len(list.GetBoxes()) != 0 {
				t.Errorf("boxes after a refusal: %v, %v", list.GetBoxes(), err)
			}
			secretsIn(t, "the log", logs.String())
			if strings.Contains(logs.String(), "/Users/someone") {
				t.Errorf("the log names a home folder: %s", logs.String())
			}
		})
	}
}

// A disk that can't be measured doesn't stop a create, and enough room
// lets it go ahead.
func TestCreateBoxGoesAheadWithRoom(t *testing.T) {
	useRestic(t)
	for name, free := range map[string]func(string) (uint64, error){
		"room":         func(string) (uint64, error) { return minFreeForNewBox, nil },
		"not measured": func(string) (uint64, error) { return 0, errors.New("statfs: not supported") },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := loggingServer(t)
			s.freeBytes = free
			if _, err := s.CreateBox(context.Background(), createBoxRequest("acme-api", testBoxID)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGigabytes(t *testing.T) {
	for n, want := range map[uint64]string{5_000_000_000: "5 GB", 3_250_000_000: "3.2 GB", 12_400_000_000: "12 GB", 0: "0 GB"} {
		if got := gigabytes(n); got != want {
			t.Errorf("gigabytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// freeBytes measures a real folder.
func TestFreeBytesMeasures(t *testing.T) {
	n, err := freeBytes(t.TempDir())
	if err != nil || n == 0 {
		t.Fatalf("freeBytes = %d, %v", n, err)
	}
}

// TestNewWorkspaceLinesMatchTheApps: CreateBox's refusals are the lines in
// testdata/new-workspace-lines.txt, which the app's test runs through
// DaemonErrorText unchanged, like the engine lines (R-0214).
func TestNewWorkspaceLinesMatchTheApps(t *testing.T) {
	b, err := os.ReadFile("testdata/new-workspace-lines.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	got := []string{NewBoxEngineNotRunning, NewBoxEngineUnresponsive, newBoxNoDisk(3_200_000_000)}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines\n got %q\nwant %q", got, want)
	}
	for _, l := range got {
		if !strings.HasSuffix(l, "then create the workspace again.") || strings.Contains(l, "'") {
			t.Errorf("%q: should end \"then create the workspace again.\" with typographic apostrophes", l)
		}
	}
}
