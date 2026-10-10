// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

// fullScanDeadline bounds the nightly scan. It is generous on purpose: the
// scan reads every file on the box's root file system, and a busy machine
// slows it down. Not finishing within it is a failure, never a pass.
const fullScanDeadline = 20 * time.Minute

// TestChannelTokenNotOnDisk is the nightly backstop for ADR 0010's
// condition 2: it searches the box's whole root file system (everything but
// /proc, /sys and /dev, which aren't files on disk) for the channel token.
// TestChannelConditions checks the places the token could land on every
// PR; this one catches a place nobody thought of. It runs only with
// PORTENV_TEST_FULL_SCAN=1 (the nightly workflow), and it reports paths,
// never the token.
func TestChannelTokenNotOnDisk(t *testing.T) {
	if os.Getenv("PORTENV_TEST_FULL_SCAN") != "1" {
		t.Skip("the full-disk scan runs nightly (PORTENV_TEST_FULL_SCAN=1)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), fullScanDeadline+5*time.Minute)
	defer cancel()
	d, id, ch, sh := startChannelBox(t, ctx, "fullscan")

	// Every top-level directory except the pseudo file systems.
	var roots []string
	for _, e := range strings.Fields(sh("", "ls -A /")) {
		switch e {
		case "proc", "sys", "dev":
			continue
		}
		roots = append(roots, "/"+e)
	}
	if len(roots) < 5 {
		t.Fatalf("the box's root lists only %v: the scan would prove nothing", roots)
	}

	start := time.Now()
	found := searchToken(ctx, t, d, id, ch.Token, fullScanDeadline, "files", roots...)
	t.Logf("full scan of %d top-level directories took %s", len(roots), time.Since(start).Round(time.Second))
	if len(found) > 0 {
		t.Errorf("the token is in a file in the box: %s", strings.Join(found, ", "))
	}

	// The control: a decoy somewhere the targeted checks don't look must be
	// found by this scan.
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	decoy := hex.EncodeToString(b)
	path := "/opt/portenv-decoy/deep/file"
	sh("", "mkdir -p /opt/portenv-decoy/deep && printf 'x %s x\\n' "+decoy+" > "+path)
	found = searchToken(ctx, t, d, id, decoy, fullScanDeadline, "files", roots...)
	if !containsLine(found, path) {
		t.Errorf("control: a decoy at %s wasn't found by the full scan (found %q)", path, found)
	}
}
