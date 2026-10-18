// SPDX-License-Identifier: Apache-2.0

// Package teamstart is the gate for an agent team's first start on a fresh
// workspace (R-0296), in the real toolbox image with a real portenvd: the
// owner's steps go through portenvd's gRPC socket, as the app makes them,
// and the agent's through its own socket inside the box. Nothing is faked.
//
//  1. portenv team init in /home/work/<workspace>, as work;
//  2. approve lead's file (ApproveAgentFile, with the sha the app shows);
//  3. start lead (StartAgent), which asks the team's first-start decision
//     (TEAM_START) naming lead; approved, portenvd starts lead itself,
//     with no second StartAgent, and a second Start is a no-op; its runner
//     is "bash <script>", a script in the project;
//  4. whoami in lead's pane says lead;
//  5. the script's "portenv ask-owner", over lead's own socket, reaches
//     portenvd's decision list (the WatchTeam stream the app reads).
//
// The harness's own docker exec only sets the project up (step 1, as work)
// and reads lead's pane as root; portenvd reaches the box only through the
// agent channel.
package teamstart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

const (
	workspace = "acme"
	namespace = "teamstart"
	project   = "/home/work/" + workspace
	script    = project + "/first-start.sh"
	question  = "R-0296 first start: may lead begin?"
)

// firstStart is lead's runner: what it prints is what step 4 and 5 read.
const firstStart = `#!/bin/bash
echo "first-start whoami=$(whoami)"
if out=$(portenv ask-owner "` + question + `" 2>&1); then
  echo "first-start ask-owner ok: $out"
else
  echo "first-start ask-owner failed: $out"
fi
exec sleep 1000000
`

type harness struct {
	t       *testing.T
	repo    string
	root    string
	env     []string
	image   string
	daemon  *exec.Cmd
	log     string
	boxName string // the container's name

	mu        sync.Mutex
	decisions map[string]*typesv1.Decision
	watchErr  error
}

func TestTeamFirstStart(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE isn't set (make team-start IMAGE=...)")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"portenvd", "portenv"} {
		if _, err := os.Stat(filepath.Join(repo, "bin", b)); err != nil {
			t.Fatalf("bin/%s is missing: run make build first (make team-start does): %v", b, err)
		}
	}
	root, err := os.MkdirTemp("", "portenv-teamstart.")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, repo: repo, root: root, image: image, log: filepath.Join(root, "portenvd.log"), decisions: map[string]*typesv1.Decision{}}
	h.env = append(os.Environ(), "PORTENV_KEYS=file", "PORTENV_HOME="+filepath.Join(root, "mac"), "PORTENV_DOCKER_NAMESPACE="+namespace)
	if err := os.MkdirAll(filepath.Join(root, "mac"), 0o700); err != nil {
		t.Fatal(err)
	}
	// This machine's ID, written before anything starts: portenvd and the
	// first portenv call would each make one at once, and one can read the
	// other's file before it's written (an empty ID, which sync refuses:
	// "invalid machine ID").
	if err := os.WriteFile(filepath.Join(root, "mac", "machine-id"), []byte("teamstart-ci\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	h.startDaemon(ctx)

	// A fresh workspace, opened as the app opens it.
	h.portenv(ctx, "init", workspace, "--image", image)
	out := h.portenv(ctx, "app", "open", workspace)
	if !strings.Contains(out, "rule 1") {
		t.Fatalf("open: not a new workspace (rule 1): %s", out)
	}
	h.boxName = "portenv-" + namespace + "-" + h.boxID()
	t.Logf("workspace %s is open in %s", workspace, h.boxName)

	conn, err := grpc.NewClient("unix://"+filepath.Join(root, "mac", "portenvd.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	c := daemonv1.NewDaemonServiceClient(conn)

	// Step 1: portenv team init in the project, as the owner (work) in the
	// box; lead's runner becomes the script.
	out, code := h.box(ctx, "work", fmt.Sprintf(`set -eu
mkdir -p %[1]s && cd %[1]s
portenv team init
cat > %[2]s <<'EOF'
%[3]sEOF
chmod 0755 %[2]s
sed -i 's|^runner: .*$|runner: bash %[2]s|' .portenv/agents/lead.md
grep -q '^runner: bash %[2]s$' .portenv/agents/lead.md`, project, script, firstStart))
	if code != 0 {
		h.fatal("step 1 (portenv team init in %s): exit %d: %s", project, code, out)
	}
	t.Logf("step 1: %s", out)

	// Step 2: approve lead, with the sha the app shows.
	lead := h.lead(ctx, c)
	if lead.GetRunner() != "bash "+script {
		h.fatal("step 2: lead's runner is %q, not the script", lead.GetRunner())
	}
	ar, err := c.ApproveAgentFile(ctx, &daemonv1.ApproveAgentFileRequest{Name: workspace, AgentId: "lead", FileSha256: lead.GetFileSha256()})
	if err != nil {
		h.fatal("step 2 (approve lead): %v", err)
	}
	if !ar.GetAgent().GetApproved() {
		h.fatal("step 2 (approve lead): not approved: %v", ar.GetAgent())
	}
	t.Log("step 2: lead approved")

	// The app's decision list.
	go h.watch(ctx, c)

	// Step 3: the first start asks the owner (TEAM_START), naming lead;
	// approved, portenvd starts lead itself, with no second StartAgent
	// (R-0296, R-0300).
	if _, err := c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: workspace, AgentId: "lead"}); err == nil {
		h.fatal("step 3: the team's first start didn't wait for the owner's approval")
	} else {
		t.Logf("step 3: first StartAgent: %v", err)
	}
	ts := h.waitDecision(func(d *typesv1.Decision) bool {
		return d.GetKind() == typesv1.DecisionKind_DECISION_KIND_TEAM_START && d.GetState() == typesv1.DecisionState_DECISION_STATE_PENDING
	}, time.Minute)
	if ts == nil {
		h.fatal("step 3: no pending TEAM_START decision in the decision list")
	}
	if ids := ts.GetStartAgentIds(); len(ids) != 1 || ids[0] != "lead" {
		h.fatal("step 3: TEAM_START names %v, not [lead]", ids)
	}
	if _, err := c.AnswerDecision(ctx, &daemonv1.AnswerDecisionRequest{Name: workspace, DecisionId: ts.GetId(), Approve: true}); err != nil {
		h.fatal("step 3 (approve the team's first start): %v", err)
	}
	running := h.waitRunning(ctx, c, 30*time.Second)
	t.Logf("step 3: portenvd started lead on Approve, tabs %v", running.GetTabIds())
	// Start on an agent already started is a no-op: no error, still
	// running, the same tabs (not started again).
	sr, err := c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: workspace, AgentId: "lead"})
	if err != nil {
		h.fatal("step 3 (Start on lead once it runs): %v", err)
	}
	if st := sr.GetAgent().GetState(); st != typesv1.AgentState_AGENT_STATE_RUNNING && st != typesv1.AgentState_AGENT_STATE_NEEDS_YOU {
		h.fatal("step 3: a second Start left lead %v, not running", st)
	}
	if got, want := fmt.Sprint(sr.GetAgent().GetTabIds()), fmt.Sprint(running.GetTabIds()); got != want {
		h.fatal("step 3: a second Start started lead again: tabs %s, were %s", got, want)
	}
	t.Log("step 3: a second Start on lead is a no-op")

	// Step 4: whoami in lead's pane, and the pane's process is lead's.
	pane := h.waitPane(ctx, "first-start ask-owner", time.Minute)
	if !strings.Contains(pane, "first-start whoami=lead") {
		h.fatal("step 4: lead's pane doesn't show whoami=lead:\n%s", pane)
	}
	out, code = h.box(ctx, "root", `sock=$(find /run/portenv-team/lead -type s -name default | head -n 1)
ps -o user= -p "$(tmux -S "$sock" display-message -p -t main '#{pane_pid}')"`)
	if code != 0 || strings.TrimSpace(out) != "lead" {
		h.fatal("step 4: lead's pane process isn't lead's: exit %d: %s", code, out)
	}
	t.Log("step 4: whoami in lead's pane is lead")

	// Step 5: lead's ask-owner is in the decision list, pending, and the
	// pane names the same decision.
	ask := h.waitDecision(func(d *typesv1.Decision) bool {
		return d.GetKind() == typesv1.DecisionKind_DECISION_KIND_ASK_OWNER && d.GetFrom() == "lead" &&
			d.GetState() == typesv1.DecisionState_DECISION_STATE_PENDING && strings.Contains(d.GetDetail(), question)
	}, time.Minute)
	if ask == nil {
		h.fatal("step 5: lead's ask-owner isn't in portenvd's decision list; lead's pane:\n%s", pane)
	}
	if !strings.Contains(pane, "first-start ask-owner ok") || !strings.Contains(pane, ask.GetId()) {
		h.fatal("step 5: lead's pane doesn't name decision %s:\n%s", ask.GetId(), pane)
	}
	t.Logf("step 5: lead's ask-owner is decision %s in the list", ask.GetId())
}

// waitRunning waits for portenvd to list lead as running (or needing
// you: its ask-owner may already be waiting), after the owner approved the
// team's first start.
func (h *harness) waitRunning(ctx context.Context, c daemonv1.DaemonServiceClient, within time.Duration) *daemonv1.AgentInfo {
	end := time.Now().Add(within)
	var last string
	for time.Now().Before(end) {
		cctx, cancel := context.WithDeadline(ctx, end)
		r, err := c.ListAgents(cctx, &daemonv1.ListAgentsRequest{Name: workspace})
		cancel()
		if err == nil {
			for _, a := range r.GetAgents() {
				if a.GetId() != "lead" {
					continue
				}
				switch a.GetState() {
				case typesv1.AgentState_AGENT_STATE_RUNNING, typesv1.AgentState_AGENT_STATE_NEEDS_YOU, typesv1.AgentState_AGENT_STATE_IDLE:
					return a
				}
				last = a.GetState().String()
			}
		} else {
			last = err.Error()
		}
		time.Sleep(500 * time.Millisecond)
	}
	h.fatal("step 3: lead isn't running %v after Approve, with no second Start: %s", within, last)
	return nil
}

// lead is lead's row, once portenvd reads the files team init wrote.
func (h *harness) lead(ctx context.Context, c daemonv1.DaemonServiceClient) *daemonv1.AgentInfo {
	var last string
	for range 60 {
		r, err := c.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: workspace})
		if err == nil {
			for _, a := range r.GetAgents() {
				if a.GetId() == "lead" && a.GetFileSha256() != "" {
					return a
				}
			}
			last = fmt.Sprint(r.GetAgents())
		} else {
			last = err.Error()
		}
		time.Sleep(500 * time.Millisecond)
	}
	h.fatal("step 2: portenvd doesn't list lead: %s", last)
	return nil
}

func (h *harness) watch(ctx context.Context, c daemonv1.DaemonServiceClient) {
	st, err := c.WatchTeam(ctx, &daemonv1.WatchTeamRequest{Name: workspace})
	for err == nil {
		var r *daemonv1.WatchTeamResponse
		if r, err = st.Recv(); err == nil {
			if d := r.GetDecision(); d != nil {
				h.mu.Lock()
				h.decisions[d.GetId()] = d
				h.mu.Unlock()
			}
		}
	}
	h.mu.Lock()
	h.watchErr = err
	h.mu.Unlock()
}

func (h *harness) waitDecision(match func(*typesv1.Decision) bool, within time.Duration) *typesv1.Decision {
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		h.mu.Lock()
		for _, d := range h.decisions {
			if match(d) {
				h.mu.Unlock()
				return d
			}
		}
		werr := h.watchErr
		h.mu.Unlock()
		if werr != nil && !strings.Contains(werr.Error(), "context canceled") {
			h.t.Logf("WatchTeam ended: %v", werr)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

// waitPane reads lead's pane, as root in the harness, until it shows want.
func (h *harness) waitPane(ctx context.Context, want string, within time.Duration) string {
	var out string
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		var code int
		out, code = h.box(ctx, "root", `sock=$(find /run/portenv-team/lead -type s -name default | head -n 1)
[ -n "$sock" ] || { echo "no tmux socket under /run/portenv-team/lead"; exit 1; }
tmux -S "$sock" capture-pane -p -J -t main -S -200`)
		if code == 0 && strings.Contains(out, want) {
			return out
		}
		time.Sleep(time.Second)
	}
	h.fatal("step 4: lead's pane never showed %q:\n%s", want, out)
	return ""
}

func (h *harness) startDaemon(ctx context.Context) {
	f, err := os.Create(h.log)
	if err != nil {
		h.t.Fatal(err)
	}
	h.daemon = exec.Command(filepath.Join(h.repo, "bin", "portenvd")) // #nosec G204 -- this repo's build
	h.daemon.Env, h.daemon.Stdout, h.daemon.Stderr = h.env, f, f
	if err := h.daemon.Start(); err != nil {
		h.t.Fatal(err)
	}
	for range 100 {
		if _, err := h.try(ctx, "app", "ping"); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.fatal("portenvd didn't answer")
}

func (h *harness) try(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(h.repo, "bin", "portenv"), args...) // #nosec G204 -- this repo's build
	cmd.Env = h.env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (h *harness) portenv(ctx context.Context, args ...string) string {
	out, err := h.try(ctx, args...)
	if err != nil {
		h.fatal("portenv %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return out
}

func (h *harness) boxID() string {
	b, err := os.ReadFile(filepath.Join(h.root, "mac", "boxes", workspace+".json"))
	if err != nil {
		h.t.Fatal(err)
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.ID == "" {
		h.t.Fatalf("the workspace's id: %v %s", err, b)
	}
	return v.ID
}

// box is the harness's own probe into the workspace (setup and reading
// lead's pane), never portenvd's path.
func (h *harness) box(ctx context.Context, user, sh string) (string, int) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", "-u", user, h.boxName, "bash", "-c", sh) // #nosec G204 -- the test's own script
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var x *exec.ExitError
		if errors.As(err, &x) {
			code = x.ExitCode()
		}
	}
	return strings.TrimSpace(buf.String()), code
}

// fatal fails the test with what's needed to see why: the workspace's team
// layout, the helper's log and portenvd's.
func (h *harness) fatal(format string, a ...any) {
	h.t.Helper()
	h.t.Errorf(format, a...)
	if h.boxName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		out, _ := h.box(ctx, "root", `set +e
echo "--- id work; getent group team"; id work; getent group team
echo "--- agents"; getent passwd | awk -F: '$3>=2000 && $3<3000'
echo "--- /home/work"; stat -c '%A %U:%G %n' /home/work /home/work/`+workspace+` 2>&1
echo "--- /run/portenv-team"; ls -laR /run/portenv-team 2>&1 | head -n 60
echo "--- processes"; ps -eo user:12,pid,args | grep -v ' \[' | head -n 60`)
		h.t.Logf("the workspace:\n%s", out)
		if b, err := exec.Command("docker", "logs", "--tail", "120", h.boxName).CombinedOutput(); err == nil { // #nosec G204 -- the test's container
			h.t.Logf("the workspace's log (last 120 lines):\n%s", b)
		}
	}
	if b, err := os.ReadFile(h.log); err == nil {
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		h.t.Logf("portenvd's log (last 80 lines):\n%s", strings.Join(lines[max(0, len(lines)-80):], "\n"))
	}
	h.t.FailNow()
}

func (h *harness) cleanup() {
	if h.daemon != nil && h.daemon.Process != nil {
		_ = h.daemon.Process.Kill()
		_ = h.daemon.Wait()
	}
	if h.boxName != "" {
		id := strings.TrimPrefix(h.boxName, "portenv-"+namespace+"-")
		_ = exec.Command("docker", "rm", "-f", h.boxName).Run()                                                                     // #nosec G204 -- the test's container
		_ = exec.Command("docker", "volume", "rm", "-f", "portenv-home-"+namespace+"-"+id, "portenv-cache-"+namespace+"-"+id).Run() // #nosec G204 -- its volumes
	}
	// restic in the box wrote the repository as portenv-sync (uid 990): on
	// a Linux host those files are uid 990, so remove them as root in a
	// container.
	_ = exec.Command("docker", "run", "--rm", "-v", h.root+":/scratch", "--entrypoint", "rm", h.image, "-rf", "/scratch/mac/Repositories").Run() // #nosec G204 -- the test's scratch folder
	_ = os.RemoveAll(h.root)
}
