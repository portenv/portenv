// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/local"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// The plan's slow uplink: about 400 kbit/s, 150 ms each way.
const (
	slowRate  = 50_000 // bytes a second
	slowDelay = 150 * time.Millisecond
)

// TestMain runs this test binary as the program in a test terminal when
// PORTENV_TERMINAL_HELPER says which.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PORTENV_TERMINAL_HELPER"); mode != "" {
		terminalHelper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// terminalHelper is a program in a box's terminal:
//
//	sink:N  reads N bytes in raw mode, slowly, then prints "sha <hex> <n>"
//	head:N  reads N bytes in raw mode and exits, like head -c N
//	ticker  prints "tick <unix nanoseconds>" every 200 ms, forever
func terminalHelper(mode string) {
	if mode == "ticker" {
		for {
			fmt.Printf("tick %d\n", time.Now().UnixNano())
			time.Sleep(200 * time.Millisecond)
		}
	}
	kind, count, _ := strings.Cut(mode, ":")
	want, err := strconv.Atoi(count)
	if err != nil {
		os.Exit(2)
	}
	if _, err := term.MakeRaw(0); err != nil {
		os.Exit(2)
	}
	fmt.Print("ready\n")
	if kind == "head" {
		if _, err := io.ReadFull(os.Stdin, make([]byte, want)); err != nil {
			os.Exit(2)
		}
		os.Exit(0) // at once: before attach's stdin closes
	}
	h, buf, got := sha256.New(), make([]byte, 4096), 0
	for got < want {
		time.Sleep(20 * time.Millisecond) // a slow reader: the PTY holds input when the client finishes
		n, err := os.Stdin.Read(buf[:min(len(buf), want-got)])
		h.Write(buf[:n])
		got += n
		if err != nil {
			break
		}
	}
	fmt.Printf("sha %s %d\n", hex.EncodeToString(h.Sum(nil)), got)
}

// ptyAgent is a box agent whose terminal runs the helper on a real PTY,
// through the agent's own relay.
type ptyAgent struct {
	agentv1.UnimplementedAgentServiceServer
	mode string
}

func (p *ptyAgent) Terminal(stream agentv1.AgentService_TerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	size := open.GetSize()
	cmd := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- this test binary
	cmd.Env = append(os.Environ(), "PORTENV_TERMINAL_HELPER="+p.mode)
	return agent.ServeTerminal(stream, cmd, uint16(size.GetCols()), uint16(size.GetRows()), open.GetInputPiped()) // #nosec G115 -- terminal sizes
}

// slowLink is a TCP proxy that holds each direction to slowRate and delays
// it by slowDelay, like the SSH forward to a server on a slow uplink.
type slowLink struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newSlowLink(t *testing.T, target string) *slowLink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &slowLink{ln: ln}
	t.Cleanup(l.cut)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			l.mu.Lock()
			l.conns = append(l.conns, c, u)
			l.mu.Unlock()
			go slowPump(c, u)
			go slowPump(u, c)
		}
	}()
	return l
}

// slowPump copies src to dst at slowRate, each piece arriving slowDelay
// after it was sent.
func slowPump(src, dst net.Conn) {
	type piece struct {
		b  []byte
		at time.Time
	}
	q := make(chan piece, 4096)
	go func() {
		defer func() { _ = dst.Close() }()
		for p := range q {
			time.Sleep(time.Until(p.at))
			if _, err := dst.Write(p.b); err != nil {
				return
			}
		}
	}()
	defer close(q)
	buf := make([]byte, 1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			time.Sleep(time.Duration(n) * time.Second / slowRate)
			q <- piece{append([]byte(nil), buf[:n]...), time.Now().Add(slowDelay)}
		}
		if err != nil {
			return
		}
	}
}

// cut drops the link, as when the network goes.
func (l *slowLink) cut() {
	_ = l.ln.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
}

// slowBox runs portenvd with acme-api open, its agent a ptyAgent running
// mode behind a slowLink. It returns portenvd's client and the link.
func slowBox(t *testing.T, mode string) (daemonv1.DaemonServiceClient, *slowLink) {
	t.Helper()
	t.Setenv("PORTENV_KEYS", "file")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, &ptyAgent{mode: mode})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	link := newSlowLink(t, ln.Addr().String())
	conn, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", link.ln.Addr().String())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, c := servingWith(t, func(s *Server) {
		boxWithState(t, s, "")
		s.open["acme-api"] = &openBox{
			closed: true,
			sess:   &local.Session{Cfg: local.BoxConfig{ID: "box-1", Name: "acme-api"}},
			agent:  agentv1.NewAgentServiceClient(conn),
		}
	})
	return c, link
}

// pipeOut is attach's stdout as a pipe (not a terminal), read line by line
// as it arrives.
type pipeOut struct {
	w     *os.File
	lines chan string
	done  chan struct{}
	mu    sync.Mutex
	all   bytes.Buffer
}

func newPipeOut(t *testing.T) *pipeOut {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeOut{w: w, lines: make(chan string, 1024), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			p.mu.Lock()
			p.all.WriteString(sc.Text() + "\n")
			p.mu.Unlock()
			select {
			case p.lines <- strings.TrimRight(sc.Text(), "\r"):
			default:
			}
		}
	}()
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	return p
}

// text closes the pipe and returns everything written to it.
func (p *pipeOut) text() string {
	_ = p.w.Close()
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.all.String()
}

// waitLine waits for a line starting with prefix; "" when none came.
func (p *pipeOut) waitLine(prefix string, within time.Duration) string {
	deadline := time.After(within)
	for {
		select {
		case l := <-p.lines:
			if strings.HasPrefix(l, prefix) {
				return l
			}
		case <-deadline:
			return ""
		}
	}
}

func openMain() *daemonv1.TerminalOpen {
	return &daemonv1.TerminalOpen{Box: "acme-api", Session: "main", Size: &daemonv1.TerminalSize{Cols: 80, Rows: 24}}
}

// TestAttachOverASlowLinkDeliversEverything (#53): a few MB streamed into
// attach over a slow, high-latency link to the box, then stdin closes.
// Every byte reaches the program in the box (by SHA-256) before attach
// ends with no error. Before the fix the agent closed the PTY the moment
// the client finished sending, dropping what the program hadn't read yet.
func TestAttachOverASlowLinkDeliversEverything(t *testing.T) {
	size := 2 << 20
	if testing.Short() {
		size = 256 << 10
	}
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	c, _ := slowBox(t, fmt.Sprintf("sink:%d", size))
	out := newPipeOut(t)
	in, feed := io.Pipe()
	go func() {
		out.waitLine("ready", 30*time.Second)
		_, _ = feed.Write(data)
		_ = feed.Close() // stdin closes
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	if err := AttachTerminal(ctx, c, openMain(), Attach{In: in, Out: out.w}); err != nil {
		t.Fatalf("attach: %v, want nil once everything arrived", err)
	}
	t.Logf("%d bytes over the slow link in %s", size, time.Since(start).Round(time.Second))
	want := fmt.Sprintf("sha %s %d", hex.EncodeToString(sum[:]), size)
	if got := out.text(); !strings.Contains(got, want) {
		t.Fatalf("the box didn't get every byte before attach ended: output %q, want %q", tail(got), want)
	}
}

// TestAttachSaysWhenTheProgramExitsFirst (R-0051): 500 bytes piped into
// a program that reads 10 and exits (head -c 10). attach ends with an
// error saying 490 didn't arrive (a non-zero exit), not with 0.
func TestAttachSaysWhenTheProgramExitsFirst(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("macOS discards a terminal's unread input when its program exits; the agent runs on Linux")
	}
	c, _ := slowBox(t, "head:10")
	out := newPipeOut(t)
	in, feed := io.Pipe()
	go func() {
		out.waitLine("ready", 30*time.Second)
		_, _ = feed.Write(make([]byte, 500))
		_ = feed.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := AttachTerminal(ctx, c, openMain(), Attach{In: in, Out: out.w})
	if err == nil || status.Convert(err).Message() != "490 bytes of input didn't reach the box's terminal: the program there ended first" {
		t.Fatalf("attach: %v, want 490 bytes reported", err)
	}
}

// TestAttachTypingAfterTheEndIsNotDataLoss (R-0053): at a terminal, keys
// still unread when the program exits, and keys typed after, are normal:
// attach ends with no error and no data-loss line.
func TestAttachTypingAfterTheEndIsNotDataLoss(t *testing.T) {
	c, _ := slowBox(t, "head:10")
	out := newPipeOut(t)
	in, feed := io.Pipe()
	defer func() { _ = feed.Close() }()
	go func() {
		out.waitLine("ready", 30*time.Second)
		_, _ = feed.Write(make([]byte, 500))
		time.Sleep(time.Second)
		_, _ = feed.Write([]byte("more keys"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := AttachTerminal(ctx, c, openMain(), Attach{In: in, Out: out.w, TTY: true}); err != nil {
		t.Fatalf("attach at a terminal: %v, want nil", err)
	}
}

// TestAttachSaysWhenTheBoxLinkIsCut: the link to the box goes mid-stream.
// attach ends with an error (a non-zero exit), never as if all arrived.
func TestAttachSaysWhenTheBoxLinkIsCut(t *testing.T) {
	size := 2 << 20
	c, link := slowBox(t, fmt.Sprintf("sink:%d", size))
	out := newPipeOut(t)
	in, feed := io.Pipe()
	go func() {
		out.waitLine("ready", 30*time.Second)
		go func() {
			time.Sleep(2 * time.Second)
			link.cut()
		}()
		_, _ = feed.Write(make([]byte, size))
		_ = feed.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := AttachTerminal(ctx, c, openMain(), Attach{In: in, Out: out.w})
	if status.Code(err) != codes.Unavailable || status.Convert(err).Message() != AgentUnavailable {
		t.Fatalf("link cut mid-stream: %v, want Unavailable: %s", err, AgentUnavailable)
	}
	if ctx.Err() != nil {
		t.Fatal("attach waited for the deadline instead of ending when the link went")
	}
}

// TestAttachOutputArrivesAsItIsPrinted: with stdout a pipe, not a
// terminal, each line reaches it within a second of being printed while
// the session stays open (over the slow link).
func TestAttachOutputArrivesAsItIsPrinted(t *testing.T) {
	c, _ := slowBox(t, "ticker")
	out := newPipeOut(t)
	in, feed := io.Pipe()
	defer func() { _ = feed.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- AttachTerminal(ctx, c, openMain(), Attach{In: in, Out: out.w}) }()
	for i := range 5 {
		l := out.waitLine("tick ", 10*time.Second)
		if l == "" {
			t.Fatalf("no tick %d within 10s", i)
		}
		ns, err := strconv.ParseInt(strings.TrimPrefix(l, "tick "), 10, 64)
		if err != nil {
			t.Fatalf("line %q", l)
		}
		if late := time.Since(time.Unix(0, ns)); late > time.Second {
			t.Fatalf("tick %d reached stdout %s after it was printed, want under 1s", i, late.Round(time.Millisecond))
		}
	}
	cancel()
	<-done
}

// TestAttachCtrlC: at a terminal, Ctrl-C as a signal goes to the box as a
// keystroke. With stdin piped, the first stops reading and ends once the
// box has everything, with an error (a non-zero exit) that says so.
func TestAttachCtrlC(t *testing.T) {
	t.Run("terminal", func(t *testing.T) {
		c, _ := slowBox(t, "sink:1")
		out := newPipeOut(t)
		in, feed := io.Pipe()
		defer func() { _ = feed.Close() }()
		intr := make(chan struct{}, 1)
		go func() {
			out.waitLine("ready", 30*time.Second)
			intr <- struct{}{}
		}()
		err := AttachTerminal(context.Background(), c, openMain(), Attach{In: in, Out: out.w, TTY: true, Interrupt: intr})
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		sum := sha256.Sum256([]byte{3})
		if got := out.text(); !strings.Contains(got, "sha "+hex.EncodeToString(sum[:])+" 1") {
			t.Fatalf("the box didn't get Ctrl-C: %q", tail(got))
		}
	})
	t.Run("piped", func(t *testing.T) {
		c, _ := slowBox(t, "sink:1000000")
		out := newPipeOut(t)
		in, feed := io.Pipe()
		defer func() { _ = feed.Close() }()
		intr := make(chan struct{}, 1)
		go func() {
			out.waitLine("ready", 30*time.Second)
			_, _ = feed.Write(make([]byte, 10000))
			time.Sleep(200 * time.Millisecond) // sent, not just read
			intr <- struct{}{}
		}()
		err := AttachTerminal(context.Background(), c, openMain(), Attach{In: in, Out: out.w, Interrupt: intr})
		if err == nil || err.Error() != "interrupted; everything read before Ctrl-C reached the box" {
			t.Fatalf("attach: %v", err)
		}
	})
}

func tail(s string) string {
	if len(s) > 200 {
		return "…" + s[len(s)-200:]
	}
	return s
}
