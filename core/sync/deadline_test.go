// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRestic is a restic stand-in: it records each command, hangs on a
// command as many times as hang-<command> says, and notes a SIGINT.
type fakeRestic struct {
	t   *testing.T
	dir string
	bin string
}

func newFakeRestic(t *testing.T) *fakeRestic {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	script := `#!/bin/sh
d="` + dir + `"
cmd=""
while [ $# -gt 0 ]; do
	case "$1" in
	--repo|--cache-dir|--key-hint) shift 2 ;;
	--*) shift ;;
	*) cmd=$1; break ;;
	esac
done
echo "$cmd" >> "$d/calls"
echo $$ >> "$d/pids"
n=$(cat "$d/hang-$cmd" 2>/dev/null || echo 0)
if [ "$n" -gt 0 ]; then
	echo $((n - 1)) > "$d/hang-$cmd"
	trap 'echo "sigint $cmd" >> "$d/calls"; kill $s 2>/dev/null; exit 130' INT
	sleep 1000 & s=$!
	wait $s
	exit 1
fi
case "$cmd" in
version) echo "restic 0.18.0 compiled with go1.26 on linux/arm64" ;;
snapshots) echo "[]" ;;
backup) echo '{"message_type":"summary","snapshot_id":"abc123","total_bytes_processed":10}' ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}
	return &fakeRestic{t: t, dir: dir, bin: bin}
}

func (f *fakeRestic) hang(cmd string, times int) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "hang-"+cmd), []byte(strconv.Itoa(times)), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeRestic) calls() []string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls")) // #nosec G304 -- the test's own file
	return strings.Fields(strings.ReplaceAll(string(b), "sigint ", "sigint-"))
}

// noneRunning: every fake restic that was started has exited.
func (f *fakeRestic) noneRunning() bool {
	b, _ := os.ReadFile(filepath.Join(f.dir, "pids")) // #nosec G304 -- the test's own file
	for _, p := range strings.Fields(string(b)) {
		pid, _ := strconv.Atoi(p)
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			return false
		}
	}
	return true
}

// Short enough to keep the tests quick, long enough that a fake restic that
// doesn't hang always finishes, even under the race detector.
var testDeadlines = Deadlines{Short: 5 * time.Second, Check: 5 * time.Second, Base: 5 * time.Second, BytesPerSecond: 1 << 20}

func (f *fakeRestic) restic(retrying *[]bool) *restic {
	return &restic{exec: LocalExecutor{Bin: f.bin}, repo: filepath.Join(f.dir, "repo"),
		cred: Credentials{Password: []byte("pw")}, deadlines: testDeadlines,
		onRetry: func(on bool) { *retrying = append(*retrying, on) }}
}

// TestAHungListingIsKilledAndRetried: a listing that hangs is stopped at its
// deadline with SIGINT, the stale lock is cleared, and it runs once more.
// Listing writes nothing, so the repository is not checked.
func TestAHungListingIsKilledAndRetried(t *testing.T) {
	f := newFakeRestic(t)
	f.hang("snapshots", 1)
	var retrying []bool
	start := time.Now()
	snaps, err := f.restic(&retrying).snapshots(context.Background())
	if err != nil {
		t.Fatalf("the retry should succeed: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("snapshots: %v", snaps)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("took %v; the deadline is 5 s", d)
	}
	want := []string{"snapshots", "sigint-snapshots", "unlock", "snapshots"}
	if got := f.calls(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("calls %v, want %v", got, want)
	}
	if strings.Join(boolsToStrings(retrying), " ") != "true false" {
		t.Fatalf("retry notices %v, want on then off", retrying)
	}
	if !f.noneRunning() {
		t.Fatal("a killed restic is still running")
	}
}

// TestAHungBackupIsCheckedThenRetried: a backup writes, so after it is
// killed the repository is checked before the backup runs again.
func TestAHungBackupIsCheckedThenRetried(t *testing.T) {
	f := newFakeRestic(t)
	f.hang("backup", 1)
	var retrying []bool
	id, err := f.restic(&retrying).backup(context.Background(), backupArgs{home: "/home"})
	if err != nil || id != "abc123" {
		t.Fatalf("backup after the retry: %q, %v (calls %v)", id, err, f.calls())
	}
	want := []string{"backup", "sigint-backup", "unlock", "check", "backup"}
	if got := f.calls(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// TestABackupThatHangsTwiceFailsWithinTheDeadlines: the retry hangs too; the
// save fails (it never hangs), and nothing is left running.
func TestABackupThatHangsTwiceFailsWithinTheDeadlines(t *testing.T) {
	f := newFakeRestic(t)
	f.hang("backup", 2)
	var retrying []bool
	start := time.Now()
	_, err := f.restic(&retrying).backup(context.Background(), backupArgs{home: "/home"})
	var de *DeadlineError
	if !errors.As(err, &de) {
		t.Fatalf("err %v, want a DeadlineError", err)
	}
	if d := time.Since(start); d > 40*time.Second {
		t.Fatalf("took %v", d)
	}
	if strings.Join(boolsToStrings(retrying), " ") != "true false" {
		t.Fatalf("retry notices %v", retrying)
	}
	if !f.noneRunning() {
		t.Fatal("a killed restic is still running")
	}
}

// TestCancellingIsNotADeadline: when the caller gives up (the app quits),
// nothing is retried.
func TestCancellingIsNotADeadline(t *testing.T) {
	f := newFakeRestic(t)
	f.hang("snapshots", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var retrying []bool
	_, err := f.restic(&retrying).snapshots(ctx)
	var de *DeadlineError
	if err == nil || errors.As(err, &de) {
		t.Fatalf("err %v, want the caller's cancellation", err)
	}
	if len(retrying) != 0 {
		t.Fatalf("retried after a cancellation: %v", retrying)
	}
}

// TestDeadlinesScaleWithTheData: backup and restore get the base plus the
// data at the floor rate; everything else the short deadline, check its own.
func TestDeadlinesScaleWithTheData(t *testing.T) {
	d := Deadlines{}.withDefaults()
	if got := d.For("snapshots", 1<<40); got != d.Short {
		t.Fatalf("snapshots: %v", got)
	}
	if got := d.For("tag", 0); got != d.Short {
		t.Fatalf("tag: %v", got)
	}
	if got := d.For("check", 0); got != d.Check {
		t.Fatalf("check: %v", got)
	}
	if got, want := d.For("backup", 300<<20), d.Base+300*time.Second; got != want {
		t.Fatalf("backup of 300 MiB: %v, want %v", got, want)
	}
	if got, want := d.For("restore", 0), d.Base; got != want {
		t.Fatalf("restore of nothing known: %v, want %v", got, want)
	}
}

func boolsToStrings(b []bool) []string {
	s := make([]string, len(b))
	for i, v := range b {
		s[i] = strconv.FormatBool(v)
	}
	return s
}
