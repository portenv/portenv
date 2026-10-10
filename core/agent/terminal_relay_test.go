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
// reads 10 bytes and exits, like head -c 10.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PORTENV_TERMINAL_HELPER"); mode != "" {
		if _, err := term.MakeRaw(0); err != nil {
			os.Exit(2)
		}
		fmt.Print("ready\n")
		if mode == "head10" {
			buf := make([]byte, 10)
			for got := 0; got < len(buf); {
				n, err := os.Stdin.Read(buf[got:])
				if err != nil {
					os.Exit(2)
				}
				got += n
			}
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
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
	if _, err := stream.Recv(); err != nil {
		return err
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 -- this test binary
	cmd.Env = append(os.Environ(), "PORTENV_TERMINAL_HELPER="+h.mode)
	return ServeTerminal(stream, cmd, 80, 24)
}

// sendAndFinish runs the helper in a terminal, sends input once it is
// ready, half-closes as when stdin ends, and returns how the terminal
// ended. It fails the test if the terminal ended as if all arrived.
func sendAndFinish(t *testing.T, mode string, input []byte) error {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, helperAgent{mode: mode})
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
	if err := tc.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Open{Open: &agentv1.TerminalOpen{Session: "main"}}}); err != nil {
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
	if err := tc.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Input{Input: input}}); err != nil {
		t.Fatal(err)
	}
	_ = tc.CloseSend()
	for {
		r, err := tc.Recv()
		if err != nil {
			return err
		}
		if _, ok := r.GetMsg().(*agentv1.TerminalResponse_ExitCode); ok {
			t.Fatal("the terminal ended as if all its input arrived")
		}
	}
}

// TestTerminalSaysWhatInputDidntArrive: the client finished sending, but
// the program in the box stopped reading. The terminal doesn't end as if
// all arrived: it ends with DataLoss, saying how much is left.
func TestTerminalSaysWhatInputDidntArrive(t *testing.T) {
	defer func(d time.Duration) { inputGiveUp = d }(inputGiveUp)
	inputGiveUp = 500 * time.Millisecond
	err := sendAndFinish(t, "stuck", []byte(strings.Repeat("x", 100)))
	if status.Code(err) != codes.DataLoss || !strings.Contains(err.Error(), "100 bytes of input didn't reach") {
		t.Fatalf("got %v, want DataLoss for 100 bytes", err)
	}
}

// TestTerminalSaysWhenTheProgramExitsFirst (R-0051): the program reads
// 10 bytes of 500 and exits, like head -c 10 with more piped in. The
// other 490 never reached a program: the terminal ends with DataLoss
// saying so, not with an exit code (attach would exit 0).
func TestTerminalSaysWhenTheProgramExitsFirst(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("macOS discards a terminal's unread input when its program exits; the agent runs on Linux")
	}
	err := sendAndFinish(t, "head10", []byte(strings.Repeat("x", 500)))
	if status.Code(err) != codes.DataLoss || !strings.Contains(err.Error(), "490 bytes of input didn't reach") {
		t.Fatalf("got %v, want DataLoss for 490 bytes", err)
	}
}
