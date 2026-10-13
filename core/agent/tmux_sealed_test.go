// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// tmux's second line against the Mac's clipboard (R-0133): set-clipboard
// and allow-passthrough are off, so neither copy mode's copies nor a
// program's own OSC 52 reach an attached client. (tmux 3.4 still sends
// `tmux set-buffer -w` as OSC 52 with set-clipboard off; only the app's
// ClipboardGuard stops that, and its tests cover it.)
func TestTmuxNeverReachesTheMacsClipboard(t *testing.T) {
	tb, raw := testTabs(t)
	ctx := context.Background()
	if err := tb.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	for opt, want := range map[string]string{"set-clipboard": "off", "allow-passthrough": "off"} {
		if got := raw("show-options", "-gv", opt); got != want {
			t.Errorf("%s is %q, want %q", opt, got, want)
		}
	}

	// An attached client, as `portenv attach` runs one, with a terminal
	// tmux knows can set the clipboard.
	sock := raw("display-message", "-p", "#{socket_path}")
	cmd := exec.Command("tmux", "-S", sock, "attach-session", "-t", "=main") // #nosec G204 -- test
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C.UTF-8")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = f.Close(); _ = cmd.Wait() })
	var mu sync.Mutex
	var out bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	seen := func(s string) bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), s)
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}

	// A program writes OSC 52 itself and prints a line; then copy mode
	// copies that line, as a program can make it do (copy-mode, then
	// copy-selection), which set-clipboard external would send on.
	marker := "PORTENV-OSC52-" + strings.Repeat("x", 8)
	b64 := base64.StdEncoding.EncodeToString([]byte(marker))
	raw("send-keys", "-t", "=main:", "-l", "printf '\\033]52;c;"+b64+"\\007'; echo DONE-$((40+2))")
	raw("send-keys", "-t", "=main:", "Enter")
	waitFor("the probe's line", func() bool { return seen("DONE-42") })
	raw("copy-mode", "-t", "=main:")
	raw("send-keys", "-t", "=main:", "-X", "cursor-up")
	raw("send-keys", "-t", "=main:", "-X", "select-line")
	raw("send-keys", "-t", "=main:", "-X", "copy-selection-and-cancel")
	if got := raw("show-buffer"); !strings.Contains(got, "DONE") {
		t.Fatalf("copy mode didn't copy the line: %q", got)
	}
	time.Sleep(300 * time.Millisecond)
	if seen("\x1b]52;") {
		t.Errorf("an OSC 52 clipboard sequence reached the attached client: %q", out.String())
	}
}
