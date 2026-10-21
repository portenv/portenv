#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Tests a toolbox-node image on the native architecture:
#
#   images/toolbox-node/test.sh portenv/toolbox-node:dev
#
# Uses docker exec to inspect the box. That is acceptable only in Phase 0 and
# only in tests; Portenv itself reaches boxes through the box agent.
set -euo pipefail

image=${1:?usage: test.sh IMAGE}
id=portenv-test-$$
new=$id-new
missing=$id-missing
failures=0

# The pins the image must carry, read from its Dockerfile.
dockerfile=$(dirname "$0")/Dockerfile
pin() { sed -n "s/^ARG $1=//p" "$dockerfile"; }
claude_pin=$(pin CLAUDE_CODE_VERSION)
chrome_pin=$(pin CHROME_VERSION)
if [[ -z $claude_pin || -z $chrome_pin ]]; then
	echo "no CLAUDE_CODE_VERSION or CHROME_VERSION in $dockerfile" >&2; exit 1
fi

cleanup() {
	docker rm -f "$new" "$missing" >/dev/null 2>&1 || true
	docker volume rm -f "$new" "$missing" >/dev/null 2>&1 || true
}
trap cleanup EXIT

pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
check() { # check NAME COMMAND...
	local name=$1; shift
	if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi
}
in_box() { docker exec "$new" "$@"; }
as_work() { docker exec -u work -w /home/work "$new" bash -lc "$1"; }
# as_agent: a team agent's user, made the way init makes one (core/agent/teamusers.go).
agent=tester
as_agent() { docker exec -u "$agent" -w "/home/$agent" "$new" bash -lc "$1"; }

# wait_health CONTAINER WANT: wait for Docker's health status, up to 10 minutes.
wait_health() {
	local status
	for _ in $(seq 600); do
		status=$(docker inspect -f '{{.State.Health.Status}}' "$1")
		[[ $status == "$2" ]] && return 0
		[[ $status == unhealthy && $2 == healthy ]] && return 1
		sleep 1
	done
	return 1
}

echo "== new box ($image)"
docker run -d --name "$new" -e PORTENV_INIT_HOME=1 -v "$new:/home" "$image" >/dev/null
if wait_health "$new" healthy; then pass "box becomes ready"; else
	fail "box becomes ready"; docker logs "$new" | tail -20; exit 1
fi

check "portenv-agent is PID 1" test "$(in_box ps -p 1 -o comm=)" = portenv-agent
check "work has uid 1000" test "$(in_box id -u work)" = 1000
# A workspace with no agent team: /home/team and agents' homes appear only
# with a team's first agent (ADR 0022 §1), never at a plain start.
check "/home holds only work" test "$(in_box ls -A /home)" = work
check "home is 0700 and owned by work" test "$(in_box stat -c '%a %U' /home/work)" = "700 work"
check "skeleton apt-packages.txt" in_box test -f /home/work/.portenv/apt-packages.txt
check "skeleton excludes" in_box grep -qx '/home/\*/.cache' /home/work/.portenv/excludes
check "skeleton shell files" in_box test -f /home/work/.bashrc
check "agent socket reaches root and portenv-agent only" test "$(in_box stat -c '%a %U:%G' /run/portenv /run/portenv/agent.sock | tr '\n' ' ')" = "710 root:portenv-agent 660 root:portenv-agent "
check "the agent channel copy carries only setuid, setgid, chown and kill" test "$(in_box getcap /usr/local/libexec/portenv/agent-serve | awk '{print $2}')" = "cap_chown,cap_kill,cap_setgid,cap_setuid=ep"
check "work has passwordless sudo" as_work 'sudo -n true'
# R-0262: what work makes at the top of its home is private.
check "work's login shells use umask 077" as_work 'test "$(umask)" = 0077'
check "node 22" as_work 'node --version | grep -q "^v22\."'
check "npm installs globals into the home" as_work 'test "$(npm config get prefix)" = /home/work/.local'
check "~/.local/bin on PATH" as_work 'case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) false ;; esac'
check "psql" as_work 'psql --version'
check "git" as_work 'git --version'
# The helper's control mode was measured on tmux 3.4 and refuses older
# (ADR 0020, decision 10); the Dockerfile pins it.
check "tmux 3.4" as_work 'test "$(tmux -V)" = "tmux 3.4"'
# The switch to an agent's user runs tmux by this path only, and only when
# root owns it and no one else can write to it (R-0265).
check "tmux at /usr/bin/tmux, root's, writable by root only" in_box sh -c 'test "$(stat -L -c %U:%a /usr/bin/tmux)" = root:755 && test "$(stat -c %F /usr/bin/tmux)" = "regular file"'
check "ripgrep" as_work 'rg --version'
check "jq" as_work 'jq --version'
# Agents share files through default ACLs (ADR 0022 §2): the tools, and a
# /home that keeps a default ACL and applies it to new files.
check "setfacl and getfacl" in_box sh -c 'command -v setfacl && command -v getfacl'
check "/home takes default ACLs" in_box sh -c '
	d=$(mktemp -d /home/.acl-check.XXXXXX) || exit 1
	setfacl -d -m g:work:rwX "$d" && touch "$d/f" &&
		getfacl -cp "$d" | grep -qx "default:group:work:rwx" &&
		getfacl -cp "$d/f" | grep -q "^group:work:rw" &&
		getfacl -cp "$d/f" | grep -qx "mask::rw-"
	rc=$?; rm -rf "$d"; exit $rc'
check "build-essential" as_work 'gcc --version && make --version'
check "python 3" as_work 'python3 --version'
check "shell integration (OSC 133) in login shells" as_work 'bash -lic "declare -F __portenv_osc133_precmd"'
check "skeleton .zshrc sources the zsh integration" in_box grep -q osc133.zsh /home/work/.zshrc
check "claude --version is the pin, $claude_pin" as_work "test \"\$(claude --version)\" = '$claude_pin (Claude Code)'"
# Chrome for Testing for browser checks (R-0411), run as work, never root.
check "CHROME_PATH and PUPPETEER_EXECUTABLE_PATH are the browser, root's, 755" as_work \
	'test "$CHROME_PATH" = /opt/chrome/chrome && test "$PUPPETEER_EXECUTABLE_PATH" = "$CHROME_PATH" &&
	test "$(stat -c %U:%a "$CHROME_PATH")" = root:755 && test "$(readlink -f "$(command -v chromium)")" = "$CHROME_PATH"'
check "no setuid sandbox helper in the browser's folder" in_box sh -c 'test -z "$(find /opt/chrome -perm -4000)"'
check "chromium --version is the pin, $chrome_pin" as_work "\"\$CHROME_PATH\" --version | grep -qF ' $chrome_pin'"
# A throwaway profile, so the run leaves nothing in the home. The sandbox
# needs user namespaces, which a container without CAP_SYS_ADMIN lacks:
# --no-sandbox here, and the sandbox's own result is reported, not judged.
headless() { # headless [FLAG...]: render a page as work, print its DOM
	as_work "p=\$(mktemp -d) && timeout 60 \"\$CHROME_PATH\" --headless --disable-gpu --disable-dev-shm-usage \
		--user-data-dir=\"\$p\" $* --dump-dom 'data:text/html,<p>portenv-headless-ok</p>' 2>&1; rc=\$?; rm -rf \"\$p\"; exit \$rc"
}
renders_unsandboxed() { local out; out=$(headless --no-sandbox) && grep -q portenv-headless-ok <<<"$out"; }
check "chromium renders a page headless as work (--no-sandbox)" renders_unsandboxed
if out=$(headless) && grep -q portenv-headless-ok <<<"$out"; then
	printf 'info  chromium sandbox: works without --no-sandbox\n'
else
	printf 'info  chromium sandbox: needs --no-sandbox here (%s)\n' "$(grep -m1 -iE 'sandbox|namespace|error' <<<"$out" || echo 'no message')"
fi
# The portenv CLI, for agents' own commands inside the workspace (ADR 0022 §4).
check "portenv on PATH at /usr/local/bin, root's, 755" in_box sh -c 'test "$(command -v portenv)" = /usr/local/bin/portenv && test "$(stat -c %U:%G:%a /usr/local/bin/portenv)" = root:root:755'
check "portenv whoami in a plain shell exits 2 and says why" as_work \
	'out=$(portenv whoami 2>&1); rc=$?; test "$rc" -eq 2 && grep -q "works only for an agent inside a workspace (PORTENV_SOCKET isn.t set)" <<<"$out"'
check "portenv guide" as_work 'out=$(portenv guide) && test -n "$out"'
check "portenv team init writes the starter agent files" as_work \
	'd=$(mktemp -d) && portenv team init --dir "$d" >/dev/null && test "$(ls "$d/.portenv/agents" | tr "\n" " ")" = "builder.md lead.md reviewer.md " && rm -rf "$d"'
# The owner's commands are refused inside: portenv init would write a box's
# repository key in the home (CC-0453).
check "portenv init is refused inside a workspace" as_work \
	'out=$(portenv init x 2>&1); rc=$?; test "$rc" -eq 2 && grep -qx "This is the owner.s command; run it on your Mac." <<<"$out"'
check "the in-box portenv commands write nothing in the home" as_work 'test ! -e ~/.config/Portenv'
# The same for an agent's user (uid 2001), made as init makes one: the team
# group, the agent's own group, then the user and its 0700 home. Removed
# after, so the rest of the run sees a box with no team.
check "make an agent's user, uid 2001" in_box sh -c "groupadd --gid 2000 team && groupadd --gid 2001 $agent &&
	useradd --uid 2001 --gid 2001 --groups team --no-create-home --home-dir /home/$agent --shell /bin/bash $agent &&
	install -d -m 0700 -o 2001 -g 2001 /home/$agent && test \"\$(id -u $agent)\" = 2001"
check "portenv init is refused for an agent's user" as_agent \
	'out=$(portenv init x 2>&1); rc=$?; test "$rc" -eq 2 && grep -qx "This is the owner.s command; run it on your Mac." <<<"$out"'
check "nothing written in the agent's home" as_agent 'test "$HOME" = "/home/$(id -un)" && test "$(id -u)" = 2001 && test ! -e ~/.config/Portenv'
check "remove the agent's user" in_box sh -c "userdel $agent && { ! getent group $agent >/dev/null || groupdel $agent; } &&
	groupdel team && rm -rf /home/$agent && test \"\$(ls -A /home)\" = work"

# An orphan that exits must be reaped by PID 1, not left as a zombie.
in_box bash -c '(sleep 1 &) ; exit 0'
sleep 3
check "orphans are reaped" test "$(in_box ps -eo stat= | grep -c '^Z' || true)" = 0

echo "== package replay"
as_work 'echo cowsay >> ~/.portenv/apt-packages.txt'
docker restart "$new" >/dev/null
if wait_health "$new" healthy; then pass "box becomes ready after restart"; else fail "box becomes ready after restart"; fi
check "listed package installed before ready" in_box dpkg -s cowsay
check "home survived restart" in_box grep -qx cowsay /home/work/.portenv/apt-packages.txt

start=$(date +%s)
docker stop "$new" >/dev/null
elapsed=$(( $(date +%s) - start ))
check "stops cleanly within 9 s (took ${elapsed}s)" test "$elapsed" -le 9
check "exit code 0 on stop" test "$(docker inspect -f '{{.State.ExitCode}}' "$new")" = 0

echo "== box without home storage"
docker run -d --name "$missing" -v "$missing:/home" "$image" >/dev/null
sleep 5
check "missing home is reported, not replaced" bash -c \
	"docker exec '$missing' portenv-agent ready | grep -q 'home storage is not attached'"
check "ready fails for a failed box" bash -c "! docker exec '$missing' portenv-agent ready"
check "no home was created" test -z "$(docker exec "$missing" ls -A /home)"

echo
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
