// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// Attach is a client's end of a terminal (portenv attach).
type Attach struct {
	In  io.Reader // keystrokes, or anything piped in
	Out io.Writer // the box's output, written as it arrives
	// TTY: In is a person's terminal, in raw mode, so Ctrl-C arrives as a
	// keystroke and goes to the box like any other.
	TTY bool
	// Resize carries the terminal's new size.
	Resize <-chan *daemonv1.TerminalSize
	// Interrupt carries Ctrl-C that arrived as a signal (SIGINT). With a
	// TTY it goes to the box as a keystroke. Otherwise the first stops
	// reading In and ends the attach once the box has everything read so
	// far, with a non-zero exit; a second ends it at once.
	Interrupt <-chan struct{}
}

// AttachTerminal runs a terminal through portenvd until the session ends.
//
// Nothing read from In is dropped silently when it is piped (not a TTY;
// TerminalOpen.input_piped tells the box agent). When In ends, the attach
// half-closes and waits: the box agent ends the session only after its
// program has read every byte, so a nil return means all of it arrived.
// Input that didn't (the session ended first, the link failed, a second
// Ctrl-C) ends it with an error that says so.
func AttachTerminal(ctx context.Context, c daemonv1.DaemonServiceClient, open *daemonv1.TerminalOpen, a Attach) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.Terminal(ctx)
	if err != nil {
		return err
	}
	open.InputPiped = !a.TTY
	if err := stream.Send(&daemonv1.TerminalRequest{Msg: &daemonv1.TerminalRequest_Open{Open: open}}); err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)

	// One goroutine reads input and one sends it: a gRPC stream takes
	// one sender at a time.
	var read, sent atomic.Int64
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		buf := make([]byte, 32<<10)
		for {
			n, err := a.In.Read(buf)
			if n > 0 {
				read.Add(int64(n))
				select {
				case chunks <- append([]byte(nil), buf[:n]...):
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var interrupted, abandoned atomic.Bool
	go func() {
		for {
			select {
			case b, ok := <-chunks:
				if !ok {
					_ = stream.CloseSend() // everything read is on its way
					return
				}
				if stream.Send(&daemonv1.TerminalRequest{Msg: &daemonv1.TerminalRequest_Input{Input: b}}) != nil {
					return // Recv says why
				}
				sent.Add(int64(len(b)))
			case sz := <-a.Resize:
				_ = stream.Send(&daemonv1.TerminalRequest{Msg: &daemonv1.TerminalRequest_Resize{Resize: sz}})
			case <-a.Interrupt:
				if a.TTY {
					_ = stream.Send(&daemonv1.TerminalRequest{Msg: &daemonv1.TerminalRequest_Input{Input: []byte{3}}})
					continue
				}
				interrupted.Store(true)
				_ = stream.CloseSend()
				select {
				case <-a.Interrupt:
					abandoned.Store(true)
					cancel()
				case <-stop:
				}
				return
			case <-stop:
				return
			}
		}
	}()

	// end is the attach's result once the session ended in order: the box
	// has everything sent, so only input never sent is missing.
	end := func() error {
		lost := read.Load() - sent.Load()
		switch {
		case interrupted.Load() && lost > 0:
			return fmt.Errorf("interrupted; %d bytes read before Ctrl-C didn't reach the box", lost)
		case interrupted.Load():
			return errors.New("interrupted; everything read before Ctrl-C reached the box")
		case lost > 0 && !a.TTY: // at a terminal, keys typed as it ends are normal
			return fmt.Errorf("%d bytes of input didn't reach the box: the session ended first", lost)
		}
		return nil
	}
	for {
		r, err := stream.Recv()
		if err != nil {
			if abandoned.Load() {
				return errors.New("interrupted twice; input still on its way may not have reached the box")
			}
			if errors.Is(err, io.EOF) {
				return end()
			}
			return err
		}
		switch m := r.GetMsg().(type) {
		case *daemonv1.TerminalResponse_Output:
			if _, err := a.Out.Write(m.Output); err != nil {
				return fmt.Errorf("writing the box's output: %w", err)
			}
		case *daemonv1.TerminalResponse_ExitCode:
			return end()
		}
	}
}
