// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/daemon"
	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// dialDaemon connects to portenvd's socket in the Portenv directory.
func dialDaemon(e *local.Env) (daemonv1.DaemonServiceClient, func(), error) {
	sock := filepath.Join(e.Dir, daemon.SocketName)
	if _, err := os.Stat(sock); err != nil {
		return nil, nil, fmt.Errorf("portenvd is not running (no %s)", sock)
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return daemonv1.NewDaemonServiceClient(conn), func() { _ = conn.Close() }, nil
}

// cmdApp runs one of the app's actions through portenvd:
//
//	portenv app open|close|point|revert BOX
//	portenv app move BOX this-mac|USER@HOST
//	portenv app servers BOX         (the Move To targets, one per line)
//	portenv app ping                (exit 0 when portenvd answers)
func cmdApp(ctx context.Context, e *local.Env, op string, args []string) error {
	if op == "ping" {
		c, done, err := dialDaemon(e)
		if err != nil {
			return err
		}
		defer done()
		if _, err := c.GetVersion(ctx, &daemonv1.GetVersionRequest{}); err != nil {
			return plain(err)
		}
		fmt.Println("portenvd is running")
		return nil
	}
	if len(args) < 1 {
		return usageError{Msg: "app needs a box name"}
	}
	name := args[0]
	if op == "servers" {
		mc, err := e.MachineConfig()
		if err != nil {
			return err
		}
		for _, s := range mc.Servers {
			fmt.Println(s)
		}
		return nil
	}
	c, done, err := dialDaemon(e)
	if err != nil {
		return err
	}
	defer done()
	var summary string
	switch op {
	case "open":
		r, err := c.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: name})
		if err != nil {
			return plain(err)
		}
		summary = r.GetSummary()
	case "close":
		if _, err := c.CloseBox(ctx, &daemonv1.CloseBoxRequest{Name: name}); err != nil {
			return plain(err)
		}
		summary = "closed and released"
	case "point":
		r, err := c.MakeSavePoint(ctx, &daemonv1.MakeSavePointRequest{Name: name})
		if err != nil {
			return plain(err)
		}
		summary = "save point " + short(r.GetSnapshot().GetId())
	case "revert":
		r, err := c.RevertToLastSavePoint(ctx, &daemonv1.RevertToLastSavePointRequest{Name: name})
		if err != nil {
			return plain(err)
		}
		summary = fmt.Sprintf("reverted to save point %s; the work it replaced is save %s", short(r.GetRestored().GetId()), short(r.GetSavedBefore().GetId()))
	case "move":
		if len(args) != 2 {
			return usageError{Msg: "app move needs BOX and a target (this-mac or USER@HOST)"}
		}
		r, err := c.MoveBox(ctx, &daemonv1.MoveBoxRequest{Name: name, Target: args[1]})
		if err != nil {
			return plain(err)
		}
		summary = r.GetSummary()
	default:
		return usageError{Msg: "unknown app action " + op}
	}
	fmt.Println(summary)
	return nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// plain drops the gRPC wrapping from an error for people.
func plain(err error) error {
	if s, ok := status.FromError(err); ok {
		return errors.New(s.Message())
	}
	return err
}

// cmdAttach attaches this terminal to the box's tmux session through
// portenvd and the box agent (never docker exec).
func cmdAttach(ctx context.Context, e *local.Env, name string, args []string) error {
	fs := flags("attach")
	session := fs.String("session", "main", "tmux session")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, done, err := dialDaemon(e)
	if err != nil {
		return err
	}
	defer done()
	stream, err := c.Terminal(ctx)
	if err != nil {
		return plain(err)
	}
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd()) // #nosec G115 -- file descriptors
	cols, rows := 80, 24
	if w, h, err := term.GetSize(out); err == nil {
		cols, rows = w, h
	}
	if err := stream.Send(&daemonv1.TerminalRequest{Msg: &daemonv1.TerminalRequest_Open{Open: &daemonv1.TerminalOpen{
		Box: name, Session: *session, Size: &daemonv1.TerminalSize{Cols: uint32(cols), Rows: uint32(rows)}, // #nosec G115 -- terminal sizes
	}}}); err != nil {
		return plain(err)
	}
	if term.IsTerminal(in) {
		old, err := term.MakeRaw(in)
		if err != nil {
			return err
		}
		defer func() { _ = term.Restore(in, old) }()
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if w, h, err := term.GetSize(out); err == nil {
				_ = stream.Send(&daemonv1.TerminalRequest{Msg: &daemonv1.TerminalRequest_Resize{Resize: &daemonv1.TerminalSize{Cols: uint32(w), Rows: uint32(h)}}}) // #nosec G115 -- terminal sizes
			}
		}
	}()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if stream.Send(&daemonv1.TerminalRequest{Msg: &daemonv1.TerminalRequest_Input{Input: append([]byte(nil), buf[:n]...)}}) != nil {
					return
				}
			}
			if err != nil {
				_ = stream.CloseSend()
				return
			}
		}
	}()
	for {
		r, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return plain(err)
		}
		switch m := r.GetMsg().(type) {
		case *daemonv1.TerminalResponse_Output:
			_, _ = os.Stdout.Write(m.Output)
		case *daemonv1.TerminalResponse_ExitCode:
			return nil
		}
	}
}
