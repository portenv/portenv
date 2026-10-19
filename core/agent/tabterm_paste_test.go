// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// pasteScript is a long paste of shell, as the owner's was (CC-0490): lines
// of heredocs whose bodies hold multibyte characters, tabs and quotes, all
// appended to out, then a line that says it's done. It returns the script
// and what out must hold after it runs.
func pasteScript(out, done string, size int) (script, want string) {
	var s, w strings.Builder
	fmt.Fprintf(&s, "rm -f %s; : > %s\n", done, out)
	words := []string{"héllo", "日本語", "naïve café", "🙂 ok", "tab\there", `"quoted" 'single' $NOT_EXPANDED ; x`, "a\\b ~", "ünïcödé ✓"}
	for block := 0; s.Len() < size; block++ {
		fmt.Fprintf(&s, "cat >> %s <<'EOF_%d'\n", out, block)
		for line := range 6 {
			l := fmt.Sprintf("block %d line %d: %s %s", block, line, words[(block+line)%len(words)], strings.Repeat("x", (block*7+line*13)%60))
			s.WriteString(l + "\n")
			w.WriteString(l + "\n")
		}
		fmt.Fprintf(&s, "EOF_%d\n", block)
	}
	fmt.Fprintf(&s, "echo pasted > %s\n", done)
	return s.String(), w.String()
}

// pasteAsTheApp sends text as MarkedTerminalView.paste does: between the
// markers, each its own input, when the reset says the helper takes
// marked pastes or the view's terminal says the program asked for
// bracketed paste; plain otherwise.
func pasteAsTheApp(t *testing.T, tt *tabTerm, r *agentv1.TabTerminalReset, viewBracketed bool, text string) {
	t.Helper()
	marked := r.GetPasteMarkers() || viewBracketed
	if marked {
		send(t, tt, pasteStart)
	}
	send(t, tt, text)
	if marked {
		send(t, tt, pasteEnd)
	}
}

// A long paste reaches the shell byte for byte (CC-0490): 10 and 48 KB of
// heredocs, pasted as the app does into an interactive bash sitting at its
// prompt, through the tab's stream.
//
// The bug: bash drew its prompt (and asked for bracketed paste) before the
// helper's client started, so the reset's seed couldn't say the mode
// (tmux 3.4 has no format for it), the app pasted unbracketed, and bash ran
// the paste line by line as typing: tabs completed, and the rest was typed
// ahead of each command and drawn over the screen.
func TestTabTerminalLongPasteReachesTheShell(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	// Readline asks for bracketed paste from bash 5 (the workspace's is
	// 5.2); macOS's bash 3.2 doesn't, and a paste there is typing.
	if v, err := exec.Command(bash, "-c", "echo ${BASH_VERSINFO[0]}").Output(); err != nil || strings.TrimSpace(string(v)) < "5" { // #nosec G204 -- test
		t.Skip("this bash has no bracketed paste")
	}
	for _, size := range []int{10 << 10, 48 << 10} {
		t.Run(fmt.Sprintf("%dKB", size>>10), func(t *testing.T) {
			dir := t.TempDir()
			out, done := filepath.Join(dir, "out"), filepath.Join(dir, "done")
			rc := filepath.Join(dir, "rc")
			if err := os.WriteFile(rc, []byte("PS1='$ '\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			h := newTabTermHarness(t, fmt.Sprintf("%s --noprofile --rcfile %s -i", bash, rc))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tab := h.firstTab()
			tt := h.open(ctx, tab, "", 80, 24)
			r := tt.next(5 * time.Second).GetReset_()
			if r == nil {
				t.Fatal("no reset")
			}
			// What the view's terminal knows of the mode: the seed, and the
			// output up to the prompt (a seed's lines lose their trailing
			// spaces).
			seen := append([]byte(nil), r.GetSeed()...)
			if !bytes.Contains(seen, []byte("$")) {
				for _, m := range tt.until(10*time.Second, "the prompt", func(o []byte) bool { return bytes.Contains(o, []byte("$")) }) {
					seen = append(seen, m.GetOutput()...)
				}
			}
			go drain(tt)
			script, want := pasteScript(out, done, size)
			pasteAsTheApp(t, tt, r, bytes.LastIndex(seen, []byte("\x1b[?2004h")) > bytes.LastIndex(seen, []byte("\x1b[?2004l")), script)
			send(t, tt, "\r")
			for end := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
				if b, _ := os.ReadFile(done); bytes.Equal(b, []byte("pasted\n")) { // #nosec G304 -- test file
					break
				}
				if time.Now().After(end) {
					got, _ := os.ReadFile(out) // #nosec G304 -- test file
					t.Fatalf("the paste didn't finish: %d of %d bytes in out; screen:\n%s", len(got), len(want), h.raw("capture-pane", "-p", "-t", tab))
				}
			}
			got, err := os.ReadFile(out) // #nosec G304 -- test file
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(want)) {
				i := 0
				for i < len(got) && i < len(want) && got[i] == want[i] {
					i++
				}
				t.Fatalf("the paste changed at byte %d of %d: got %q, want %q", i, len(want),
					got[max(0, i-40):min(len(got), i+80)], want[max(0, i-40):min(len(want), i+80)])
			}
		})
	}
}

// A marked paste into a program that never asked for bracketed paste
// arrives as its bytes alone, in order and whole: tmux adds no markers,
// keeps LF, and a character cut between two inputs and the set-buffer
// chunks comes through. Typed input before and after it keeps its place,
// and the paste's buffer is gone afterwards.
func TestTabTerminalMarkedPasteToARawProgram(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "got")
	var text strings.Builder
	for i := 0; text.Len() < 3*pasteChunk; i++ {
		fmt.Fprintf(&text, "line %d: héllo 日本語 🙂 \"q\" $X ; ~ \\ #{x} %%s\ttab\n", i)
	}
	want := "before" + text.String() + "after"
	h := newTabTermHarness(t, fmt.Sprintf("sh -c 'stty raw -echo; echo raw-now; head -c %d > %s; sleep 600'", len(want), out))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tab := h.firstTab()
	tt := h.open(ctx, tab, "", 80, 24)
	r := tt.next(5 * time.Second).GetReset_()
	if r == nil {
		t.Fatal("no reset")
	}
	if !r.GetPasteMarkers() {
		t.Fatal("the reset doesn't say pastes may be marked")
	}
	if !bytes.Contains(r.GetSeed(), []byte("raw-now")) {
		tt.until(10*time.Second, "the program's raw terminal", func(o []byte) bool { return bytes.Contains(o, []byte("raw-now")) })
	}
	go drain(tt)
	h.raw("copy-mode", "-t", tab) // a mode doesn't swallow it
	send(t, tt, "before")
	send(t, tt, pasteStart)
	cut := strings.Index(text.String(), "日") + 1
	send(t, tt, text.String()[:cut])
	send(t, tt, text.String()[cut:])
	send(t, tt, pasteEnd)
	send(t, tt, "after")
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		got, _ := os.ReadFile(out) // #nosec G304 -- test file
		if len(got) == len(want) {
			if string(got) != want {
				i := 0
				for i < len(got) && got[i] == want[i] {
					i++
				}
				t.Fatalf("the paste changed at byte %d: got %q, want %q", i, got[max(0, i-20):min(len(got), i+40)], want[max(0, i-20):min(len(want), i+40)])
			}
			if bufs, _ := h.tb.tmux(context.Background(), "list-buffers", "-F", "#{buffer_name}"); strings.Contains(bufs, "portenv-paste") {
				t.Fatalf("the paste's buffer is left: %q", bufs)
			}
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%d of %d bytes arrived: %q", len(got), len(want), tail(got))
		}
	}
}

// tmuxQuoted's text, parsed by tmux as a command line, is the bytes it was
// given: every byte value but NUL (which pasteIn drops), and the
// characters tmux would otherwise expand, split or unquote.
func TestTmuxQuotedRoundTrips(t *testing.T) {
	_, raw, _ := testTabsCmd(t)
	raw("new-session", "-d", "-s", "q", "sleep 600")
	var all []byte
	for b := 1; b < 256; b++ {
		all = append(all, byte(b))
	}
	all = append(all, "a ; b \" $HOME ~ \\ #{pane_id} ' -x"...)
	line := "set-buffer -b x -- \"" + tmuxQuoted(all) + "\""
	if strings.ContainsAny(line, "\r\n") || strings.Contains(line, " ; ") {
		t.Fatalf("not one command line: %q", line)
	}
	dir := t.TempDir()
	conf, saved := filepath.Join(dir, "cmd.conf"), filepath.Join(dir, "saved")
	if err := os.WriteFile(conf, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw("source-file", conf)
	raw("save-buffer", "-b", "x", saved)
	got, err := os.ReadFile(saved) // #nosec G304 -- test file
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, all) {
		t.Fatalf("read back %q, want %q", got, all)
	}
}

func send(t *testing.T, tt *tabTerm, s string) {
	t.Helper()
	if err := tt.s.Send(&agentv1.TabTerminalRequest{Msg: &agentv1.TabTerminalRequest_Input{Input: []byte(s)}}); err != nil {
		t.Fatal(err)
	}
}

// drain reads the stream's output so it never backs up.
func drain(tt *tabTerm) {
	for range tt.events {
	}
}
