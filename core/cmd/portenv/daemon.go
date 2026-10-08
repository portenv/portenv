// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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
//	portenv app open|close|point|revert|check|restart BOX
//	portenv app move BOX this-mac|USER@HOST
//	portenv app servers BOX         (the Move To targets, one per line)
//	portenv app ping                (exit 0 when portenvd answers)
//	portenv app state BOX           (the save state and location, JSON)
//	portenv app channel BOX         (a runner's box channel for a Mac, JSON on stdout)
//	portenv app network up|down     (the system's network status, from the app)
//	portenv app woke                (after sleep: check every open box's channel)
func cmdApp(ctx context.Context, e *local.Env, op string, args []string) error {
	if op == "woke" {
		c, done, err := dialDaemon(e)
		if err != nil {
			return err
		}
		defer done()
		r, err := c.Woke(ctx, &daemonv1.WokeRequest{})
		if err != nil {
			return plain(err)
		}
		for _, b := range r.GetBoxes() {
			switch {
			case b.GetRestarted():
				fmt.Printf("%s: the box agent's channel was gone; restarted\n", b.GetName())
			case b.GetAgentAvailable():
				fmt.Printf("%s: the box agent answers\n", b.GetName())
			default:
				fmt.Printf("%s: the box agent is unavailable: %s\n", b.GetName(), b.GetDetail())
			}
		}
		return nil
	}
	if op == "network" {
		path := map[string]daemonv1.NetworkPath{"up": daemonv1.NetworkPath_NETWORK_PATH_SATISFIED, "down": daemonv1.NetworkPath_NETWORK_PATH_UNSATISFIED}
		if len(args) != 1 || path[args[0]] == daemonv1.NetworkPath_NETWORK_PATH_UNSPECIFIED {
			return usageError{Msg: "app network needs up or down"}
		}
		c, done, err := dialDaemon(e)
		if err != nil {
			return err
		}
		defer done()
		// The reporter is this command's parent (the app): portenvd trusts a
		// "down" only while it runs, and for 30 s.
		_, err = c.SetNetworkPath(ctx, &daemonv1.SetNetworkPathRequest{Path: path[args[0]], ReporterPid: int32(os.Getppid())}) // #nosec G115 -- a process ID
		if err != nil {
			return plain(err)
		}
		return nil
	}
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
	case "channel":
		// The box's agent channel, for a Mac reaching this server's box:
		// printed once on stdout (read over SSH, kept in memory there),
		// never logged or written.
		r, err := c.GetChannel(ctx, &daemonv1.GetChannelRequest{Name: name})
		if err != nil {
			return plain(err)
		}
		b, err := json.Marshal(struct {
			Address string `json:"address"`
			CertPEM []byte `json:"cert_pem"`
			Token   string `json:"token"` // #nosec G117 -- deliberate: handed to the Mac over SSH
		}{r.GetAddress(), r.GetCertPem(), r.GetToken()})
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(b, '\n'))
		return err
	case "state":
		r, err := c.GetBoxState(ctx, &daemonv1.GetBoxStateRequest{Name: name})
		if err != nil {
			return plain(err)
		}
		var at time.Time
		if r.GetSavedAt() != nil {
			at = r.GetSavedAt().AsTime()
		}
		b, err := json.Marshal(struct {
			State    string    `json:"state"`
			SavedAt  time.Time `json:"saved_at,omitzero"`
			Location string    `json:"location,omitempty"`
		}{r.GetState().String(), at, r.GetLocation()})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	case "check":
		r, err := c.CheckBox(ctx, &daemonv1.CheckBoxRequest{Name: name})
		if err != nil {
			return plain(err)
		}
		if !r.GetAgentAvailable() {
			return errors.New(r.GetDetail())
		}
		summary = "the box agent is available"
	case "restart":
		r, err := c.RestartBox(ctx, &daemonv1.RestartBoxRequest{Name: name})
		if err != nil {
			return plain(err)
		}
		summary = "restarted: " + r.GetSummary()
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
