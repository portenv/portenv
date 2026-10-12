// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// TestMain runs this test binary as a program in a test terminal when
// PORTENV_TERMINAL_HELPER says which: stuck never reads its input; head10
// reads 10 bytes and exits, like head -c 10; late10 does the same after
// 300 ms.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PORTENV_TERMINAL_HELPER"); mode != "" {
		if _, err := term.MakeRaw(0); err != nil {
			os.Exit(2)
		}
		fmt.Print("ready\n")
		if mode == "late10" {
			time.Sleep(300 * time.Millisecond)
		}
		if mode == "head10" || mode == "late10" {
			buf := make([]byte, 10)
			for got := 0; got < len(buf); {
				n, err := os.Stdin.Read(buf[got:])
				if err != nil {
					os.Exit(2)
				}
				got += n
			}
			os.Exit(0) // at once, before the client finishes sending
		}
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type helperAgent struct {
	agentv1.UnimplementedAgentServiceServer
	mode string
}

func (h helperAgent) Terminal(stream agentv1.AgentService_TerminalServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- this test binary
	cmd.Env = append(os.Environ(), "PORTENV_TERMINAL_HELPER="+h.mode)
	return ServeTerminal(stream, cmd, 80, 24, first.GetOpen().GetInputPiped())
}

// session is how a test drives a terminal: piped says whose input it is
// (TerminalOpen.input_piped); input goes once the helper is ready; the
// client half-closes after closeAfter (never when negative), sending
// later after that wait instead.
type session struct {
	mode       string
	piped      bool
	input      []byte
	closeAfter time.Duration
	later      []byte
}

// run returns how the terminal ended: with its exit code (exited) or with
// an error.
func (sn session) run(t *testing.T) (exited bool, err error) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, helperAgent{mode: sn.mode})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	conn, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tc, err := agentv1.NewAgentServiceClient(conn).Terminal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tc.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Open{Open: &agentv1.TerminalOpen{Session: "main", InputPiped: sn.piped}}}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !strings.Contains(out.String(), "ready") {
		r, err := tc.Recv()
		if err != nil {
			t.Fatalf("before ready: %v", err)
		}
		out.Write(r.GetOutput())
	}
	if err := tc.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Input{Input: sn.input}}); err != nil {
		t.Fatal(err)
	}
	go func() {
		if sn.closeAfter < 0 {
			return
		}
		time.Sleep(sn.closeAfter)
		if sn.later != nil {
			_ = tc.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Input{Input: sn.later}})
		}
		_ = tc.CloseSend()
	}()
	for {
		r, err := tc.Recv()
		if err != nil {
			return false, err
		}
		if _, ok := r.GetMsg().(*agentv1.TerminalResponse_ExitCode); ok {
			return true, nil
		}
	}
}

// wantDataLoss fails unless the terminal ended with DataLoss naming want.
func wantDataLoss(t *testing.T, exited bool, err error, want string) {
	t.Helper()
	if exited {
		t.Fatal("the terminal ended with its exit code, as if all its input arrived")
	}
	if status.Code(err) != codes.DataLoss || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want DataLoss: %s", err, want)
	}
}

// TestTerminalSaysWhatInputDidntArrive: the client finished sending, but
// the program in the box stopped reading. The terminal doesn't end as if
// all arrived: it ends with DataLoss, saying how much is left.
func TestTerminalSaysWhatInputDidntArrive(t *testing.T) {
	defer func(d time.Duration) { inputGiveUp = d }(inputGiveUp)
	inputGiveUp = 500 * time.Millisecond
	exited, err := session{mode: "stuck", piped: true, input: []byte(strings.Repeat("x", 100))}.run(t)
	wantDataLoss(t, exited, err, "100 bytes of input didn't reach")
}

// TestTerminalSaysWhenTheProgramExitsFirst (R-0051, R-0053): piped input,
// 500 bytes into a program that reads 10 and exits at once, like head -c
// 10. The other 490 never reached a program: DataLoss saying so, whether
// the client's half-close comes before the exit, after it, or not at all.
func TestTerminalSaysWhenTheProgramExitsFirst(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("macOS discards a terminal's unread input when its program exits; the agent runs on Linux")
	}
	for name, after := range map[string]time.Duration{"half-close at once": 0, "half-close after the exit": 300 * time.Millisecond, "no half-close": -1} {
		t.Run(name, func(t *testing.T) {
			exited, err := session{mode: "head10", piped: true, input: []byte(strings.Repeat("x", 500)), closeAfter: after}.run(t)
			wantDataLoss(t, exited, err, "490 bytes of input didn't reach")
		})
	}
}

// TestTerminalCountsWhatTheProgramLeftAtItsExit (#73): the client has
// finished sending, and the program reads 10 of 500 bytes only after the
// agent last asked the PTY what's unread, then exits. The count comes from
// the PTY after the exit (490), never from that earlier answer (500, or 0
// while the write was still on its way into the queue).
func TestTerminalCountsWhatTheProgramLeftAtItsExit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("macOS discards a terminal's unread input when its program exits; the agent runs on Linux")
	}
	defer func(d time.Duration) { inputPoll = d }(inputPoll)
	inputPoll = 2 * time.Second // one look before the program reads, the next after it exits
	exited, err := session{mode: "late10", piped: true, input: []byte(strings.Repeat("x", 500))}.run(t)
	wantDataLoss(t, exited, err, "490 bytes of input didn't reach")
}

// TestTerminalTypingAfterTheEndIsNotDataLoss (R-0053): a person typing
// (input_piped false). Keys still unread when the program exits, and keys
// typed after it, are normal at a terminal: it ends with its exit code,
// never a data-loss line.
func TestTerminalTypingAfterTheEndIsNotDataLoss(t *testing.T) {
	exited, err := session{mode: "head10", input: []byte(strings.Repeat("x", 500)), closeAfter: 300 * time.Millisecond, later: []byte("more keys")}.run(t)
	if !exited || err != nil {
		t.Fatalf("got exited=%v %v, want the exit code", exited, err)
	}
}
