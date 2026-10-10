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
check "/home holds only work" test "$(in_box ls -A /home)" = work
check "home is 0700 and owned by work" test "$(in_box stat -c '%a %U' /home/work)" = "700 work"
check "skeleton apt-packages.txt" in_box test -f /home/work/.portenv/apt-packages.txt
check "skeleton excludes" in_box grep -qx '/home/\*/.cache' /home/work/.portenv/excludes
check "skeleton shell files" in_box test -f /home/work/.bashrc
check "agent socket reaches root and portenv-agent only" test "$(in_box stat -c '%a %U:%G' /run/portenv /run/portenv/agent.sock | tr '\n' ' ')" = "710 root:portenv-agent 660 root:portenv-agent "
check "the agent channel copy carries only setuid, setgid, chown and kill" test "$(in_box getcap /usr/local/libexec/portenv/agent-serve | awk '{print $2}')" = "cap_chown,cap_kill,cap_setgid,cap_setuid=ep"
check "work has passwordless sudo" as_work 'sudo -n true'
check "node 22" as_work 'node --version | grep -q "^v22\."'
check "npm installs globals into the home" as_work 'test "$(npm config get prefix)" = /home/work/.local'
check "~/.local/bin on PATH" as_work 'case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) false ;; esac'
check "psql" as_work 'psql --version'
check "git" as_work 'git --version'
check "tmux" as_work 'tmux -V'
check "ripgrep" as_work 'rg --version'
check "jq" as_work 'jq --version'
check "build-essential" as_work 'gcc --version && make --version'
check "python 3" as_work 'python3 --version'
check "shell integration (OSC 133) in login shells" as_work 'bash -lic "declare -F __portenv_osc133_precmd"'
check "skeleton .zshrc sources the zsh integration" in_box grep -q osc133.zsh /home/work/.zshrc
check "claude code" as_work 'claude --version'

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
