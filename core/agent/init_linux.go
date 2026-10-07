// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// shutdownGrace is how long processes get between SIGTERM and SIGKILL.
// Docker's default stop timeout is 10 seconds; stay below it.
const shutdownGrace = 8 * time.Second

// Init runs the agent as the box's init process: it serves the agent API,
// runs the start sequence, then reaps orphaned processes until SIGTERM or
// SIGINT, when it stops every process in the box and returns.
//
// A failed start sequence does not end Init: the box stays up and reports
// FAILED so the failure can be inspected.
func Init(ctx context.Context, cfg Config, log *slog.Logger) error {
	if os.Getpid() != 1 {
		log.Warn("not running as PID 1; orphaned processes will not be reaped by this agent")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, unix.SIGCHLD, unix.SIGTERM, unix.SIGINT)
	defer signal.Stop(sigs)

	readiness := NewReadiness()
	go func() {
		if err := Serve(ctx, DefaultSocket, readiness); err != nil {
			log.Error("agent API stopped", "err", err)
		}
	}()

	// The reaper and the start sequence's commands share this lock, so the
	// reaper never collects a child that ExecRunner is still waiting for.
	var mu sync.Mutex
	booted := make(chan struct{})
	go func() {
		defer close(booted)
		if err := Boot(ctx, cfg, ExecRunner{Mu: &mu}, readiness); err != nil {
			readiness.Fail(err)
			log.Error("start sequence failed", "err", err)
			return
		}
		readiness.Ready()
		log.Info("box ready")
	}()

	bootDone := booted
	for {
		select {
		case <-bootDone:
			// Catch up on orphans that exited while a start-sequence command
			// held the lock.
			bootDone = nil
			mu.Lock()
			reap()
			mu.Unlock()
		case sig := <-sigs:
			switch sig {
			case unix.SIGCHLD:
				// Never block the signal loop behind a long command such as
				// apt-get: skip now, catch up when the start sequence ends.
				if mu.TryLock() {
					reap()
					mu.Unlock()
				}
			case unix.SIGTERM, unix.SIGINT:
				log.Info("stopping box", "signal", sig.String())
				cancel()
				<-booted
				stopAll(log)
				return nil
			}
		}
	}
}

// reap collects every exited child without blocking.
func reap() {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

// stopAll sends SIGTERM to every other process in the box, waits for them to
// exit for up to shutdownGrace, then sends SIGKILL to what remains.
func stopAll(log *slog.Logger) {
	if os.Getpid() != 1 {
		return // kill(-1) outside a PID namespace would hit the whole host.
	}
	_ = unix.Kill(-1, unix.SIGTERM)
	deadline := time.Now().Add(shutdownGrace)
	for time.Now().Before(deadline) {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if errors.Is(err, unix.ECHILD) {
			return
		}
		if pid <= 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	log.Warn("processes still running after grace period; killing them")
	_ = unix.Kill(-1, unix.SIGKILL)
	for {
		var ws unix.WaitStatus
		if _, err := unix.Wait4(-1, &ws, 0, nil); err != nil {
			return
		}
	}
}
