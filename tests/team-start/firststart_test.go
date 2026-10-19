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
//     portenvd's decision list (the WatchTeam stream the app reads);
//  6. lead has its own clone (ADR 0022 §2a, R-0315): /home/lead/acme,
//     lead's, on agent/lead with the remote owner, and its runner started
//     there; work's repository is never agent-writable: lead can't create,
//     change or remove anything in the project or its .git, the system git
//     config trusts nothing in the project, and the project has no
//     core.sharedRepository; lead commits in its own clone, and work's git
//     in the project still works;
//  7. approve and start builder: its start checks the project again, and
//     whoami in builder's pane says builder; builder reads the project,
//     has its own clone, and can't read lead's;
//  8. builder tampers with its own clone's config and hooks (each would
//     leave a marker naming the uid that ran it), commits on agent/builder
//     and runs portenv share; work runs git status, log, diff and a commit
//     in the project, and the owner's Fetch (portenv app fetch-agent)
//     brings refs/remotes/agents/builder with builder's commit, by the
//     bundle; no marker of work's uid appears, work's git in builder's
//     clone is refused, and the bundle is gone (ADR 0022 §2a points 3, 5);
//  9. a message wakes an idle agent (CC-0498): reviewer's runner becomes a
//     stand-in named claude, and the owner's message brings Portenv's fixed
//     wake line, and only that line, into reviewer's own main pane;
//  10. reviewer, started in step 9, is a lens with read_clones: [builder]
//     (the template's): it reads builder's files, can't write any, can't list
//     builder's home, and its git in builder's clone is refused; lead,
//     without the grant, can't read them; the grant is a named-user ACL,
//     never group team's (ADR 0022 §2a point 4);
//  11. the move (ADR 0022 §2a point 6): with every agent stopped, the
//     workspace is put back the old way (the project group team's,
//     setgid, g:team:rwX, core.sharedRepository, Portenv's safe.directory,
//     an R-0280 worktree for builder holding its uncommitted file, a hook
//     builder wrote, a file lead wrote, builder's clone not made yet);
//     lead's start moves it and waits for the owner: .git is work's alone,
//     the settings are gone, builder's clone holds its file uncommitted,
//     nothing is deleted; Approve removes the hook and the worktree and
//     keeps lead's file, lead starts, and the earlier checks pass again;
//  12. a hand-off (CC-0507): lead hands a file to builder, and builder finds
//     it in ~/inbox/lead/…, builder's, readable only by builder, its home
//     still closed to others.
//
// The harness's own docker exec only sets the project up (step 1, as work),
// tries what each agent may and may not do (as that agent), and reads the
// panes as root; portenvd reaches the box only through the agent channel.
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
	script2   = project + "/second-start.sh"
	script3   = project + "/third-start.sh"
	question  = "R-0296 first start: may lead begin?"
)

// firstStart is lead's runner: what it prints is what step 4 and 5 read.
const firstStart = `#!/bin/bash
echo "first-start whoami=$(whoami)"
echo "first-start pwd=$PWD"
if out=$(portenv ask-owner "` + question + `" 2>&1); then
  echo "first-start ask-owner ok: $out"
else
  echo "first-start ask-owner failed: $out"
fi
exec sleep 1000000
`

// thirdStart is reviewer's runner.
const thirdStart = `#!/bin/bash
echo "third-start whoami=$(whoami)"
exec sleep 1000000
`

// secondStart is builder's runner.
const secondStart = `#!/bin/bash
echo "second-start whoami=$(whoami)"
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

	// Step 1: portenv team init in the project, a git repository, as the
	// owner (work) in the box; lead's and builder's runners become the
	// scripts.
	out, code := h.box(ctx, "work", fmt.Sprintf(`set -eu
mkdir -p %[1]s && cd %[1]s
git init -q
portenv team init
cat > %[2]s <<'EOF'
%[3]sEOF
cat > %[4]s <<'EOF'
%[5]sEOF
cat > %[6]s <<'EOF'
%[7]sEOF
chmod 0755 %[2]s %[4]s %[6]s
sed -i 's|^runner: .*$|runner: bash %[2]s|' .portenv/agents/lead.md
grep -q '^runner: bash %[2]s$' .portenv/agents/lead.md
sed -i 's|^runner: .*$|runner: bash %[4]s|' .portenv/agents/builder.md
grep -q '^runner: bash %[4]s$' .portenv/agents/builder.md
sed -i 's|^runner: .*$|runner: bash %[6]s|' .portenv/agents/reviewer.md
grep -q '^runner: bash %[6]s$' .portenv/agents/reviewer.md
grep -q '^  read_clones: \[builder\]$' .portenv/agents/reviewer.md
git add -A
git -c user.name=Owner -c user.email=owner@example.invalid commit -q -m "first commit"`, project, script, firstStart, script2, secondStart, script3, thirdStart))
	if code != 0 {
		h.fatal("step 1 (portenv team init in %s): exit %d: %s", project, code, out)
	}
	t.Logf("step 1: %s", out)

	// Step 2: approve lead, with the sha the app shows.
	lead := h.agent(ctx, c, "lead")
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
	running := h.waitRunning(ctx, c, "lead", 30*time.Second)
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
	pane := h.waitPane(ctx, "lead", "first-start ask-owner", time.Minute)
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

	// Step 6: lead's own clone (ADR 0022 §2a, R-0315). It's lead's, in
	// lead's 0700 home, on agent/lead with the remote owner, and lead's
	// runner started in it.
	if !strings.Contains(pane, "first-start pwd=/home/lead/"+workspace) {
		h.fatal("step 6: lead's runner didn't start in its clone:\n%s", pane)
	}
	out, code = h.box(ctx, "root", fmt.Sprintf(`set -eu
stat -c '%%U %%a %%n' /home/lead /home/lead/%[1]s`, workspace))
	if code != 0 || !strings.Contains(out, "lead 700 /home/lead\n") || !strings.Contains(out, "lead ") || !strings.HasSuffix(out, "/home/lead/"+workspace) {
		h.fatal("step 6: lead's clone isn't lead's, in its 0700 home: exit %d: %s", code, out)
	}
	out, code = h.box(ctx, "lead", fmt.Sprintf(`set -eu
cd /home/lead/%s
echo "branch=$(git symbolic-ref --short HEAD)"
echo "remotes=$(git remote | tr '\n' ' ')"
echo "owner=$(git remote get-url owner)"
echo "log=$(git log -1 --format=%%s)"`, workspace))
	for _, want := range []string{"branch=agent/lead", "remotes=owner ", "owner=" + project, "log=first commit"} {
		if code != 0 || !strings.Contains(out, want) {
			h.fatal("step 6: lead's clone: want %q: exit %d:\n%s", want, code, out)
		}
	}
	t.Logf("step 6: lead's clone:\n%s", out)
	// work's repository refuses every write of lead's: in the tree and
	// in .git, a new file, a change, a removal and a rename; and git in
	// the project as lead can't write its index.
	out, code = h.box(ctx, "lead", fmt.Sprintf(`cd %s || exit 2
try() { if "$@" 2>/dev/null; then echo "WROTE: $*"; else echo "refused: $*"; fi; }
try touch from-lead.txt
try sh -c 'echo x >> first-start.sh'
try sh -c 'echo "[core]" >> .git/config'
try touch .git/hooks/post-checkout
try sh -c 'echo x >> .git/HEAD'
try mkdir .git/refs/heads/evil
try rm -f .git/description
try mv .git/config .git/config.old
try git -c user.name=Lead -c user.email=lead@example.invalid commit -q --allow-empty -m "lead in work's repository"
true`, project))
	if code != 0 || strings.Contains(out, "WROTE") || strings.Count(out, "refused:") != 9 {
		h.fatal("step 6: lead wrote to work's repository: exit %d:\n%s", code, out)
	}
	t.Logf("step 6: work's repository refuses lead:\n%s", out)
	// Portenv trusts nothing in the project in the system git config, and
	// the project shares nothing through core.sharedRepository.
	out, _ = h.box(ctx, "root", fmt.Sprintf(`git config --system --get-all safe.directory | grep -F %[1]s; echo "safe.directory: $?"
git config --file %[1]s/.git/config --get core.sharedRepository; echo "sharedRepository: $?"
getfacl -cp %[1]s/.git/config %[1]s/.git/hooks 2>&1`, project))
	if !strings.Contains(out, "safe.directory: 1") || !strings.Contains(out, "sharedRepository: 1") || strings.Contains(out, "group:team:rw") || !strings.Contains(out, "group:team:r") {
		h.fatal("step 6: the system config trusts the project, it shares its repository, or team can write it:\n%s", out)
	}
	t.Logf("step 6: no safe.directory, no sharedRepository, team reads only:\n%s", out)
	// Lead commits in its own clone; work's git in the project still
	// works, and sees nothing of lead's.
	out, code = h.box(ctx, "lead", fmt.Sprintf(`set -eu
cd /home/lead/%s
echo "from lead" > from-lead.txt
git add from-lead.txt
git -c user.name=Lead -c user.email=lead@example.invalid commit -q -m "from lead"
git log -1 --format=%%s`, workspace))
	if code != 0 || !strings.HasSuffix(out, "from lead") {
		h.fatal("step 6: lead couldn't commit in its own clone: exit %d: %s", code, out)
	}
	out, code = h.box(ctx, "work", fmt.Sprintf(`set -eu
cd %s
git status --short
git log -1 --format=%%s
test ! -e from-lead.txt`, project))
	if code != 0 || !strings.HasSuffix(out, "first commit") {
		h.fatal("step 6: work's git in the project after lead's commit: exit %d: %s", code, out)
	}
	t.Log("step 6: lead committed in its own clone; work's project is as it was")

	// Step 7: start builder. Its start checks the project again and must
	// succeed.
	builder := h.agent(ctx, c, "builder")
	if builder.GetRunner() != "bash "+script2 {
		h.fatal("step 7: builder's runner is %q, not the script", builder.GetRunner())
	}
	if _, err := c.ApproveAgentFile(ctx, &daemonv1.ApproveAgentFileRequest{Name: workspace, AgentId: "builder", FileSha256: builder.GetFileSha256()}); err != nil {
		h.fatal("step 7 (approve builder): %v", err)
	}
	if _, err := c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: workspace, AgentId: "builder"}); err != nil {
		h.fatal("step 7 (start builder after lead's commit): %v", err)
	}
	h.waitRunning(ctx, c, "builder", 30*time.Second)
	if bp := h.waitPane(ctx, "builder", "second-start whoami=", time.Minute); !strings.Contains(bp, "second-start whoami=builder") {
		h.fatal("step 7: builder's pane doesn't show whoami=builder:\n%s", bp)
	}
	// Builder reads the project, has its own clone, and can't read lead's.
	out, code = h.box(ctx, "builder", fmt.Sprintf(`set -u
head -n 1 %[1]s/second-start.sh
git -C /home/builder/%[2]s symbolic-ref --short HEAD
if ls /home/lead/%[2]s >/dev/null 2>&1; then echo "READ lead's clone"; else echo "lead's clone refused"; fi`, project, workspace))
	if code != 0 || !strings.Contains(out, "#!/bin/bash") || !strings.Contains(out, "agent/builder") || !strings.Contains(out, "lead's clone refused") {
		h.fatal("step 7: builder's reads: exit %d:\n%s", code, out)
	}
	t.Log("step 7: builder started; it reads the project, has its own clone on agent/builder, and can't read lead's")

	// Step 8: builder tampers with its own clone, so that anything git
	// honoured from it, for whoever runs git, would leave a marker named
	// after that uid; builder's own commit runs them, as builder.
	out, code = h.box(ctx, "root", `mkdir -p /tmp/portenv-markers && chmod 1777 /tmp/portenv-markers && id -u builder && id -u work`)
	ids := strings.Fields(out)
	if code != 0 || len(ids) != 2 {
		h.fatal("step 8: the markers folder: exit %d: %s", code, out)
	}
	builderUID, workUID := ids[0], ids[1]
	out, code = h.box(ctx, "builder", fmt.Sprintf(`set -eu
cd /home/builder/%[1]s
mk='touch /tmp/portenv-markers/$(id -u)'
mkdir -p ~/hooks
for hk in pre-commit post-commit reference-transaction post-checkout post-merge post-rewrite pre-auto-gc fsmonitor-watchman; do
  printf '#!/bin/sh\n%%s-hook-%%s\n' "$mk" "$hk" > ~/hooks/$hk
  chmod 0755 ~/hooks/$hk
done
git config core.hooksPath ~/hooks
for k in core.fsmonitor core.pager diff.external core.sshCommand uploadpack.packObjectsHook; do
  git config "$k" "$mk-config-$k; false"
done
for k in filter.evil.clean filter.evil.smudge; do
  git config "$k" "$mk-config-$k; cat"
done
echo '* filter=evil' > .gitattributes
echo "from builder" > from-builder.txt
git add .gitattributes from-builder.txt
git -c user.name=Builder -c user.email=builder@example.invalid commit -q -m "from builder" || git -c user.name=Builder -c user.email=builder@example.invalid -c core.hooksPath=/dev/null commit -q -m "from builder"
git log -1 --format=%%s
PORTENV_AGENT=builder PORTENV_SOCKET=/run/portenv-team/builder/portenv.sock portenv share --text "the gate's tampered branch"
ls /tmp/portenv-markers`, workspace))
	if code != 0 || !strings.Contains(out, "from builder") || !strings.Contains(out, builderUID+"-hook-pre-commit") {
		h.fatal("step 8: builder's tampered commit and share (its own hooks must run, as builder): exit %d:\n%s", code, out)
	}
	t.Logf("step 8: builder tampered with its clone, committed and shared:\n%s", out)
	// work's everyday git in the project, then the owner's Fetch, then
	// work's git again with builder's branch in the repository.
	everyday := func(when string) string {
		return fmt.Sprintf(`set -eu
cd %s
git status --short
git log -1 --format=%%s
git diff --stat HEAD
git -c user.name=Owner -c user.email=owner@example.invalid commit -q --allow-empty -m "owner, %s"`, project, when)
	}
	if out, code = h.box(ctx, "work", everyday("before the fetch")); code != 0 {
		h.fatal("step 8: work's git before the fetch: exit %d: %s", code, out)
	}
	fetched := h.portenv(ctx, "app", "fetch-agent", workspace, "builder")
	if !strings.Contains(fetched, "refs/remotes/agents/builder") {
		h.fatal("step 8: the owner's Fetch said %q", fetched)
	}
	out, code = h.box(ctx, "work", fmt.Sprintf(`set -eu
cd %s
git log -1 --format=%%s refs/remotes/agents/builder
git log --all -p >/dev/null
git diff --stat HEAD refs/remotes/agents/builder
git show --stat refs/remotes/agents/builder >/dev/null`, project))
	if code != 0 || !strings.HasPrefix(out, "from builder") {
		h.fatal("step 8: builder's commit isn't refs/remotes/agents/builder in work's repository: exit %d:\n%s", code, out)
	}
	if out, code = h.box(ctx, "work", everyday("after the fetch")); code != 0 {
		h.fatal("step 8: work's git after the fetch: exit %d: %s", code, out)
	}
	t.Logf("step 8: the owner's Fetch: %s", fetched)
	// No marker of work's: nothing of builder's ran for work. work's git
	// in builder's clone is refused; no system safe.directory names it;
	// the bundle is gone.
	out, _ = h.box(ctx, "root", fmt.Sprintf(`ls /tmp/portenv-markers
echo "---"
runuser -u work -- git -C /home/builder/%[1]s status >/dev/null 2>&1; echo "work in builder's clone: $?"
git config --system --get-all safe.directory | grep -F /home/builder; echo "safe.directory: $?"
test -e /home/work/.portenv/team/handoff/builder.bundle; echo "bundle: $?"`, workspace))
	if strings.Contains(out, workUID+"-") || strings.Contains(out, "work in builder's clone: 0") || !strings.Contains(out, "safe.directory: 1") || !strings.Contains(out, "bundle: 1") {
		h.fatal("step 8: something of builder's ran for work, work's git opened builder's clone, or the bundle stayed:\n%s", out)
	}
	t.Logf("step 8: no marker of work's (uid %s), work's git refused in builder's clone, bundle gone:\n%s", workUID, out)

	h.wakeOnMessage(ctx, c)
	// Step 10: reviewer, a lens on builder's clone (the template's
	// read_clones: [builder]). Step 9 already approved and started it, with
	// its wake stand-in runner; Approve and Start again are no-ops.
	reviewer := h.agent(ctx, c, "reviewer")
	if _, err := c.ApproveAgentFile(ctx, &daemonv1.ApproveAgentFileRequest{Name: workspace, AgentId: "reviewer", FileSha256: reviewer.GetFileSha256()}); err != nil {
		h.fatal("step 10 (approve reviewer): %v", err)
	}
	if _, err := c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: workspace, AgentId: "reviewer"}); err != nil {
		h.fatal("step 10 (start reviewer): %v", err)
	}
	h.waitRunning(ctx, c, "reviewer", 30*time.Second)
	out, code = h.box(ctx, "reviewer", fmt.Sprintf(`cd / || exit 2
clone=/home/builder/%s
try() { if "$@" >/dev/null 2>&1; then echo "DID: $*"; else echo "refused: $*"; fi; }
cat $clone/from-builder.txt
try ls /home/builder
try touch $clone/from-reviewer.txt
try sh -c "echo x >> $clone/from-builder.txt"
try mkdir $clone/.git/refs/heads/from-reviewer
try git -C $clone log -1
true`, workspace))
	if code != 0 || !strings.HasPrefix(out, "from builder") || strings.Contains(out, "DID:") || strings.Count(out, "refused:") != 5 {
		h.fatal("step 10: reviewer must read builder's files and nothing more: exit %d:\n%s", code, out)
	}
	t.Logf("step 10: reviewer reads builder's clone, and nothing more:\n%s", out)
	out, _ = h.box(ctx, "lead", fmt.Sprintf(`cat /home/builder/%s/from-builder.txt >/dev/null 2>&1; echo "lead reads builder's clone: $?"`, workspace))
	if !strings.Contains(out, "lead reads builder's clone: 1") {
		h.fatal("step 10: lead, without the grant, read builder's clone:\n%s", out)
	}
	out, _ = h.box(ctx, "root", fmt.Sprintf(`getfacl -cp /home/builder /home/builder/%[1]s/from-builder.txt 2>&1`, workspace))
	if !strings.Contains(out, "user:reviewer:--x") || !strings.Contains(out, "user:reviewer:r--") || strings.Contains(out, "group:team") || strings.Contains(out, "user:reviewer:rw") || strings.Contains(out, "user:lead") {
		h.fatal("step 10: builder's home and clone must give reviewer (by name) traverse and read only:\n%s", out)
	}
	t.Logf("step 10: lead can't read builder's clone; the ACLs:\n%s", out)

	// Step 11: the move. Every agent stops first.
	for _, id := range []string{"lead", "builder", "reviewer"} {
		if _, err := c.StopAgent(ctx, &daemonv1.StopAgentRequest{Name: workspace, AgentId: id}); err != nil {
			h.fatal("step 11 (stop %s): %v", id, err)
		}
	}
	out, code = h.box(ctx, "root", fmt.Sprintf(`set -eu
p=%[1]s
runuser -u work -- git -C $p worktree add -q .worktrees/builder-old -b agent/builder
rm -rf /home/builder/%[2]s
chgrp -R team $p
find $p -type d -exec chmod g+rwxs {} +
find $p -type f -exec chmod g+rw {} +
setfacl -R -m g:team:rwX $p
find $p -type d -exec setfacl -d -m g:team:rwX {} +
runuser -u work -- git -C $p config core.sharedRepository group
git config --system --add safe.directory $p
git config --system --add safe.directory $p/.worktrees/builder-old
runuser -u builder -- sh -c 'echo "builder, not committed" > %[1]s/.worktrees/builder-old/wip.txt'
runuser -u builder -- sh -c 'printf "#!/bin/sh\ntouch /tmp/portenv-markers/\$(id -u)-old-hook\n" > %[1]s/.git/hooks/post-checkout && chmod 0775 %[1]s/.git/hooks/post-checkout'
runuser -u lead -- sh -c 'echo "lead notes" > %[1]s/lead-notes.txt'
stat -c '%%U %%n' $p/.git/hooks/post-checkout $p/lead-notes.txt $p/.worktrees/builder-old/wip.txt`, project, workspace))
	if code != 0 {
		h.fatal("step 11: putting the workspace back the old way: exit %d:\n%s", code, out)
	}
	t.Logf("step 11: the workspace, the old way:\n%s", out)
	var startErr error
	for range 20 {
		_, startErr = c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: workspace, AgentId: "lead"})
		if startErr == nil || !strings.Contains(startErr.Error(), "needs every agent stopped") {
			break
		}
		time.Sleep(500 * time.Millisecond) // the stopped agents' processes are still going
	}
	if startErr == nil || !strings.Contains(startErr.Error(), "own copies") {
		h.fatal("step 11: lead's start didn't wait for the owner's answer to the move: %v", startErr)
	}
	move := h.waitDecision(func(d *typesv1.Decision) bool {
		return d.GetKind() == typesv1.DecisionKind_DECISION_KIND_TEAM_START && d.GetState() == typesv1.DecisionState_DECISION_STATE_PENDING && len(d.GetMigrationItems()) > 0
	}, time.Minute)
	if move == nil {
		h.fatal("step 11: no pending team_start decision with the move's record")
	}
	byPath := map[string]*typesv1.MigrationItem{}
	var record []string
	for _, i := range move.GetMigrationItems() {
		byPath[i.GetPath()] = i
		record = append(record, fmt.Sprintf("%s %s %s %s choice=%v: %s", i.GetId(), i.GetKind(), i.GetPath(), i.GetAgent(), i.GetChoice(), i.GetNote()))
	}
	hook, wt, notes := byPath[".git/hooks/post-checkout"], byPath[".worktrees/builder-old"], byPath["lead-notes.txt"]
	if hook == nil || hook.GetKind() != "git" || hook.GetAgent() != "builder" || wt == nil || wt.GetKind() != "worktree" || wt.GetAgent() != "builder" || notes == nil || notes.GetKind() != "file" || notes.GetAgent() != "lead" {
		h.fatal("step 11: the move's record:\n%s", strings.Join(record, "\n"))
	}
	t.Logf("step 11: the move's record:\n%s", strings.Join(record, "\n"))
	// Before the answer: .git work's alone, the settings gone, builder's
	// clone with its file uncommitted, lead's file in lead's clone and now
	// work's, and nothing deleted.
	out, _ = h.box(ctx, "root", fmt.Sprintf(`p=%[1]s
echo "not work's in .git: $(find $p/.git ! -user work | wc -l)"
echo "team writes in .git: $(getfacl -Rcp $p/.git 2>/dev/null | grep -c '^group:team:.w')"
git config --file $p/.git/config --get core.sharedRepository; echo "sharedRepository: $?"
git config --system --get-all safe.directory | grep -F $p; echo "safe.directory: $?"
stat -c '%%U %%n' $p/lead-notes.txt $p/.git/hooks/post-checkout $p/.worktrees/builder-old/wip.txt
runuser -u builder -- git -C /home/builder/%[2]s symbolic-ref --short HEAD
runuser -u builder -- git -C /home/builder/%[2]s status --porcelain
runuser -u lead -- cat /home/lead/%[2]s/lead-notes.txt`, project, workspace))
	for _, want := range []string{"not work's in .git: 0", "team writes in .git: 0", "sharedRepository: 1", "safe.directory: 1", "work " + project + "/lead-notes.txt", "agent/builder", "?? wip.txt", "lead notes", "/.git/hooks/post-checkout", "/.worktrees/builder-old/wip.txt"} {
		if !strings.Contains(out, want) {
			h.fatal("step 11: after the move, want %q:\n%s", want, out)
		}
	}
	t.Logf("step 11: after the move, before the answer:\n%s", out)
	if _, err := c.AnswerDecision(ctx, &daemonv1.AnswerDecisionRequest{Name: workspace, DecisionId: move.GetId(), Approve: true, RemoveItems: []string{hook.GetId(), wt.GetId()}}); err != nil {
		h.fatal("step 11 (the owner's answer): %v", err)
	}
	h.waitRunning(ctx, c, "lead", time.Minute)
	out, _ = h.box(ctx, "root", fmt.Sprintf(`p=%s
for f in .git/hooks/post-checkout .worktrees/builder-old .git/worktrees/builder-old; do test -e $p/$f; echo "$f: $?"; done
stat -c '%%U %%n' $p/lead-notes.txt`, project))
	for _, want := range []string{".git/hooks/post-checkout: 1", ".worktrees/builder-old: 1", ".git/worktrees/builder-old: 1", "work " + project + "/lead-notes.txt"} {
		if !strings.Contains(out, want) {
			h.fatal("step 11: after the answer, want %q:\n%s", want, out)
		}
	}
	t.Logf("step 11: the answer removed the hook and the worktree, kept lead's file; lead runs:\n%s", out)
	// Every earlier check, again: work's repository refuses lead, the
	// team reads only, and builder's branch comes back with nothing of
	// builder's run for work.
	out, code = h.box(ctx, "lead", fmt.Sprintf(`cd %s || exit 2
try() { if "$@" 2>/dev/null; then echo "WROTE: $*"; else echo "refused: $*"; fi; }
try touch from-lead.txt
try sh -c 'echo "[core]" >> .git/config'
try touch .git/hooks/post-checkout
try mkdir .git/refs/heads/evil
true`, project))
	if code != 0 || strings.Contains(out, "WROTE") || strings.Count(out, "refused:") != 4 {
		h.fatal("step 11: after the move, lead wrote to work's repository: exit %d:\n%s", code, out)
	}
	out, _ = h.box(ctx, "root", fmt.Sprintf(`getfacl -Rcp %[1]s 2>/dev/null | grep -c '^group:team:.w'; git config --system --get-all safe.directory | grep -c -F %[1]s`, project))
	if strings.Fields(out)[0] != "0" || !strings.HasSuffix(out, "0") {
		h.fatal("step 11: after the move, team can write the project or the system config trusts it:\n%s", out)
	}
	if _, err := c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: workspace, AgentId: "builder"}); err != nil {
		h.fatal("step 11 (start builder after the move): %v", err)
	}
	h.waitRunning(ctx, c, "builder", time.Minute)
	if out, code = h.box(ctx, "builder", fmt.Sprintf(`set -eu
cd /home/builder/%s
git add wip.txt
git -c user.name=Builder -c user.email=builder@example.invalid -c core.hooksPath=/dev/null commit -q -m "after the move"`, workspace)); code != 0 {
		h.fatal("step 11: builder's commit after the move: exit %d: %s", code, out)
	}
	h.portenv(ctx, "app", "fetch-agent", workspace, "builder")
	out, _ = h.box(ctx, "root", fmt.Sprintf(`runuser -u work -- git -C %s log -1 --format=%%s refs/remotes/agents/builder; ls /tmp/portenv-markers`, project))
	if !strings.HasPrefix(out, "after the move") || strings.Contains(out, workUID+"-") {
		h.fatal("step 11: builder's branch after the move, or a marker of work's:\n%s", out)
	}
	t.Log("step 11: after the move every check passes again: lead refused, team reads only, builder's branch fetched, no marker of work's")

	// Step 12: lead hands a file to builder (CC-0507): the unpack runs as
	// builder, into builder's 0700 home.
	out, code = h.box(ctx, "lead", `set -eu
mkdir -p ~/handoff && echo "for builder" > ~/handoff/note.txt
PORTENV_AGENT=lead PORTENV_SOCKET=/run/portenv-team/lead/portenv.sock portenv handoff builder /home/lead/handoff/note.txt --note "the gate's hand-off"`)
	if code != 0 {
		h.fatal("step 12: lead's hand-off to builder: exit %d:\n%s", code, out)
	}
	t.Logf("step 12: lead's hand-off: %s", out)
	out, code = h.box(ctx, "builder", `set -eu
f=$(ls -d ~/inbox/lead/*/note.txt)
cat "$f"
stat -c '%U %a %n' "$f"`)
	if code != 0 || !strings.HasPrefix(out, "for builder") || !strings.Contains(out, "builder 600 /home/builder/inbox/lead/") {
		h.fatal("step 12: builder's inbox: exit %d:\n%s", code, out)
	}
	t.Logf("step 12: builder's inbox:\n%s", out)
	out, _ = h.box(ctx, "root", `stat -c '%U %a' /home/builder; getfacl -cp /home/builder 2>/dev/null | grep -v '^#'
for who in lead reviewer work; do runuser -u $who -- sh -c 'cat /home/builder/inbox/lead/*/note.txt' >/dev/null 2>&1; echo "$who reads it: $?"; done`)
	if !strings.HasPrefix(out, "builder 7") || !strings.Contains(out, "other::---") || strings.Contains(out, "reads it: 0") {
		h.fatal("step 12: builder's home isn't closed to others, or another user read the hand-off:\n%s", out)
	}
	t.Logf("step 12: builder's home and the hand-off are builder's alone:\n%s", out)
	// A file from the project, with the project closed to other users (as
	// the app makes it: agents reach it through group team only).
	if out, code = h.box(ctx, "root", fmt.Sprintf(`chmod o-rwx %s && stat -c '%%A %%U:%%G' %s`, project, project)); code != 0 {
		h.fatal("step 12: closing the project to other users: exit %d: %s", code, out)
	}
	out, code = h.box(ctx, "lead", fmt.Sprintf(`PORTENV_AGENT=lead PORTENV_SOCKET=/run/portenv-team/lead/portenv.sock portenv handoff builder %s`, script))
	if code != 0 {
		h.fatal("step 12: lead's hand-off of a project file to builder: exit %d:\n%s", code, out)
	}
	out, code = h.box(ctx, "builder", `stat -c '%U %a %n' ~/inbox/lead/*/first-start.sh`)
	if code != 0 || !strings.HasPrefix(out, "builder 600 ") {
		h.fatal("step 12: the project file in builder's inbox: exit %d:\n%s", code, out)
	}
	t.Logf("step 12: a project file handed over, with the project closed to others:\n%s", out)
}

// waitRunning waits for portenvd to list the agent as running (or needing
// you: its ask-owner may already be waiting), after it was started.
func (h *harness) waitRunning(ctx context.Context, c daemonv1.DaemonServiceClient, id string, within time.Duration) *daemonv1.AgentInfo {
	end := time.Now().Add(within)
	var last string
	for time.Now().Before(end) {
		cctx, cancel := context.WithDeadline(ctx, end)
		r, err := c.ListAgents(cctx, &daemonv1.ListAgentsRequest{Name: workspace})
		cancel()
		if err == nil {
			for _, a := range r.GetAgents() {
				if a.GetId() != id {
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
	h.fatal("%s isn't running %v after it was started: %s", id, within, last)
	return nil
}

// agent is the agent's row, once portenvd reads the files team init wrote.
func (h *harness) agent(ctx context.Context, c daemonv1.DaemonServiceClient, id string) *daemonv1.AgentInfo {
	var last string
	for range 60 {
		r, err := c.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: workspace})
		if err == nil {
			for _, a := range r.GetAgents() {
				if a.GetId() == id && a.GetFileSha256() != "" {
					return a
				}
			}
			last = fmt.Sprint(r.GetAgents())
		} else {
			last = err.Error()
		}
		time.Sleep(500 * time.Millisecond)
	}
	h.fatal("portenvd doesn't list %s: %s", id, last)
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

// waitPane reads the agent's pane, as root in the harness, until it shows
// want.
func (h *harness) waitPane(ctx context.Context, id, want string, within time.Duration) string {
	var out string
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		var code int
		out, code = h.box(ctx, "root", fmt.Sprintf(`sock=$(find /run/portenv-team/%[1]s -type s -name default | head -n 1)
[ -n "$sock" ] || { echo "no tmux socket under /run/portenv-team/%[1]s"; exit 1; }
tmux -S "$sock" capture-pane -p -J -t main -S -200`, id))
		if code == 0 && strings.Contains(out, want) {
			return out
		}
		time.Sleep(time.Second)
	}
	h.fatal("%s's pane never showed %q:\n%s", id, want, out)
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
echo "--- the project's git"; stat -c '%A %U:%G %n' `+project+`/.git `+project+`/.git/config `+project+`/.git/index `+project+`/.git/hooks 2>&1; getfacl -cp `+project+`/.git/config 2>&1
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

// Step 6 (CC-0498): a message wakes an idle agent. builder's runner
// becomes a stand-in named claude, so the runner table's claude pattern
// applies: it waits at "> " alone, as Claude Code's empty input does, and
// echoes each line typed in, then reads its inbox, as a woken runner
// would. The owner's message must bring Portenv's fixed line, and only
// that line, into builder's own main pane.
const (
	wakeRunner = project + "/wake/claude"
	wakeText   = "CC-0498 wake check: build the parser"
	wakeLine   = "[Portenv] new message from the owner; run portenv inbox"
)

const wakeStandIn = `#!/bin/bash
while IFS= read -r -p '> ' line; do
  echo "wake-read: $line"
  if out=$(portenv inbox 2>&1); then
    echo "wake-inbox ok, $(printf '%s\n' "$out" | grep -c .) lines"
  else
    echo "wake-inbox failed"
  fi
done
exec sleep 1000000
`

func (h *harness) wakeOnMessage(ctx context.Context, c daemonv1.DaemonServiceClient) {
	out, code := h.box(ctx, "work", fmt.Sprintf(`set -eu
cd %[1]s
mkdir -p wake
cat > %[2]s <<'EOF'
%[3]sEOF
chmod 0755 wake %[2]s
sed -i 's|^runner: .*$|runner: %[2]s|' .portenv/agents/reviewer.md
grep -q '^runner: %[2]s$' .portenv/agents/reviewer.md`, project, wakeRunner, wakeStandIn))
	if code != 0 {
		h.fatal("step 9 (reviewer's stand-in runner): exit %d: %s", code, out)
	}
	var reviewer *daemonv1.AgentInfo
	for range 60 {
		r, err := c.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: workspace})
		if err == nil {
			for _, a := range r.GetAgents() {
				if a.GetId() == "reviewer" && a.GetRunner() == wakeRunner && a.GetFileSha256() != "" {
					reviewer = a
				}
			}
		}
		if reviewer != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if reviewer == nil {
		h.fatal("step 9: portenvd doesn't list reviewer with runner %s", wakeRunner)
	}
	if off := reviewer.GetRoutinesOff(); off != "" {
		h.fatal("step 9: reviewer's runner is claude, but its row says %q", off)
	}
	if _, err := c.ApproveAgentFile(ctx, &daemonv1.ApproveAgentFileRequest{Name: workspace, AgentId: "reviewer", FileSha256: reviewer.GetFileSha256()}); err != nil {
		h.fatal("step 9 (approve reviewer): %v", err)
	}
	if _, err := c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: workspace, AgentId: "reviewer"}); err != nil {
		h.fatal("step 9 (start reviewer): %v", err)
	}
	h.agentPane(ctx, "reviewer", ">", time.Minute)
	if _, err := c.SendMessage(ctx, &daemonv1.SendMessageRequest{Name: workspace, To: "reviewer", Text: wakeText}); err != nil {
		h.fatal("step 9 (the owner's message to reviewer): %v", err)
	}
	pane := h.agentPane(ctx, "reviewer", "wake-inbox", 2*time.Minute)
	if !strings.Contains(pane, "wake-read: "+wakeLine) || !strings.Contains(pane, "wake-inbox ok") {
		h.fatal("step 9: reviewer's pane didn't read the wake line, then its inbox:\n%s", pane)
	}
	if strings.Contains(pane, wakeText) {
		h.fatal("step 9: the message's text reached reviewer's pane:\n%s", pane)
	}
	if n := strings.Count(pane, "wake-read: "); n != 1 {
		h.fatal("step 9: reviewer's pane read %d lines, want one wake line:\n%s", n, pane)
	}
	for _, l := range strings.Split(pane, "\n") {
		if strings.Contains(l, "wake-") {
			h.t.Logf("step 9: reviewer's pane: %s", strings.TrimSpace(l))
		}
	}
}

// agentPane reads an agent's main pane, as root in the harness, until it
// shows want.
func (h *harness) agentPane(ctx context.Context, agent, want string, within time.Duration) string {
	var out string
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		var code int
		out, code = h.box(ctx, "root", `sock=$(find /run/portenv-team/`+agent+` -type s -name default | head -n 1)
[ -n "$sock" ] || { echo "no tmux socket under /run/portenv-team/`+agent+`"; exit 1; }
tmux -S "$sock" capture-pane -p -J -t main -S -200`)
		if code == 0 && strings.Contains(out, want) {
			return out
		}
		time.Sleep(time.Second)
	}
	h.fatal("step 6: %s's pane never showed %q:\n%s", agent, want, out)
	return ""
}
