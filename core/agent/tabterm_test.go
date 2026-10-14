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
