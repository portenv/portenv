// SPDX-License-Identifier: Apache-2.0

// Package drivertest is the conformance suite every box driver must pass.
// It needs a toolbox image (portenv-agent as init) and a real engine.
package drivertest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// Env describes the driver under test.
type Env struct {
	Driver driver.Driver
	Image  string
	// HomeRef returns a fresh, unused home storage reference for a box.
	HomeRef func(id driver.BoxID) string
	// Cleanup removes home storage after the test (drivers never do).
	Cleanup func(ref string)
	// Restarted returns a new instance of the driver on the same state, as
	// a restarted portenvd or runner has: it holds no channel secrets.
	Restarted func() driver.Driver
	// Probe runs a command in a box so the suite can inspect it. Test code
	// only: drivers have no way to exec into a box (ADR 0010 condition 4).
	Probe func(ctx context.Context, id driver.BoxID, req ProbeRequest) (ProbeResult, error)
}

// ProbeRequest is one command a test runs in a box to inspect it.
type ProbeRequest struct {
	Argv    []string // not run through a shell
	Env     []string // "KEY=value"
	User    string   // empty means root
	Stdin   []byte
	Timeout time.Duration // 0 means no timeout
}

// ProbeResult is what a probe printed and how it exited.
type ProbeResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Run runs the conformance suite.
func Run(t *testing.T, env Env) {
	t.Run("Capabilities", func(t *testing.T) { capabilities(t, env) })
	t.Run("Lifecycle", func(t *testing.T) { lifecycle(t, env) })
	t.Run("HomeSurvivesRestartAndDestroy", func(t *testing.T) { homeSurvives(t, env) })
	t.Run("MissingHomeIsReported", func(t *testing.T) { missingHome(t, env) })
	t.Run("AgentChannel", func(t *testing.T) { agentChannel(t, env) })
	t.Run("TakeOverWithoutRestart", func(t *testing.T) { takeOver(t, env) })
}

type box struct {
	t   *testing.T
	env Env
	id  driver.BoxID
	ref string
}

func newBox(t *testing.T, env Env) *box {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := driver.BoxID("conformance-" + hex.EncodeToString(b))
	bx := &box{t: t, env: env, id: id, ref: env.HomeRef(id)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.Driver.Stop(ctx, id, time.Second)
		_ = env.Driver.Destroy(ctx, id)
		if env.Cleanup != nil {
			env.Cleanup(bx.ref)
		}
	})
	return bx
}

func (b *box) create(fresh bool) {
	b.t.Helper()
	ctx := context.Background()
	st, err := b.env.Driver.Create(ctx, driver.Box{ID: b.id, Name: string(b.id), ToolboxImage: b.env.Image})
	if err != nil {
		b.t.Fatalf("Create: %v", err)
	}
	if st != driver.StateCreated {
		b.t.Fatalf("Create returned %v, want created", st)
	}
	if err := b.env.Driver.MountHome(ctx, b.id, driver.HomeStorage{Ref: b.ref, Fresh: fresh}); err != nil {
		b.t.Fatalf("MountHome: %v", err)
	}
}

func (b *box) start() {
	b.t.Helper()
	if _, err := b.env.Driver.Start(context.Background(), b.id); err != nil {
		b.t.Fatalf("Start: %v", err)
	}
}

// exec runs argv in the box as root and returns stdout.
func (b *box) exec(argv ...string) (string, int) {
	b.t.Helper()
	res, err := b.env.Probe(context.Background(), b.id, ProbeRequest{Argv: argv, Timeout: time.Minute})
	if err != nil {
		b.t.Fatalf("Exec %v: %v", argv, err)
	}
	return string(res.Stdout), res.ExitCode
}

// waitReady waits for the agent to report want ("READY" or "FAILED").
func (b *box) waitReady(want string) string {
	b.t.Helper()
	var out string
	for range 240 {
		res, err := b.env.Probe(context.Background(), b.id, ProbeRequest{Argv: []string{"portenv-agent", "ready"}, Timeout: 10 * time.Second})
		if err == nil {
			out = string(res.Stdout)
			if strings.Contains(out, want) {
				return out
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	b.t.Fatalf("box never became %s; last: %q", want, out)
	return ""
}

func capabilities(t *testing.T, env Env) {
	c, err := env.Driver.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Driver == "" || c.EngineVersion == "" || c.Isolation == driver.IsolationUnspecified || len(c.Architectures) == 0 {
		t.Fatalf("incomplete capabilities: %+v", c)
	}
}

func lifecycle(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(true)
	b.start()
	b.waitReady("READY")

	st, err := env.Driver.Stats(context.Background(), b.id)
	if err != nil || st.State != driver.StateRunning || st.ProcessCount == 0 {
		t.Fatalf("Stats while running: %+v, %v", st, err)
	}

	var logs strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for chunk, err := range env.Driver.Logs(ctx, b.id, driver.LogOptions{}) {
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		logs.Write(chunk.Data)
	}
	if !strings.Contains(logs.String(), "box ready") {
		t.Fatalf("logs lack the agent's ready line:\n%s", logs.String())
	}

	if st, err := env.Driver.Stop(context.Background(), b.id, 5*time.Second); err != nil || st != driver.StateStopped {
		t.Fatalf("Stop: %v %v", st, err)
	}
	if st, err := env.Driver.Stats(context.Background(), b.id); err != nil || st.State == driver.StateRunning {
		t.Fatalf("Stats after stop: %+v, %v", st, err)
	}
}

func homeSurvives(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(true)
	b.start()
	b.waitReady("READY")
	if _, code := b.exec("sh", "-c", "echo kept > /home/work/marker && echo lost > /var/tmp/marker && echo cached > /var/cache/portenv-sync/probe"); code != 0 {
		t.Fatal("write markers")
	}
	if _, err := env.Driver.Stop(context.Background(), b.id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	b.start()
	b.waitReady("READY")
	if out, _ := b.exec("cat", "/home/work/marker"); strings.TrimSpace(out) != "kept" {
		t.Fatalf("/home lost across restart: %q", out)
	}
	if _, code := b.exec("test", "-e", "/var/tmp/marker"); code == 0 {
		t.Fatal("the root filesystem survived a restart; it must be fresh each start")
	}
	if out, _ := b.exec("cat", "/var/cache/portenv-sync/probe"); strings.TrimSpace(out) != "cached" {
		t.Fatalf("restic's cache did not survive a restart: %q", out)
	}
	if out, _ := b.exec("stat", "-c", "%u %a", "/var/cache/portenv-sync"); strings.TrimSpace(out) != "990 700" {
		t.Fatalf("restic's cache directory is %q, want owned by portenv-sync (990) with mode 700", out)
	}

	// Destroy keeps home storage: a new box on the same storage finds it.
	if _, err := env.Driver.Stop(context.Background(), b.id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := env.Driver.Destroy(context.Background(), b.id); err != nil {
		t.Fatal(err)
	}
	b.create(false)
	b.start()
	b.waitReady("READY")
	if out, _ := b.exec("cat", "/home/work/marker"); strings.TrimSpace(out) != "kept" {
		t.Fatalf("Destroy lost the home: %q", out)
	}
}

func missingHome(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(false) // new, empty storage without Fresh: the agent must refuse
	b.start()
	out := b.waitReady("FAILED")
	if !strings.Contains(out, "home storage is not attached") {
		t.Fatalf("got %q", out)
	}
}

// agentChannel: the agent's API is reachable through AgentChannel (never
// Exec), a terminal lands in tmux as work, and a restart replaces the
// channel's credentials (ADR 0010).
func agentChannel(t *testing.T, env Env) {
	b := newBox(t, env)
	b.create(true)
	b.start()
	b.waitReady("READY")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dial := func() (agentv1.AgentServiceClient, *agent.ChannelSecrets, func()) {
		ch, err := env.Driver.AgentChannel(ctx, b.id)
		if err != nil {
			t.Fatalf("AgentChannel: %v", err)
		}
		conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
		if err != nil {
			t.Fatal(err)
		}
		return agentv1.NewAgentServiceClient(conn), &agent.ChannelSecrets{Token: ch.Token, CertPEM: ch.CertPEM}, func() { _ = conn.Close() }
	}
	c, old, closeOld := dial()
	defer closeOld()
	var ready *agentv1.GetReadinessResponse
	var err error
	for range 50 {
		if ready, err = c.GetReadiness(ctx, &agentv1.GetReadinessRequest{}); err == nil && ready.GetState() == agentv1.ReadinessState_READINESS_STATE_READY {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil || ready.GetState() != agentv1.ReadinessState_READINESS_STATE_READY {
		t.Fatalf("readiness over the channel: %v %v", ready, err)
	}
	if info, err := c.GetPathInfo(ctx, &agentv1.GetPathInfoRequest{Path: "/home/work"}); err != nil || !info.GetIsDir() {
		t.Fatalf("path info over the channel: %v %v", info, err)
	}

	term, err := c.Terminal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	send := func(r *agentv1.TerminalRequest) {
		if err := term.Send(r); err != nil {
			t.Fatal(err)
		}
	}
	send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Open{Open: &agentv1.TerminalOpen{Session: "main", Size: &agentv1.TerminalSize{Cols: 100, Rows: 30}}}})
	send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Input{Input: []byte("echo user=$(id -un) in=$TMUX marker=$((40+2))\r")}})
	var out strings.Builder
	for !strings.Contains(out.String(), "marker=42") {
		r, err := term.Recv()
		if err != nil {
			t.Fatalf("terminal: %v; output so far %q", err, out.String())
		}
		out.Write(r.GetOutput())
	}
	if !strings.Contains(out.String(), "user=work") || !strings.Contains(out.String(), "in=/tmp/tmux-1000") {
		t.Fatalf("terminal is not work's tmux session: %q", out.String())
	}
	_ = term.CloseSend()

	// A restart makes new credentials: the old ones no longer work.
	if _, err := env.Driver.Stop(ctx, b.id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	b.start()
	b.waitReady("READY")
	stale, err := agent.DialChannel(func(ctx context.Context) (net.Conn, error) {
		ch, err := env.Driver.AgentChannel(ctx, b.id)
		if err != nil {
			return nil, err
		}
		return ch.Dial(ctx)
	}, old.CertPEM, old.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stale.Close() }()
	if _, err := agentv1.NewAgentServiceClient(stale).GetPathInfo(ctx, &agentv1.GetPathInfoRequest{Path: "/home"}); err == nil {
		t.Fatal("the previous start's credentials still reach the agent")
	}
	c2, _, closeNew := dial()
	defer closeNew()
	for range 50 {
		if _, err = c2.GetPathInfo(ctx, &agentv1.GetPathInfoRequest{Path: "/home"}); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the new channel after a restart: %v", err)
	}
}

// takeOver: a restarted daemon re-keys a running box instead of restarting
// it (ADR 0014). A command started before keeps running, the tmux session
// survives, and the old secrets stop working.
func takeOver(t *testing.T, env Env) {
	if env.Restarted == nil {
		t.Fatal("the driver must support take-over: set Env.Restarted")
	}
	b := newBox(t, env)
	b.create(true)
	b.start()
	b.waitReady("READY")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := func(d driver.Driver) (agentv1.AgentServiceClient, driver.AgentChannel, func()) {
		ch, err := d.AgentChannel(ctx, b.id)
		if err != nil {
			t.Fatalf("AgentChannel: %v", err)
		}
		conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
		if err != nil {
			t.Fatal(err)
		}
		return agentv1.NewAgentServiceClient(conn), ch, func() { _ = conn.Close() }
	}
	// runIn types a command into the box's main tmux session and returns
	// the output up to want.
	runIn := func(c agentv1.AgentServiceClient, cmd, want string) string {
		term, err := c.Terminal(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = term.CloseSend() }()
		for _, r := range []*agentv1.TerminalRequest{
			{Msg: &agentv1.TerminalRequest_Open{Open: &agentv1.TerminalOpen{Session: "main", Size: &agentv1.TerminalSize{Cols: 120, Rows: 30}}}},
			{Msg: &agentv1.TerminalRequest_Input{Input: []byte(cmd + "\r")}},
		} {
			if err := term.Send(r); err != nil {
				t.Fatal(err)
			}
		}
		var out strings.Builder
		for !strings.Contains(out.String(), want) {
			r, err := term.Recv()
			if err != nil {
				t.Fatalf("terminal: %v; output so far %q", err, out.String())
			}
			out.Write(r.GetOutput())
		}
		return out.String()
	}

	first, firstCh, closeFirst := client(env.Driver)
	defer closeFirst()
	// A long command, started in the session before the daemon restarts.
	runIn(first, "sleep 3600 & echo long=$((1+1))", "long=2")

	restarted := env.Restarted()
	if _, err := restarted.AgentChannel(ctx, b.id); !errors.Is(err, driver.ErrNoChannel) {
		t.Fatalf("a restarted driver's channel before re-keying: %v, want ErrNoChannel", err)
	}
	if err := restarted.Rekey(ctx, b.id); err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	second, secondCh, closeSecond := client(restarted)
	defer closeSecond()
	if secondCh.Token == firstCh.Token {
		t.Fatal("the re-key reused the old token")
	}
	out := runIn(second, "pgrep -x sleep >/dev/null && echo still=running", "still=")
	if !strings.Contains(out, "still=running") {
		t.Fatalf("the long command didn't survive the take-over: %q", out)
	}
	// The old secrets no longer work.
	if _, err := first.GetVersion(ctx, &agentv1.GetVersionRequest{}); err == nil {
		t.Fatal("the old channel still answers after the take-over")
	}
	// Re-keying a box that isn't running fails plainly; nothing is started.
	if _, err := restarted.Stop(ctx, b.id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Rekey(ctx, b.id); err == nil {
		t.Fatal("re-keyed a stopped box")
	}
}
