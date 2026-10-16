// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestGetEngineSaysWhetherDockerAnswers (R-0098): Welcome's Docker card and
// the state line ask portenvd, which asks the engine through the driver.
func TestGetEngineSaysWhetherDockerAnswers(t *testing.T) {
	s := quietServer(t)
	for _, c := range []struct {
		name    string
		engine  func(context.Context) error
		running bool
	}{
		{"answers", func(context.Context) error { return nil }, true},
		{"no engine", func(context.Context) error { return errors.New("connect: no such file or directory") }, false},
	} {
		s.engine = c.engine
		r, err := s.GetEngine(context.Background(), &daemonv1.GetEngineRequest{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if r.GetRunning() != c.running {
			t.Fatalf("%s: running = %v, want %v", c.name, r.GetRunning(), c.running)
		}
	}
}

// TestGetEngineAnswersWithinItsTimeLimit: an engine that never answers is
// "not running" after engineTimeout, not a hang (Welcome checks every 2 s).
func TestGetEngineAnswersWithinItsTimeLimit(t *testing.T) {
	s := quietServer(t)
	s.engine = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	r, err := s.GetEngine(context.Background(), &daemonv1.GetEngineRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetRunning() {
		t.Fatal("an engine that never answered counted as running")
	}
	if d := time.Since(start); d > engineTimeout+time.Second {
		t.Fatalf("GetEngine took %v (limit %v)", d, engineTimeout)
	}
}
