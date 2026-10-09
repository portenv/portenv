// SPDX-License-Identifier: Apache-2.0

//go:build linux

package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
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
	// The agent channel (ADR 0010) runs in its own process, started here
	// and restarted when it exits. Its restic and terminal processes are its
	// own children, so this process's reaper never takes their exit status.
	servePid := startServe(log)
	// No other process starts until the API process has taken the secrets
	// off stdin and listens.
	if servePid != 0 {
		if err := WaitChannelListening(ChannelPort, 30*time.Second); err != nil {
			log.Error("agent channel", "err", err)
		}
	}

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
		if failed, msg := readiness.Packages(); len(failed) > 0 {
			log.Error("packages from apt-packages.txt couldn't be installed; the box started anyway, retrying in the background", "packages", failed, "err", msg)
		}
		go retryPackages(ctx, cfg, ExecRunner{Mu: &mu}, readiness, log)
	}()

	bootDone := booted
	for {
		select {
		case <-bootDone:
			// Catch up on orphans that exited while a start-sequence command
			// held the lock.
			bootDone = nil
			mu.Lock()
			servePid = restartIfReaped(reap(), servePid, log)
			mu.Unlock()
		case sig := <-sigs:
			switch sig {
			case unix.SIGCHLD:
				// Never block the signal loop behind a long command such as
				// apt-get: skip now, catch up when the start sequence ends.
				if mu.TryLock() {
					servePid = restartIfReaped(reap(), servePid, log)
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

// reap collects every exited child without blocking and returns their PIDs.
func reap() []int {
	var pids []int
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return pids
		}
		pids = append(pids, pid)
	}
}

// startServe starts portenv-agent serve when the driver provided a channel,
// and returns its PID (0 when there is none).
// The secrets arrive on stdin (ADR 0010): init hands its stdin to the API
// process without reading it, then closes its own copy, so nothing else in
// the box can reach them.
func startServe(log *slog.Logger) int {
	if os.Getenv(ChannelEnv) != "stdin" {
		log.Info("no agent channel provided; serving the local socket only")
		return 0
	}
	// Then point init's own fd 0 at /dev/null: the stdin pipe is the API
	// process's alone.
	defer func() {
		if null, err := os.Open(os.DevNull); err == nil {
			_ = unix.Dup2(int(null.Fd()), 0)
			_ = null.Close()
		}
	}()
	// As portenv-agent, from the copy with file capabilities: non-dumpable
	// from its first instruction (ADR 0010, conditions).
	p, err := os.StartProcess(ServeBinary, []string{"portenv-agent", "serve"}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Env:   []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		// portenv-sync as a supplementary group only to read and run the
		// restic binary (root:portenv-sync, 0750).
		Sys: &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: ServeUID, Gid: ServeUID, Groups: []uint32{syncGID}}},
	})
	if err != nil {
		log.Error("start agent channel", "err", err)
		return 0
	}
	return p.Pid
}

// restartIfReaped restarts the channel process when it is among the reaped.
func restartIfReaped(reaped []int, servePid int, log *slog.Logger) int {
	if servePid != 0 && slices.Contains(reaped, servePid) {
		// Its secrets are gone with it: a new channel needs a new start.
		log.Warn("agent channel exited; restart the box for a new one")
		return 0
	}
	return servePid
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

// retryPackages retries the packages that couldn't be installed, in the
// background (1, 5, 15 minutes, then hourly), or at once on RetryPackages.
func retryPackages(ctx context.Context, cfg Config, run Runner, r *Readiness, log *slog.Logger) {
	for n := 0; ; {
		var wait <-chan time.Time
		if failed, _ := r.Packages(); len(failed) > 0 {
			wait = time.After(retryDelay(n))
		}
		select {
		case <-ctx.Done():
			return
		case <-wait:
			n++
		case <-r.Retries():
			n = 0
		}
		RetryPackages(ctx, cfg, run, r)
		if failed, msg := r.Packages(); len(failed) > 0 {
			log.Error("packages still couldn't be installed", "packages", failed, "err", msg)
		} else {
			log.Info("packages from apt-packages.txt installed")
		}
	}
}
