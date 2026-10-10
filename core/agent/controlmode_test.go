// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseControlLines(t *testing.T) {
	cases := []struct {
		line string
		want ctlEvent
	}{
		{"%window-add @3", ctlEvent{kind: ctlWindowAdd, window: "@3"}},
		{"%window-close @3", ctlEvent{kind: ctlWindowClose, window: "@3"}},
		{"%unlinked-window-close @4", ctlEvent{kind: ctlWindowClose, window: "@4"}},
		{"%window-renamed @3 build and test", ctlEvent{kind: ctlWindowRenamed, window: "@3", name: "build and test"}},
		{"%exit", ctlEvent{kind: ctlExit}},
		{"%begin 1 2 0", ctlEvent{}},
		{"%sessions-changed", ctlEvent{}},
	}
	for _, c := range cases {
		got := parseControlLine(c.line)
		if got.kind != c.want.kind || got.window != c.want.window || got.name != c.want.name {
			t.Errorf("%q: got %+v, want %+v", c.line, got, c.want)
		}
	}
	e := parseControlLine(`%output %7 a\033]133;D;2\007b\134c\015\012`)
	if e.kind != ctlOutput || e.pane != "%7" || string(e.data) != "a\x1b]133;D;2\x07b\\c\r\n" {
		t.Fatalf("output: %+v %q", e, e.data)
	}
}

// ctlHarness is a tmux server on a private socket with one session, "main",
// whose tabs start the given shell, and a control-mode client attached to it.
type ctlHarness struct {
	t      *testing.T
	raw    func(args ...string) string
	mu     sync.Mutex
	shells *tabShells
	events []ctlEvent
	// lastOut is when each pane last printed (for the "quiet" signal).
	lastOut map[string]time.Time
}

// newShellCommand returns a command line starting an interactive shell with
// Portenv's OSC 133 integration and nothing else from the machine.
func newShellCommand(t *testing.T, shell string) string {
	t.Helper()
	root, err := filepath.Abs("../../images/toolbox-node/rootfs/usr/share/portenv/shell")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	path, err := exec.LookPath(shell)
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is required in CI", shell)
		}
		t.Skipf("%s not installed", shell)
	}
	switch shell {
	case "bash":
		out, err := exec.Command(path, "-c", `echo "${BASH_VERSINFO[0]}.${BASH_VERSINFO[1]}"`).Output()
		if err != nil {
			t.Fatal(err)
		}
		v := strings.TrimSpace(string(out))
		if v < "4.4" && !strings.HasPrefix(v, "5") {
			if os.Getenv("CI") != "" {
				t.Fatalf("bash %s is too old for PS0", v)
			}
			t.Skipf("bash %s is too old for PS0 (macOS ships 3.2)", v)
		}
		rc := filepath.Join(home, "rc")
		if err := os.WriteFile(rc, []byte("PS1='$ '\n. "+filepath.Join(root, "osc133.bash")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("env -i HOME=%s TERM=xterm-256color PATH=/usr/bin:/bin %s --noprofile --rcfile %s -i", home, path, rc)
	case "zsh":
		rc := filepath.Join(home, ".zshrc")
		if err := os.WriteFile(rc, []byte("PS1='%% '\nsource "+filepath.Join(root, "osc133.zsh")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// -d: no global rc files. Ubuntu's /etc/zsh/zshrc runs compinit,
		// which can stop to ask about "insecure directories" on a CI
		// runner; the test exercises only Portenv's integration.
		return fmt.Sprintf("env -i HOME=%s ZDOTDIR=%s TERM=xterm-256color PATH=/usr/bin:/bin %s -d -i", home, home, path)
	}
	t.Fatalf("no shell %s", shell)
	return ""
}

func newCtlHarness(t *testing.T, shell string) (*ctlHarness, tabs) {
	t.Helper()
	tb, raw := testTabs(t)
	cmd := newShellCommand(t, shell)
	// The server must exist before its options can be set: a placeholder
	// session holds it while "main" starts with the integrated shell.
	raw("new-session", "-d", "-s", "boot", "sleep 600")
	raw("set-option", "-g", "default-command", cmd)
	if err := tb.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw("kill-session", "-t", "=boot")
	h := &ctlHarness{t: t, raw: raw, shells: newTabShells(), lastOut: map[string]time.Time{}}
	// The control client: tmux -C on pipes, attached to the same session.
	sock := strings.TrimSpace(raw("display-message", "-p", "#{socket_path}"))
	ctx, cancel := context.WithCancel(context.Background())
	c := exec.CommandContext(ctx, "tmux", "-S", sock, "-C", "attach-session", "-t", "=main") // #nosec G204 -- test
	c.Env = append(os.Environ(), "LANG=C.UTF-8")
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); cancel(); _ = c.Wait() })
	h.refreshPanes()
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			e := parseControlLine(sc.Text())
			if e.kind == ctlOther {
				continue
			}
			h.mu.Lock()
			if e.kind == ctlOutput {
				h.lastOut[e.pane] = time.Now()
			}
			h.events = append(h.events, e)
			unknown := h.shells.apply(e)
			h.mu.Unlock()
			if unknown || e.kind == ctlWindowAdd {
				h.refreshPanes()
				if unknown {
					h.mu.Lock()
					h.shells.apply(e)
					h.mu.Unlock()
				}
			}
		}
	}()
	return h, tb
}

func (h *ctlHarness) refreshPanes() {
	out := h.raw("list-panes", "-s", "-t", "=main", "-F", "#{pane_id} #{window_id}")
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range strings.Split(out, "\n") {
		if p, w, ok := strings.Cut(l, " "); ok {
			h.shells.setPane(p, w)
		}
	}
}

// waitFor polls cond (under the lock) for up to 10 s.
func (h *ctlHarness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		ok := cond()
		h.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s; events: %+v", what, h.snapshot())
}

func (h *ctlHarness) snapshot() []ctlEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ctlEvent(nil), h.events...)
}

func (h *ctlHarness) saw(kind ctlKind, window string) bool {
	for _, e := range h.events {
		if e.kind == kind && e.window == window {
			return true
		}
	}
	return false
}

// Spike question 1: control mode tells the agent about every tab change,
// with the same window IDs the tab interface uses.
func TestControlModeSeesTabsAsWindows(t *testing.T) {
	shell := "zsh"
	if _, err := exec.LookPath("zsh"); err != nil {
		shell = "bash"
	}
	h, tb := newCtlHarness(t, shell)
	ctx := context.Background()
	tab, err := tb.open(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	id := tab.GetId()
	h.waitFor("window-add "+id, func() bool { return h.saw(ctlWindowAdd, id) })
	if err := tb.rename(ctx, id, "tests"); err != nil {
		t.Fatalf("%v; windows: %q; main: %q", err, h.raw("list-windows", "-a", "-F", "#{session_name} #{window_id} #{window_name} #{pane_dead}"), h.raw("list-windows", "-t", "=main:", "-F", "#{window_id}"))
	}
	h.waitFor("window-renamed "+id, func() bool {
		for _, e := range h.events {
			if e.kind == ctlWindowRenamed && e.window == id && e.name == "tests" {
				return true
			}
		}
		return false
	})
	if err := tb.close(ctx, id); err != nil {
		t.Fatal(err)
	}
	h.waitFor("window-close "+id, func() bool { return h.saw(ctlWindowClose, id) })
}

// Spike question 2: shell integration's OSC 133 marks reach the agent per
// tab, through control mode's %output, with exit codes, for bash and zsh.
func TestOSC133PerTabThroughControlMode(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if runtime.GOOS == "darwin" && shell == "bash" {
				t.Log("macOS's bash 3.2 can't run the integration; Linux CI runs this")
			}
			h, tb := newCtlHarness(t, shell)
			ctx := context.Background()
			first := strings.TrimSpace(h.raw("display-message", "-p", "-t", "=main:", "#{window_id}"))
			second, err := tb.open(ctx, "other")
			if err != nil {
				t.Fatal(err)
			}
			w2 := second.GetId()
			state := func(w string) (shellState, int, bool) {
				s := h.shells.shell(w)
				code, ok := s.LastExit()
				return s.State(), code, ok
			}
			// A prompt printed before the control client attached isn't
			// seen: an empty line gives a fresh one (the agent attaches its
			// client before tabs start, so it sees every prompt).
			h.raw("send-keys", "-t", first, "Enter")
			h.waitFor("prompt in "+first, func() bool { s, _, _ := state(first); return s == shellPrompt })
			h.waitFor("prompt in "+w2, func() bool { s, _, _ := state(w2); return s == shellPrompt })
			send := func(w, keys string) { h.raw("send-keys", "-t", w, keys, "Enter") }

			send(first, "false")
			h.waitFor("exit 1", func() bool { s, c, ok := state(first); return s == shellPrompt && ok && c == 1 })
			send(first, "(exit 3)")
			h.waitFor("exit 3", func() bool { s, c, ok := state(first); return s == shellPrompt && ok && c == 3 })
			send(first, "true")
			h.waitFor("exit 0", func() bool { s, c, ok := state(first); return s == shellPrompt && ok && c == 0 })
			// The integration prints nothing the person can see: a command's
			// output appears exactly as it would without it.
			send(first, "echo portenv-marker")
			h.waitFor("echo done", func() bool { return h.shells.shell(first).Commands() >= 4 })
			screen := h.raw("capture-pane", "-p", "-t", first)
			if !strings.Contains("\n"+screen+"\n", "\nportenv-marker\n") {
				t.Fatalf("the integration changed a command's output:\n%s", screen)
			}

			// A running command shows as running in its own tab only.
			send(w2, "sleep 2")
			h.waitFor("running in "+w2, func() bool { s, _, _ := state(w2); return s == shellRunning })
			if s, _, _ := state(first); s != shellPrompt {
				t.Fatalf("the other tab moved: %v", s)
			}
			h.waitFor("sleep done", func() bool { s, c, ok := state(w2); return s == shellPrompt && ok && c == 0 })
			// A program inside the command (as Claude Code runs in a tab) is
			// one long "running": OSC 133 can't see what it asks.
			if got := h.shells.shell(first).Commands(); got < 3 {
				t.Fatalf("commands in %s: %d", first, got)
			}
		})
	}
}
