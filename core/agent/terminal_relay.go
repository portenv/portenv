// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// How long a terminal whose client finished sending waits for the program
// to take its input: the input it hasn't read must shrink at least this
// often, or the client is told what didn't arrive.
var inputGiveUp = 15 * time.Second

// After the program has read all its input, output it prints in reply
// still reaches the client: the terminal closes once the output has been
// quiet this long (at most outputLingerMax).
var (
	outputQuiet     = 300 * time.Millisecond
	outputLingerMax = 5 * time.Second
)

// ServeTerminal starts cmd on a new PTY of the given size and relays it
// over the stream until the program ends (runTerminal's relay; portenvd's
// tests drive it over a slow link).
//
// Nothing the client sent is dropped: when the client half-closes (its
// stdin ended), the PTY stays open until the program has read every byte
// written to it and its output has gone quiet. Input that can't be
// delivered (the program stops reading, or exits first) ends the call
// with DataLoss, saying how much, never a quiet exit.
func ServeTerminal(stream agentv1.AgentService_TerminalServer, cmd *exec.Cmd, cols, rows uint16) error {
	f, tty, err := pty.Open()
	if err != nil {
		return err
	}
	ttyName := tty.Name()
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	if err := pty.Setsize(f, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		_, _ = f.Close(), tty.Close()
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid, cmd.SysProcAttr.Setctty = true, true
	err = cmd.Start()
	_ = tty.Close() // the program holds the PTY's other end now
	if err != nil {
		_ = f.Close()
		return err
	}
	// Ending the terminal hangs up its program (as closing the PTY does on
	// Linux; on macOS a close waits for the blocked read).
	var closeOnce sync.Once
	closePTY := func() {
		closeOnce.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGHUP)
			_ = f.Close()
		})
	}
	defer closePTY()

	var lastOutput atomic.Int64 // UnixNano of the last output read
	lastOutput.Store(time.Now().UnixNano())
	var lost atomic.Pointer[error] // input that didn't reach the program
	// Once the client half-closed, the result waits for the drain, which
	// also learns when the program exits (and what it left unread).
	exited, drained := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	halfClosed := false

	// Keystrokes and resizes from the client.
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				if status.Code(err) == codes.Canceled || status.Code(err) == codes.DeadlineExceeded {
					closePTY() // the client went away: nothing waits for the rest
					return
				}
				// The client finished sending (or its link failed after
				// the last message that arrived): hand the program
				// everything already written before closing.
				mu.Lock()
				halfClosed = true
				mu.Unlock()
				defer close(drained)
				if e := drainInput(ttyName, exited, &lastOutput); e != nil {
					lost.Store(&e)
				}
				closePTY()
				return
			}
			switch m := req.GetMsg().(type) {
			case *agentv1.TerminalRequest_Input:
				if n, err := f.Write(m.Input); err != nil {
					e := status.Errorf(codes.DataLoss, "%d bytes of input didn't reach the box's terminal: it ended first", len(m.Input)-n)
					lost.Store(&e)
					return
				}
			case *agentv1.TerminalRequest_Resize:
				_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(m.Resize.GetCols()), Rows: uint16(m.Resize.GetRows())}) // #nosec G115 -- terminal sizes
			}
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			lastOutput.Store(time.Now().UnixNano())
			if err := stream.Send(&agentv1.TerminalResponse{Msg: &agentv1.TerminalResponse_Output{Output: append([]byte(nil), buf[:n]...)}}); err != nil {
				_ = cmd.Process.Kill()
				break
			}
		}
		if rerr != nil {
			if !isClosed(rerr) {
				return rerr
			}
			break
		}
	}
	code := 0
	var exit *exec.ExitError
	if err := cmd.Wait(); errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	mu.Lock()
	waitDrain := halfClosed
	mu.Unlock()
	close(exited)
	if waitDrain {
		<-drained
	}
	if e := lost.Load(); e != nil {
		return *e
	}
	return stream.Send(&agentv1.TerminalResponse{Msg: &agentv1.TerminalResponse_ExitCode{ExitCode: int32(code)}}) // #nosec G115 -- exit codes fit
}

// drainInput waits until the program on the PTY has read all the input
// written to it, then until its output has been quiet for outputQuiet. It
// fails, saying how much is left, when the unread input stops shrinking
// for inputGiveUp.
func drainInput(ttyName string, exited <-chan struct{}, lastOutput *atomic.Int64) error {
	if err := waitInputRead(ttyName, exited); err != nil {
		return err
	}
	for start := time.Now(); time.Since(start) < outputLingerMax; time.Sleep(25 * time.Millisecond) {
		if time.Since(time.Unix(0, lastOutput.Load())) >= outputQuiet {
			break
		}
	}
	return nil
}

// waitInputRead waits until the PTY has no unread input: seen empty twice
// in a row, since the kernel may still be moving a write along. If the
// program exits first (or the PTY can't be asked), what it left unread
// never reaches it: DataLoss, with the count.
func waitInputRead(ttyName string, exited <-chan struct{}) error {
	left, progress := -1, time.Now()
	for zeros := 0; zeros < 2; time.Sleep(25 * time.Millisecond) {
		select {
		case <-exited:
			return inputLeftAtExit(ttyName, left)
		default:
		}
		n, err := unreadInput(ttyName)
		switch {
		case err != nil:
			return inputLeftAtExit(ttyName, left)
		case n == 0:
			zeros++
			continue
		case left < 0 || n < left:
			progress = time.Now()
		case time.Since(progress) > inputGiveUp:
			return status.Errorf(codes.DataLoss, "%d bytes of input didn't reach the box's terminal: the program there stopped reading", n)
		}
		zeros, left = 0, n
	}
	return nil
}

// inputLeftAtExit reports the input a program that ended never read: the
// PTY's count if it can still be asked (Linux keeps it after the exit),
// else the last count seen.
func inputLeftAtExit(ttyName string, left int) error {
	if n, err := unreadInput(ttyName); err == nil {
		left = n
	}
	if left > 0 {
		return status.Errorf(codes.DataLoss, "%d bytes of input didn't reach the box's terminal: the program there ended first", left)
	}
	return nil
}

// unreadInput is how many bytes written to the PTY its program hasn't read
// yet. It opens the PTY's program end for the ioctl: the agent closed its
// own copy so that the PTY ends when the program does.
func unreadInput(ttyName string) (int, error) {
	fd, err := unix.Open(ttyName, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = unix.Close(fd) }()
	n, err := unix.IoctlGetInt(fd, inputQueueReq)
	if err != nil {
		return 0, fmt.Errorf("unread input on %s: %w", ttyName, err)
	}
	return n, nil
}

// isClosed reports whether err only says the other side went away.
func isClosed(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || strings.Contains(err.Error(), "input/output error")
}
