// SPDX-License-Identifier: Apache-2.0

package teamboundary

import (
	"context"
	"testing"
)

// A team for the rows: the owner (work) and two agents, builder (2001) and
// reviewer (2002), laid out as ADR 0022 sections 1 and 2 say, with
// stand-ins for what the rows must not reach: the owner's tmux server and
// sockets, its logins, an agent's portenv socket and a process with a
// secret in its environment.
//
// setupFixture builds that layout as root (PORTENV_TEAM_SETUP=fixture).
// It lets every row and its mutation be proven before lane C's team start
// and lane A's per-agent tmux servers exist. setupReal (=real) asks the
// workspace helper to start the team instead, and is what gates the
// preview build: until it exists, real mode fails, never passes.
const fixture = `set -eu
groupadd -g 2000 team
usermod -aG team work
install -d -o root -g root -m 0755 /run/portenv-team /run/portenv-test
for spec in builder:2001 reviewer:2002; do
  id=${spec%:*}; uid=${spec#*:}
  groupadd -g "$uid" "$id"
  useradd -u "$uid" -g "$uid" -G team -d "/home/$id" -M -s /bin/bash "$id"
  passwd -l "$id" >/dev/null
  install -d -o "$id" -g "$id" -m 0700 "/home/$id"
  echo "$id-secret" > "/home/$id/secret"; chown "$id:$id" "/home/$id/secret"; chmod 0600 "/home/$id/secret"
  install -d -o 991 -g "$id" -m 0710 "/run/portenv-team/$id"
  install -d -o "$id" -g "$id" -m 0700 "/run/portenv-team/$id/tmux" "/run/portenv-team/$id/tmp"
  # The agent's portenv socket: the helper's (991), the agent's group, 0660.
  setpriv --reuid=991 --regid=991 --groups="991,$uid" -- python3 -c '
import os, socket, sys, time
p = sys.argv[1]
s = socket.socket(socket.AF_UNIX); s.bind(p); s.listen(8)
os.chown(p, 991, int(sys.argv[2])); os.chmod(p, 0o660)
time.sleep(10**9)' "/run/portenv-team/$id/portenv.sock" "$uid" &
  # Its own tmux server, started as the helper starts it.
  portenv-test-as "$id" "tmux -S /run/portenv-team/$id/tmux/default new-session -d -s main 'sleep 1000000'"
done
# The owner's home: pass-through, logins private.
chmod 0711 /home/work
for d in .ssh .config .claude .gnupg; do install -d -o work -g work -m 0700 "/home/work/$d"; done
echo owner-key > /home/work/.ssh/id_test; echo owner-token > /home/work/.config/token
chown work:work /home/work/.ssh/id_test /home/work/.config/token; chmod 0600 /home/work/.ssh/id_test /home/work/.config/token
# The team folder.
install -d -o work -g team -m 2770 /home/team
# The project: group team, setgid directories, group-writable files. (The
# real team start uses default ACLs; the image has no setfacl, so the
# fixture relies on umask 002, which the agents' login also sets.)
install -d -o work -g team -m 2775 /home/work/proj /home/work/proj/src
runuser -u work -- git -C /home/work/proj init -q
chgrp -R team /home/work/proj
find /home/work/proj -type d -exec chmod g+rws {} +
find /home/work/proj -type f -exec chmod g+rw {} +
# Never group-writable: hooks, config, .portenv.
chown -R work:work /home/work/proj/.git/hooks /home/work/proj/.git/config
chmod 0755 /home/work/proj/.git/hooks; chmod 0644 /home/work/proj/.git/config
install -d -o work -g work -m 0755 /home/work/proj/.portenv /home/work/proj/.portenv/agents
echo '{}' > /home/work/proj/.portenv/agents/builder.json
chown work:work /home/work/proj/.portenv/agents/builder.json; chmod 0644 /home/work/proj/.portenv/agents/builder.json
# The owner's tmux server and sockets: an SSH agent, a gpg agent, VS Code's.
runuser -u work -- tmux new-session -d -s main 'sleep 1000000'
runuser -u work -- sh -c '
umask 077
d=$(mktemp -d /tmp/ssh-XXXXXXXX); echo "$d/agent.sock" > /tmp/portenv-test-ssh
listen() { python3 -c "
import socket, sys, time
s = socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(8); time.sleep(10**9)" "$1" & }
listen "$d/agent.sock"
listen /home/work/.gnupg/S.gpg-agent
listen /tmp/vscode-ipc-portenv-test.sock
PORTENV_TEST_SECRET=owner-env-secret nohup sleep 1000001 >/dev/null 2>&1 &
'
cp /tmp/portenv-test-ssh /run/portenv-test/ssh-socket
sleep 1
`

// asAgent is how the fixture starts an agent's process: what ADR 0022
// section 1 says the helper does (its uid, its group and team, no
// capabilities, no_new_privs), with the agent's login environment.
// /run/portenv-test/no-nnp, written by a mutation, drops no_new_privs.
const asAgent = `#!/bin/sh
set -eu
id=$1; shift
uid=$(id -u "$id"); gid=$(id -g "$id")
nnp=--no-new-privs
[ -e /run/portenv-test/no-nnp ] && nnp=
exec setpriv --reuid="$uid" --regid="$gid" --groups="$gid,2000" $nnp --inh-caps=-all --ambient-caps=-all -- \
  env -i HOME="/home/$id" USER="$id" LOGNAME="$id" PATH=/usr/local/bin:/usr/bin:/bin \
  TMPDIR="/run/portenv-team/$id/tmp" PORTENV_AGENT="$id" \
  sh -c "umask 002; $*"
`

// setupFixture installs the agent launcher and builds the fixture team.
func setupFixture(ctx context.Context, t *testing.T, b *box) {
	t.Helper()
	if out, code, err := b.root(ctx, "cat > /usr/local/sbin/portenv-test-as <<'EOF'\n"+asAgent+"EOF\nchmod 0755 /usr/local/sbin/portenv-test-as"); err != nil || code != 0 {
		t.Fatalf("install the agent launcher: %v %d %s", err, code, out)
	}
	if out, code, err := b.root(ctx, fixture); err != nil || code != 0 {
		t.Fatalf("the fixture team: %v %d %s", err, code, out)
	}
}

// setupReal starts the team through the workspace helper (lane C's team
// start, lane A's per-agent tmux servers). Until they exist, it fails:
// the preview build is gated on real mode, never on the fixture.
func setupReal(_ context.Context, t *testing.T, _ *box) {
	t.Helper()
	t.Fatal("real mode needs lane C's team start (ADR 0022 sections 1 and 2), which isn't in this image yet")
}
