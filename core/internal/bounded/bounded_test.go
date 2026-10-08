// SPDX-License-Identifier: Apache-2.0

package bounded

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestEveryProcessHasALimit: no limit, no process.
func TestEveryProcessHasALimit(t *testing.T) {
	if _, err := Command(context.Background(), 0, "true"); !errors.Is(err, ErrNoLimit) {
		t.Fatalf("limit 0: %v, want ErrNoLimit", err)
	}
}

// TestAProcessIsStoppedAtItsLimit: SIGINT at the limit; a process that
// handles it exits cleanly.
func TestAProcessIsStoppedAtItsLimit(t *testing.T) {
	cmd, err := Command(context.Background(), 300*time.Millisecond, "sh", "-c", `trap 'kill $s; echo stopped; exit 0' INT; sleep 30 & s=$!; wait $s`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, _ := cmd.Output()
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v; the limit was 300 ms", d)
	}
	if string(out) != "stopped\n" {
		t.Fatalf("output %q: the process did not get SIGINT first", out)
	}
}

// TestTheContextsDeadlineComesFirst: Limit hands down a caller's deadline.
func TestTheContextsDeadlineComesFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if l := Limit(ctx, time.Hour); l > 200*time.Millisecond || l <= 0 {
		t.Fatalf("Limit %v, want at most the context's 200 ms", l)
	}
	if l := Limit(context.Background(), time.Minute); l != time.Minute {
		t.Fatalf("no deadline: %v, want the fallback", l)
	}
}
