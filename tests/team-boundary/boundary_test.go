// SPDX-License-Identifier: Apache-2.0

package teamboundary

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// A step runs script as agent; exit 0 means the boundary held.
type step struct {
	agent  string
	script string
}

// A row is one rule of the boundary (ADR 0022, section 12, and R-0245 to
// R-0247). mutate (as root) loosens exactly what the row protects and
// appends the commands that undo it to $UNDO, including removing anything
// the agent then managed to make; the row must then fail.
type row struct {
	name     string
	steps    []step
	mutate   string
	residual string // set: a stated residual risk, reported, not gated
	pending  string // set: waiting for another lane's code
}

// connects is in every step: whether the caller can connect to a Unix
// socket.
const prelude = `connects() { python3 -c 'import socket, sys
s = socket.socket(socket.AF_UNIX); s.settimeout(2); s.connect(sys.argv[1])' "$1" 2>/dev/null; }
`

// keep records a path's mode and owner in $UNDO, before a mutation.
const keep = `keep() { for p in "$@"; do echo "chmod $(stat -c %a "$p") '$p'; chown $(stat -c %u:%g "$p") '$p'" >> "$UNDO"; done; }
`

var rows = []row{
	{
		name: "an agent can't reach the owner's tmux server",
		steps: []step{{"builder", `! tmux -S /tmp/tmux-1000/default ls 2>/dev/null &&
tmux -S /run/portenv-team/builder/tmux/default ls >/dev/null`}},
		mutate: `keep /tmp/tmux-1000 /tmp/tmux-1000/default
chmod 0755 /tmp/tmux-1000; chmod 0777 /tmp/tmux-1000/default
runuser -u work -- tmux -S /tmp/tmux-1000/default server-access -a builder
echo "runuser -u work -- tmux -S /tmp/tmux-1000/default server-access -d builder" >> "$UNDO"`,
	},
	{
		name: "an agent can't reach another agent's tmux server",
		steps: []step{{"builder", `! tmux -S /run/portenv-team/reviewer/tmux/default ls 2>/dev/null &&
tmux -S /run/portenv-team/builder/tmux/default ls >/dev/null`}},
		mutate: `keep /run/portenv-team/reviewer /run/portenv-team/reviewer/tmux /run/portenv-team/reviewer/tmux/default
chmod 0711 /run/portenv-team/reviewer; chmod 0777 /run/portenv-team/reviewer/tmux /run/portenv-team/reviewer/tmux/default
portenv-test-as reviewer "tmux -S /run/portenv-team/reviewer/tmux/default server-access -a builder"
echo "portenv-test-as reviewer 'tmux -S /run/portenv-team/reviewer/tmux/default server-access -d builder'" >> "$UNDO"`,
	},
	{
		name: "an agent can't read another agent's home",
		steps: []step{{"builder", `! ls /home/reviewer 2>/dev/null && ! cat /home/reviewer/secret 2>/dev/null &&
cat /home/builder/secret >/dev/null`}},
		mutate: `keep /home/reviewer /home/reviewer/secret
chmod 0755 /home/reviewer; chmod 0644 /home/reviewer/secret`,
	},
	{
		name:  "an agent can't list the owner's home (0711, R-0261)",
		steps: []step{{"builder", `! ls /home/work 2>/dev/null && test -d /home/work/proj/src`}},
		mutate: `keep /home/work
chmod 0755 /home/work`,
	},
	{
		name: "an agent can't enter the owner's private folders (.ssh, .config, .claude, .gnupg, .docker)",
		steps: []step{{"builder", `for d in .ssh .config .claude .gnupg .docker; do
  if ls "/home/work/$d" >/dev/null 2>&1; then echo "listed $d"; exit 1; fi
done`}},
		mutate: `keep /home/work/.config
chmod 0755 /home/work/.config`,
	},
	{
		name: "an agent gets EACCES on every one of the owner's known secret paths (R-0262)",
		steps: []step{{"builder", `python3 - <<'PY'
import sys
paths = ["/home/work/" + p for p in (
    ".npmrc", ".netrc", ".git-credentials", ".gitconfig", ".bash_history",
    ".ssh/id_ed25519", ".ssh/id_test", ".config/gh/hosts.yml", ".config/token",
    ".claude/.credentials.json", ".gnupg/pubring.kbx", ".docker/config.json")]
bad = []
for p in paths:
    try:
        open(p, "rb").close()
        bad.append(p + ": opened")
    except PermissionError:
        pass
    except OSError as e:
        bad.append(p + ": " + e.strerror + ", not EACCES")
if bad:
    print("; ".join(bad))
    sys.exit(1)
PY`}},
		mutate: `keep /home/work/.npmrc
chmod 0644 /home/work/.npmrc`,
	},
	{
		name: "the team folder keeps its group: what one agent makes, another can change",
		steps: []step{
			{"builder", `rm -f /home/team/row; echo one > /home/team/row &&
[ "$(stat -c %G /home/team/row)" = team ] && [ "$(stat -c %a /home/team)" = 2770 ]`},
			{"reviewer", `echo two >> /home/team/row`},
		},
		mutate: `keep /home/team
chmod g-s /home/team`,
	},
	{
		name: "an agent can't change the project's hooks, git config or agent files",
		steps: []step{{"builder", `! touch /home/work/proj/.git/hooks/pre-commit 2>/dev/null &&
! sh -c 'echo "[core] hooksPath = /tmp" >> /home/work/proj/.git/config' 2>/dev/null &&
! touch /home/work/proj/.portenv/agents/evil.json 2>/dev/null &&
! sh -c 'echo x >> /home/work/proj/.portenv/agents/builder.json' 2>/dev/null &&
touch /home/work/proj/src/ok-$$ && rm /home/work/proj/src/ok-$$`}},
		mutate: `keep /home/work/proj/.git/hooks
chgrp team /home/work/proj/.git/hooks; chmod 0775 /home/work/proj/.git/hooks
echo "rm -f /home/work/proj/.git/hooks/pre-commit" >> "$UNDO"`,
	},
	{
		name:  "an agent can't reach the workspace helper's socket",
		steps: []step{{"builder", `! connects /run/portenv/agent.sock && ! ls /run/portenv-sftp 2>/dev/null`}},
		mutate: `keep /run/portenv /run/portenv/agent.sock
chmod 0711 /run/portenv; chmod 0666 /run/portenv/agent.sock`,
	},
	{
		name: "an agent can't reach another agent's portenv socket",
		steps: []step{{"builder", `! connects /run/portenv-team/reviewer/portenv.sock &&
connects /run/portenv-team/builder/portenv.sock`}},
		mutate: `keep /run/portenv-team/reviewer /run/portenv-team/reviewer/portenv.sock
chmod 0711 /run/portenv-team/reviewer; chmod 0666 /run/portenv-team/reviewer/portenv.sock`,
	},
	{
		name: "an agent can't reach any of the owner's sockets (SSH agent, gpg agent, VS Code, /tmp)",
		steps: []step{{"builder", `for s in $(find / \( -path /proc -o -path /sys \) -prune -o -type s -print 2>/dev/null); do
  [ "$(stat -c %U "$s" 2>/dev/null)" = work ] || continue
  if connects "$s"; then echo "reached $s"; exit 1; fi
done
exit 0`}},
		mutate: `s=$(cat /run/portenv-test/ssh-socket); d=$(dirname "$s")
keep "$d" "$s"
chmod 0755 "$d"; chmod 0777 "$s"`,
	},
	{
		name: "an agent's TMPDIR is its own, 0700, and no other agent can enter it",
		steps: []step{
			{"builder", `[ "$TMPDIR" = /run/portenv-team/builder/tmp ] &&
[ "$(stat -c '%U %a' "$TMPDIR")" = "builder 700" ] && touch "$TMPDIR/mine"`},
			{"reviewer", `! ls /run/portenv-team/builder/tmp 2>/dev/null && ! touch /run/portenv-team/builder/tmp/x 2>/dev/null`},
		},
		mutate: `keep /run/portenv-team/builder /run/portenv-team/builder/tmp
chmod 0711 /run/portenv-team/builder; chmod 0777 /run/portenv-team/builder/tmp
echo "rm -f /run/portenv-team/builder/tmp/x" >> "$UNDO"`,
	},
	{
		name: "an agent gains nothing from sudo or set-uid programs",
		steps: []step{{"builder", `! sudo -n true 2>/dev/null &&
grep -Eq '^NoNewPrivs:[[:space:]]+1$' /proc/self/status &&
grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status &&
! su -c true root </dev/null >/dev/null 2>&1`}},
		mutate: `echo 'builder ALL=(ALL:ALL) NOPASSWD: ALL' > /etc/sudoers.d/zz-portenv-test; chmod 0440 /etc/sudoers.d/zz-portenv-test
touch /run/portenv-test/no-nnp
echo "rm -f /etc/sudoers.d/zz-portenv-test /run/portenv-test/no-nnp" >> "$UNDO"`,
	},
	{
		name: "an agent's tmux server and its panes have no capabilities and no_new_privs (CC-0409)",
		steps: []step{{"builder", `sock=/run/portenv-team/builder/tmux/default
pids="$(tmux -S $sock display -p '#{pid}') $(tmux -S $sock list-panes -a -F '#{pane_pid}')"
[ -n "$pids" ] || exit 1
for pid in $pids; do
  st=/proc/$pid/status
  for c in CapPrm CapEff CapAmb; do
    grep -Eq "^$c:[[:space:]]+0+$" $st || { echo "$pid: $(grep "^$c:" $st)"; exit 1; }
  done
  grep -Eq '^NoNewPrivs:[[:space:]]+1$' $st || { echo "$pid: no no_new_privs"; exit 1; }
done`}},
		// The agent's server, started again without no_new_privs.
		mutate: `restart='portenv-test-as builder "tmux -S /run/portenv-team/builder/tmux/default kill-server"; portenv-test-as builder "tmux -S /run/portenv-team/builder/tmux/default new-session -d -s main sleep\ 1000000"'
touch /run/portenv-test/no-nnp
sh -c "$restart"
printf '%s\n' "rm -f /run/portenv-test/no-nnp" "$restart" >> "$UNDO"`,
	},
	{
		name: "an agent can't install a system package without the owner's decision",
		steps: []step{{"builder", `! apt-get install -y -q --no-download hello >/dev/null 2>&1 &&
! dpkg --configure -a >/dev/null 2>&1 &&
! touch /etc/apt/sources.list.d/portenv-test.list 2>/dev/null &&
! touch /var/lib/dpkg/portenv-test 2>/dev/null &&
! touch /usr/local/bin/portenv-test 2>/dev/null`}},
		mutate: `keep /etc/apt/sources.list.d
chmod 0777 /etc/apt/sources.list.d
echo "rm -f /etc/apt/sources.list.d/portenv-test.list" >> "$UNDO"`,
	},
	{
		name: "an agent can't read the helper's, restic's or the owner's process memory or environment (ADR 0005)",
		steps: []step{{"builder", `for pid in $(cat /run/portenv-test/memory-targets); do
  if grep -q . /proc/$pid/environ 2>/dev/null; then echo "read $pid's environment"; exit 1; fi
  if python3 -c 'import sys; f = open("/proc/%s/mem" % sys.argv[1], "rb", 0)' $pid 2>/dev/null; then echo "opened $pid's memory"; exit 1; fi
done
[ -s /run/portenv-test/memory-targets ]`}},
		// The probe must see a process it may read: one of builder's own.
		mutate: `cp /run/portenv-test/memory-targets /run/portenv-test/memory-targets.orig
portenv-test-as builder 'PORTENV_TEST_SECRET=mine nohup sleep 1000002 >/dev/null 2>&1 &'
sleep 1
pgrep -u builder -f 'sleep 1000002' >> /run/portenv-test/memory-targets
echo "pkill -u builder -f 'sleep 1000002'; mv /run/portenv-test/memory-targets.orig /run/portenv-test/memory-targets" >> "$UNDO"`,
	},
	{
		name:     "an agent can't read the owner's command lines (/proc)",
		residual: "/proc isn't mounted with hidepid in the box, so other uids' command lines are readable: a stated residual risk (R-0245 item 2); agents are told never to pass secrets on the command line",
		steps: []step{{"builder", `pid=$(pgrep -u work -f 'sleep 1000001' | head -1)
! grep -q 1000001 /proc/$pid/cmdline 2>/dev/null`}},
	},
	{
		name:    "portenv whoami reports what the grant check enforces",
		pending: "lane C's portenv whoami (R-0246)",
	},
	{
		name:    "a new world-readable file at the top of the owner's home is made private again at the next team start (R-0262)",
		pending: "lane C's PrepareTeamProject in the image (real mode)",
	},
	{
		name:    "an agent can only set its own status (R-0256)",
		pending: "lane C's portenv status set and SetStatus",
	},
	{
		name:    "an agent without the team_status grant gets no team status (R-0257)",
		pending: "lane C's portenv team status",
	},
	{
		name:    "a hand-off lands only in the recipient's ~/inbox/<sender>/<timestamp>/, owned by the recipient, regular files only (R-0257)",
		pending: "lane C's portenv handoff",
	},
	{
		name:    "a routine is delivered only to the agent's own main pane, never while it's busy, and collapses after misses (R-0258)",
		pending: "lane C's routines and ADR 0017's waiting detector",
	},
	{
		name:    "an agent's routine propose changes nothing until the owner approves it (R-0258)",
		pending: "lane C's portenv routine propose",
	},
}

// TestTheAgentBoundary runs every row as an agent, then with its mutation
// (it must fail), then undone (it must pass again).
func TestTheAgentBoundary(t *testing.T) {
	image := os.Getenv("PORTENV_TEST_IMAGE")
	if image == "" {
		t.Skip("PORTENV_TEST_IMAGE not set; run make team-boundary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	b := newBox(ctx, t, image)
	switch mode := os.Getenv("PORTENV_TEAM_SETUP"); mode {
	case "", "fixture":
		setupFixture(ctx, t, b)
	case "real":
		setupReal(ctx, t, b)
	default:
		t.Fatalf("PORTENV_TEAM_SETUP=%q: fixture or real", mode)
	}
	if out, code, err := b.root(ctx, `{ pgrep -x -f 'portenv-agent serve'; pgrep -u work -f 'sleep 1000001'; } > /run/portenv-test/memory-targets; wc -l < /run/portenv-test/memory-targets`); err != nil || code != 0 || strings.TrimSpace(out) != "2" {
		t.Fatalf("memory targets: %v %d %q (want the helper's API process and the owner's process)", err, code, out)
	}
	for i, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if r.pending != "" {
				t.Skipf("waiting for %s", r.pending)
			}
			held, why := b.run(ctx, t, r)
			if r.residual != "" {
				if held {
					t.Logf("held here: the residual risk doesn't apply on this engine")
				} else {
					t.Skipf("residual risk: %s (%s)", r.residual, why)
				}
				return
			}
			if !held {
				t.Fatalf("the boundary didn't hold: %s", why)
			}
			if r.mutate == "" {
				t.Fatal("a gating row needs a mutation")
			}
			undo := fmt.Sprintf("/run/portenv-test/undo-%d.sh", i)
			if out, code, err := b.root(ctx, "UNDO="+undo+"; : > \"$UNDO\"\n"+keep+r.mutate); err != nil || code != 0 {
				t.Fatalf("mutation: %v %d %s", err, code, out)
			}
			if held, _ := b.run(ctx, t, r); held {
				t.Errorf("the row still passed with its protection removed: it doesn't test what it says")
			}
			if out, code, err := b.root(ctx, "sh "+undo); err != nil || code != 0 {
				t.Fatalf("undo: %v %d %s", err, code, out)
			}
			if held, why := b.run(ctx, t, r); !held {
				t.Fatalf("undone, the boundary didn't hold again: %s", why)
			}
		})
	}
}

// run runs a row's steps, each as its agent; held is whether all passed.
func (b *box) run(ctx context.Context, t *testing.T, r row) (held bool, why string) {
	t.Helper()
	for _, s := range r.steps {
		out, code, err := b.exec(ctx, "root", []string{"portenv-test-as", s.agent, prelude + s.script})
		if err != nil {
			t.Fatalf("%s: %v", s.agent, err)
		}
		if code != 0 {
			return false, fmt.Sprintf("as %s: exit %d %s", s.agent, code, out)
		}
	}
	return true, ""
}
