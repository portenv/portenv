// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// tabTermHarness serves the agent API (without the channel's TLS and token,
// which channel_test covers for every call) on a test tmux whose tabs start
// cmd, with control-mode clients.
type tabTermHarness struct {
	t   *testing.T
	c   agentv1.AgentServiceClient
	raw func(args ...string) string
	tb  tabs
	srv *apiServer
}

func newTabTermHarness(t *testing.T, cmd string) *tabTermHarness {
	t.Helper()
	tb, raw, cmdf := testTabsCmd(t)
	raw("new-session", "-d", "-s", "boot", "sleep 600")
	raw("set-option", "-g", "default-command", cmd)
	raw("set-option", "-g", "history-limit", "100000")
	if err := tb.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw("kill-session", "-t", "=boot")
	s := &apiServer{
		tmux:    func(Config) (tmuxFunc, error) { return tb.tmux, nil },
		tmuxCmd: func(Config) (tmuxCmdFunc, error) { return cmdf, nil },
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, s)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///tabterm",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(8<<20)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &tabTermHarness{t: t, c: agentv1.NewAgentServiceClient(conn), raw: raw, tb: tb, srv: s}
}

func (h *tabTermHarness) firstTab() string {
	return strings.Fields(h.raw("list-windows", "-t", "=main:", "-F", "#{window_id}"))[0]
}

// tabTerm is an open stream and what it has received.
type tabTerm struct {
	t      *testing.T
	s      agentv1.AgentService_TabTerminalClient
	events chan *agentv1.TabTerminalResponse
	err    error // why the stream ended, once events is closed
}

func (h *tabTermHarness) open(ctx context.Context, tab, author string, cols, rows uint32) *tabTerm {
	h.t.Helper()
	tt := h.openPaused(ctx, tab, author, cols, rows)
	tt.read()
	return tt
}

// openPaused opens a stream that reads nothing until read is called.
func (h *tabTermHarness) openPaused(ctx context.Context, tab, author string, cols, rows uint32) *tabTerm {
	h.t.Helper()
	s, err := h.c.TabTerminal(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := s.Send(&agentv1.TabTerminalRequest{Msg: &agentv1.TabTerminalRequest_Open{Open: &agentv1.TabTerminalOpen{
		Session: "main", Tab: tab, Author: author, Size: &agentv1.TerminalSize{Cols: cols, Rows: rows},
	}}}); err != nil {
		h.t.Fatal(err)
	}
	return &tabTerm{t: h.t, s: s, events: make(chan *agentv1.TabTerminalResponse, 100000)}
}

func (tt *tabTerm) read() {
	go func() {
		defer close(tt.events)
		for {
			m, err := tt.s.Recv()
			if err != nil {
				tt.err = err
				return
			}
			tt.events <- m
		}
	}()
}

func (tt *tabTerm) next(within time.Duration) *agentv1.TabTerminalResponse {
	tt.t.Helper()
	select {
	case m, ok := <-tt.events:
		if !ok {
			if !errors.Is(tt.err, io.EOF) {
				tt.t.Logf("the stream ended: %v", tt.err)
			}
			return nil
		}
		return m
	case <-time.After(within):
		tt.t.Fatal("nothing came")
	}
	return nil
}

func (tt *tabTerm) input(s string) {
	tt.t.Helper()
	if err := tt.s.Send(&agentv1.TabTerminalRequest{Msg: &agentv1.TabTerminalRequest_Input{Input: []byte(s)}}); err != nil {
		tt.t.Fatal(err)
	}
}

// until reads until the output seen so far satisfies ok; it returns every
// message read.
func (tt *tabTerm) until(within time.Duration, what string, ok func(out []byte) bool) []*agentv1.TabTerminalResponse {
	tt.t.Helper()
	var msgs []*agentv1.TabTerminalResponse
	var out []byte
	end := time.Now().Add(within)
	for !ok(out) {
		left := time.Until(end)
		if left <= 0 {
			tt.t.Fatalf("%s: not within %s; output %q", what, within, tail(out))
		}
		m := tt.next(left)
		if m == nil {
			tt.t.Fatalf("%s: the stream ended; output %q", what, tail(out))
		}
		msgs = append(msgs, m)
		out = append(out, m.GetOutput()...)
	}
	return msgs
}

func tail(b []byte) []byte { return b[max(0, len(b)-300):] }

// A tab terminal starts with a reset: the tab's history and screen, the
// marks of the commands before it on their lines, and their authors; then
// typed input runs, and its command's author comes before the output that
// completes the command's mark.
func TestTabTerminalSeedsThenStreams(t *testing.T) {
	h := newTabTermHarness(t, newShellCommand(t, "zsh"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tab := h.firstTab()
	// A first stream runs two commands, as Retnan.
	a := h.open(ctx, tab, "Retnan", 80, 24)
	if m := a.next(5 * time.Second); m.GetReset_() == nil {
		t.Fatalf("the first message isn't a reset: %v", m)
	}
	for _, c := range []string{"echo one", "echo two"} {
		a.input(c + "\r")
		var authorFirst bool
		msgs := a.until(5*time.Second, c, func(out []byte) bool { return bytes.Contains(out, []byte(strings.TrimPrefix(c, "echo ")+"\r\n")) })
		for _, m := range msgs {
			if au := m.GetAuthor(); au != nil {
				if au.GetName() != "Retnan" {
					t.Errorf("author %q", au.GetName())
				}
				authorFirst = true
			}
			if bytes.Contains(m.GetOutput(), []byte("\x1b]133;C")) && !authorFirst {
				t.Errorf("%s: the command's mark came before its author", c)
			}
		}
		if !authorFirst {
			t.Errorf("%s: no author", c)
		}
	}
	time.Sleep(300 * time.Millisecond)
	// A second stream's reset carries both commands with their marks and
	// authors on the seed's lines.
	b := h.open(ctx, tab, "", 80, 24)
	r := b.next(5 * time.Second).GetReset_()
	if r == nil {
		t.Fatal("no reset")
	}
	lines := strings.Split(string(r.GetSeed()), "\r\n")
	cmds := 0
	for _, l := range lines {
		if strings.Contains(l, "\x1b]133;C") {
			cmds++
		}
	}
	if cmds < 2 {
		t.Errorf("the seed carries %d command marks, want 2", cmds)
	}
	if len(r.GetAuthors()) != 2 {
		t.Fatalf("reset authors %v, want 2", r.GetAuthors())
	}
	for _, au := range r.GetAuthors() {
		l := lines[au.GetLine()]
		if !strings.Contains(l, "\x1b]133;C") || au.GetName() != "Retnan" {
			t.Errorf("author %v on seed line %q", au, l)
		}
	}
	// A command typed with no author records none.
	b.input("echo three\r")
	for _, m := range b.until(5*time.Second, "three", func(out []byte) bool { return bytes.Contains(out, []byte("three\r\n")) }) {
		if m.GetAuthor() != nil {
			t.Errorf("an author %v for a stream without one", m.GetAuthor())
		}
	}
}

var (
	counterRE = regexp.MustCompile(`n(\d+)\r\n`)
	csiRE     = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
)

// No output is lost or repeated between a reset's seed and what follows:
// a pane printing a counter, opened again and again while it prints.
func TestTabTerminalResetLosesAndRepeatsNothing(t *testing.T) {
	h := newTabTermHarness(t, "sh -c 'i=0; while :; do i=$((i+1)); echo n$i; sleep 0.001; done'")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tab := h.firstTab()
	for !strings.Contains(h.raw("capture-pane", "-p", "-t", tab), "n30") {
		time.Sleep(20 * time.Millisecond)
	}
	for round := range 10 {
		tt := h.open(ctx, tab, "", 80, 24)
		r := tt.next(5 * time.Second).GetReset_()
		if r == nil {
			t.Fatal("no reset")
		}
		seed := csiRE.ReplaceAll(r.GetSeed(), nil)
		ms := counterRE.FindAllSubmatch(seed, -1)
		if len(ms) == 0 {
			t.Fatalf("round %d: no counter in the seed %q", round, seed)
		}
		last, _ := strconv.Atoi(string(ms[len(ms)-1][1]))
		// The seed's last line may be partial: the output continues it.
		seedTail := seed[bytes.LastIndex(seed, []byte("\r\n"))+2:]
		var out []byte
		tt.until(5*time.Second, "output after the seed", func(o []byte) bool { out = o; return len(counterRE.FindAll(o, -1)) >= 20 })
		joined := append(append([]byte(nil), seedTail...), out...)
		got := counterRE.FindAllSubmatch(joined, -1)
		first, _ := strconv.Atoi(string(got[0][1]))
		if first != last+1 {
			t.Fatalf("round %d: seed ends at n%d, output continues at n%d (seed tail %q, output %q)", round, last, first, seedTail, out[:min(40, len(out))])
		}
		for i := 1; i < len(got); i++ {
			a, _ := strconv.Atoi(string(got[i-1][1]))
			b, _ := strconv.Atoi(string(got[i][1]))
			if b != a+1 {
				t.Fatalf("round %d: n%d then n%d", round, a, b)
			}
		}
		_ = tt.s.CloseSend()
	}
}

// Input reaches the program unchanged: every byte value, in order, in a
// paste far larger than one send-keys, even from copy mode.
func TestTabTerminalInputArrivesUnchanged(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "got")
	const n = 64 << 10
	h := newTabTermHarness(t, fmt.Sprintf("sh -c 'stty raw -echo; head -c %d > %s; sleep 600'", n, out))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tab := h.firstTab()
	tt := h.open(ctx, tab, "", 80, 24)
	if tt.next(5*time.Second).GetReset_() == nil {
		t.Fatal("no reset")
	}
	// A program put the pane in copy mode: the input still reaches it.
	h.raw("copy-mode", "-t", tab)
	want := make([]byte, n)
	for i := range want {
		want[i] = byte(i * 7)
	}
	for i := 0; i < n; i += 5000 {
		if err := tt.s.Send(&agentv1.TabTerminalRequest{Msg: &agentv1.TabTerminalRequest_Input{Input: want[i:min(n, i+5000)]}}); err != nil {
			t.Fatal(err)
		}
	}
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		got, _ := os.ReadFile(out) // #nosec G304 -- test file
		if len(got) == n {
			if !bytes.Equal(got, want) {
				i := 0
				for i < n && got[i] == want[i] {
					i++
				}
				t.Fatalf("the input changed at byte %d", i)
			}
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%d of %d bytes arrived", len(got), n)
		}
	}
}

// Opening a tab that's gone ends at once; a tab closed while it streams
// ends after its last output.
func TestTabTerminalEndsWhenTheTabGoes(t *testing.T) {
	h := newTabTermHarness(t, "sh -c 'echo started; exec sleep 600'")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	gone := h.open(ctx, "@9999", "", 80, 24)
	m := gone.next(2 * time.Second)
	if m.GetEnd() != agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED || time.Since(start) > time.Second {
		t.Fatalf("opening a gone tab: %v after %s", m, time.Since(start))
	}
	h.raw("new-window", "-d", "-t", "=main:", "sh -c 'sleep 0.5; echo last-words; sleep 0.2'")
	tab := strings.Fields(h.raw("list-windows", "-t", "=main:", "-F", "#{window_id}"))[1]
	tt := h.open(ctx, tab, "", 80, 24)
	var out []byte
	for {
		m := tt.next(5 * time.Second)
		if m == nil {
			t.Fatal("the stream ended without saying why")
		}
		out = append(out, m.GetOutput()...)
		if m.GetEnd() != agentv1.TabTerminalEnd_TAB_TERMINAL_END_UNSPECIFIED {
			if m.GetEnd() != agentv1.TabTerminalEnd_TAB_TERMINAL_END_TAB_CLOSED || !bytes.Contains(out, []byte("last-words")) {
				t.Fatalf("end %v after output %q", m.GetEnd(), out)
			}
			return
		}
	}
}

// A slow reader falls behind: its stream resets with skipped set, and the
// output after it continues from the new seed (ADR 0020, decision 11).
func TestTabTerminalSkipsAndSaysSoWhenTheReaderFallsBehind(t *testing.T) {
	h := newTabTermHarness(t, "sh -c 'sleep 1; i=0; while [ $i -lt 200000 ]; do i=$((i+1)); echo line-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; done; sleep 600'")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub, err := h.srv.ctlHub()
	if err != nil {
		t.Fatal(err)
	}
	hub.subBytes = 64 << 10
	// The reader takes the first reset, then nothing while the pane
	// prints far more than the subscriber holds.
	tt := h.openPaused(ctx, h.firstTab(), "", 80, 24)
	if m, err := tt.s.Recv(); err != nil || m.GetReset_() == nil || m.GetReset_().GetSkipped() {
		t.Fatalf("first: %v, %v", m, err)
	}
	time.Sleep(4 * time.Second)
	tt.read()
	for end := time.Now().Add(60 * time.Second); time.Now().Before(end); {
		m := tt.next(time.Until(end))
		if m == nil {
			break
		}
		if r := m.GetReset_(); r != nil {
			if !r.GetSkipped() {
				t.Fatal("a reset after falling behind isn't marked skipped")
			}
			return
		}
	}
	t.Fatal("the stream never fell behind")
}

// A seed puts each mark of the parser's current epoch on its own line,
// even among identical prompts, and leaves older epochs' marks out (they
// can't be aligned); authors go only with a command mark it carries.
func TestBuildSeedPutsMarksOnTheirLines(t *testing.T) {
	disp := []string{"%1 80 3 2 2 2 0 0 2 0 1 0 0 0 0 0 0 0 1 0 0"}
	main := []string{"$ ls", "a", "$ ls", "b", "$ "}
	snap := &paneSnapshot{cursorLine: 104, epoch: 2, marks: []paneMark{
		{kind: markCommand, line: 100, epoch: 2},
		{kind: markCommand, line: 101, epoch: 1}, // before a full-screen erase
		{kind: markCommand, line: 102, epoch: 2},
		{kind: markPrompt, line: 104, epoch: 2},
	}}
	authors := []recordedAuthor{
		{line: 100, epoch: 2, name: "Retnan"},
		{line: 101, epoch: 1, name: "old"},
		{line: 104, epoch: 2, name: "no command here"},
	}
	seed, size, got, base, err := buildSeed(disp, main, []string{""}, snap, authors)
	if err != nil {
		t.Fatal(err)
	}
	if size != [2]int{80, 3} || base != 100 {
		t.Fatalf("size %v base %d", size, base)
	}
	lines := strings.Split(string(seed), "\x1b[0m\r\n")
	c := markSequence(paneMark{kind: markCommand})
	for i, l := range lines {
		want := i == 0 || i == 2
		if strings.Contains(l, c) != want {
			t.Errorf("seed line %d %q: command mark %v, want %v", i, l, !want, want)
		}
	}
	if !strings.Contains(lines[4], markSequence(paneMark{kind: markPrompt})) {
		t.Errorf("no prompt mark on the last line: %q", lines[4])
	}
	if len(got) != 1 || got[0].GetLine() != 0 || got[0].GetName() != "Retnan" {
		t.Errorf("authors %v", got)
	}
}

// A seed brings back what shapes the output after it: the scroll region,
// origin mode, autowrap, the pen, bracketed paste and the cursor. A viewer
// fed the seed and the output that follows shows what tmux shows.
func TestTabTerminalSeedRestoresTheTerminalsState(t *testing.T) {
	dir := t.TempDir()
	ready, goFile := filepath.Join(dir, "ready"), filepath.Join(dir, "go")
	script := filepath.Join(dir, "prog.sh")
	if err := os.WriteFile(script, []byte(`printf 'first\n'
while [ ! -e `+ready+` ]; do sleep 0.05; done
printf '\033[2;10r\033[?6h\033[?7l\033[?2004h\033[31m'
printf 'top'
while [ ! -e `+goFile+` ]; do sleep 0.05; done
printf '\033[5;1Hafter'
printf '\033[9;1H'
for i in 1 2 3 4; do printf 'line%s\n' $i; done
printf '\033[3;70H0123456789ABCDEFGHIJ'
printf '\033[1;1HEND'
exec sleep 600
`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newTabTermHarness(t, "sh "+script)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tab := h.firstTab()
	// The pane's parser follows it from the first stream on: the pen it
	// sets after that is known (one set before the session's control
	// client started isn't; the app's tab watch keeps that client up).
	first := h.open(ctx, h.firstTab(), "", 80, 24)
	if first.next(5*time.Second).GetReset_() == nil {
		t.Fatal("no first reset")
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	first.until(5*time.Second, "top", func(o []byte) bool { return bytes.Contains(o, []byte("top")) })
	tt := h.open(ctx, tab, "", 80, 24)
	r := tt.next(5 * time.Second).GetReset_()
	if r == nil {
		t.Fatal("no reset")
	}
	if !bytes.Contains(r.GetSeed(), []byte("\x1b[?2004h")) {
		t.Error("the seed doesn't turn bracketed paste on")
	}
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out []byte
	tt.until(5*time.Second, "END", func(o []byte) bool { out = o; return bytes.Contains(o, []byte("END")) })
	time.Sleep(100 * time.Millisecond)
	e := vt.NewEmulator(80, 24) // nothing here asks it for a reply
	_, _ = e.Write(r.GetSeed())
	_, _ = e.Write(out)
	want := strings.Split(strings.TrimRight(h.raw("capture-pane", "-p", "-t", tab), "\n"), "\n")
	for y := range 24 {
		var b strings.Builder
		for x := range 80 {
			if c := e.CellAt(x, y); c != nil && c.Content != "" {
				b.WriteString(c.Content)
			} else {
				b.WriteByte(' ')
			}
		}
		got := strings.TrimRight(b.String(), " ")
		w := ""
		if y < len(want) {
			w = strings.TrimRight(want[y], " ")
		}
		if got != w {
			t.Errorf("row %d: viewer %q, tmux %q", y, got, w)
		}
	}
	// END is at the scroll region's top (origin mode), in the pen.
	if c := e.CellAt(0, 1); c.Content != "E" || c.Style.Fg != ansi.Red {
		t.Errorf("END's first cell %+v, want a red E", c)
	}
	cur := strings.Fields(h.raw("display", "-p", "-t", tab, "#{cursor_x} #{cursor_y}"))
	if p := e.CursorPosition(); strconv.Itoa(p.X) != cur[0] || strconv.Itoa(p.Y) != cur[1] {
		t.Errorf("viewer cursor %v, tmux %v", p, cur)
	}
}

// A reset taken while the output stands inside an escape sequence or a
// UTF-8 character ends with its start: the output after the seed
// completes it, and the viewer shows what tmux shows.
func TestTabTerminalResetMidSequence(t *testing.T) {
	dir := t.TempDir()
	step := func(n int) string { return filepath.Join(dir, strconv.Itoa(n)) }
	script := filepath.Join(dir, "prog.sh")
	if err := os.WriteFile(script, []byte(`while [ ! -e `+step(0)+` ]; do sleep 0.05; done
printf 'a\303'
while [ ! -e `+step(1)+` ]; do sleep 0.05; done
printf '\251b\033[3'
while [ ! -e `+step(2)+` ]; do sleep 0.05; done
printf '1mRED\033[0mEND'
exec sleep 600
`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newTabTermHarness(t, "sh "+script)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tab := h.firstTab()
	first := h.open(ctx, tab, "", 80, 24)
	if first.next(5*time.Second).GetReset_() == nil {
		t.Fatal("no first reset")
	}
	touch := func(n int) {
		if err := os.WriteFile(step(n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	touch(0)
	first.until(5*time.Second, "a", func(o []byte) bool { return bytes.Contains(o, []byte("a\303")) })
	// Mid-character: the seed ends with é's first byte.
	a := h.open(ctx, tab, "", 80, 24)
	ra := a.next(5 * time.Second).GetReset_()
	touch(1)
	first.until(5*time.Second, "b", func(o []byte) bool { return bytes.Contains(o, []byte("\033[3")) })
	// Mid-escape: the seed ends with ESC [ 3.
	b := h.open(ctx, tab, "", 80, 24)
	rb := b.next(5 * time.Second).GetReset_()
	touch(2)
	var outA, outB []byte
	a.until(5*time.Second, "END", func(o []byte) bool { outA = o; return bytes.Contains(o, []byte("END")) })
	b.until(5*time.Second, "END", func(o []byte) bool { outB = o; return bytes.Contains(o, []byte("END")) })
	for _, v := range []struct {
		name      string
		seed, out []byte
	}{{"mid-character", ra.GetSeed(), outA}, {"mid-escape", rb.GetSeed(), outB}} {
		e := vt.NewEmulator(80, 24)
		_, _ = e.Write(v.seed)
		_, _ = e.Write(v.out)
		var row strings.Builder
		for x := range 9 {
			row.WriteString(e.CellAt(x, 0).Content)
		}
		if got := row.String(); got != "aébREDEND" {
			t.Errorf("%s: the viewer shows %q, want aébREDEND (seed ends %q)", v.name, got, tail(v.seed))
		}
		if c := e.CellAt(3, 0); c.Style.Fg != ansi.Red {
			t.Errorf("%s: R is %v, want red", v.name, c.Style.Fg)
		}
	}
}
