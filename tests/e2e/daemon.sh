#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# portenvd end to end, as the app uses it (milestone 1.0): open a box,
# type into its tmux session through the agent channel, make a save point,
# change a file, revert, close, and reopen. Nothing here uses docker exec.
#
#   tests/e2e/daemon.sh [IMAGE]
set -euo pipefail
image=${1:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/portenv-d.XXXXXX")
export PORTENV_KEYS=file PORTENV_HOME=$root/mac PORTENV_DOCKER_NAMESPACE=daemon
failures=0
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/d.json"; }
# type sends keystrokes to the box's tmux session and returns what the
# terminal showed; the session lives on after the client detaches.
type_in() { { printf '%s\r' "$1"; sleep "${2:-2}"; } | "$portenv" attach d 2>/dev/null | tr -d '\r' || true; }
cleanup() {
	# Nothing in the cleanup may fail: under set -e that would fail the run.
	if [[ -n ${dpid:-} ]]; then kill "$dpid" 2>/dev/null || true; wait "$dpid" 2>/dev/null || true; fi
	if [[ -n ${epid:-} ]]; then kill "$epid" 2>/dev/null || true; fi
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-daemon-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-daemon-$id" "portenv-cache-daemon-$id" >/dev/null 2>&1 || true
	fi
	# restic in the box wrote the repository as portenv-sync (uid 990); on
	# Linux hosts those files really are uid 990, so remove them as root in
	# a container.
	docker run --rm -v "$root:/scratch" --entrypoint rm "$image" -rf /scratch/mac/Repositories >/dev/null 2>&1 || true
	rm -rf "$root" || true
}
trap cleanup EXIT

mkdir -p "$root/mac"
# Every docker exec into a container shows up as an exec_create event.
docker events --filter type=container --filter event=exec_create --format '{{.Actor.Attributes.name}} {{.Action}}' > "$root/execs.txt" &
epid=$!
"$repo/bin/portenvd" 2>"$root/portenvd.log" &
dpid=$!
for _ in $(seq 50); do [[ -S $root/mac/portenvd.sock ]] && break; sleep 0.1; done
mode() { if [[ $(uname) == Darwin ]]; then stat -f %Lp "$1"; else stat -c %a "$1"; fi; }
expect "portenvd serves its socket, mode 0600 ($(mode "$root/mac/portenvd.sock"))" test "$(mode "$root/mac/portenvd.sock")" = 600

"$portenv" init d --image "$image" >/dev/null
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "open: a new box (rule 1)" grep -q "rule 1" <<<"$out"

out=$(type_in 'echo who=$(id -un) tmux=${TMUX%%,*}')
expect "attach lands in work's tmux session" grep -q "who=work tmux=/tmp/tmux-1000" <<<"$out"
type_in 'echo one > ~/f.txt' 1 >/dev/null
out=$("$portenv" app point d 2>&1) || true; echo "  $out"
expect "save point" grep -q "^save point " <<<"$out"
type_in 'echo two > ~/f.txt; echo added > ~/g.txt' 1 >/dev/null
out=$("$portenv" app revert d 2>&1) || true; echo "  $out"
expect "revert to the last save point" grep -q "^reverted to save point" <<<"$out"
out=$(type_in 'echo f=$(cat ~/f.txt) g=$(cat ~/g.txt 2>/dev/null || echo none)')
expect "the home is back at the save point" grep -q "f=one g=none" <<<"$out"

out=$("$portenv" app close d 2>&1) || true; echo "  $out"
expect "close saves and releases" grep -q "closed and released" <<<"$out"
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "reopen on the same machine (rule 3)" grep -q "rule 3" <<<"$out"
out=$(type_in 'echo f=$(cat ~/f.txt)')
expect "the reverted file is still there after reopening" grep -q "f=one" <<<"$out"
"$portenv" app close d >/dev/null 2>&1 || true
sleep 1; kill "$epid" 2>/dev/null; epid=""
# The image's HEALTHCHECK is the engine's own exec of "portenv-agent ready";
# anything else would be an access path.
grep "portenv-daemon-" "$root/execs.txt" | sed 's/^[^ ]* //' | sort | uniq -c | sed 's/^/  exec events: /' || true
others=$(grep "portenv-daemon-" "$root/execs.txt" | grep -vc 'exec_create: /usr/local/bin/portenv-agent ready$' || true)
expect "no docker exec into the box but the engine's health check ($others others)" test "$others" = 0

# The box agent dies mid-session. Processes in the box keep editing files;
# nothing can be saved until the box restarts. Restart Box keeps the local
# home (it has unsaved changes, so nothing is restored over it: rule 3),
# and the next save includes the edits made while the agent was gone.
# docker exec here only simulates the failure and the box's own processes.
"$portenv" app open d >/dev/null 2>&1 || true
c=portenv-daemon-$(box_id)
docker exec "$c" pkill -f '^portenv-agent serve' || true
sleep 1
out=$("$portenv" app check d 2>&1) || true; echo "  $out"
expect "check: the box agent is unavailable" grep -q "the box agent is unavailable" <<<"$out"
docker exec -u work "$c" sh -c 'echo while-down > /home/work/down.txt'
out=$("$portenv" app restart d 2>&1) || true; echo "  $out"
expect "Restart Box keeps the local home (rule 3)" grep -q "rule 3" <<<"$out"
out=$(type_in 'echo down=$(cat ~/down.txt)')
expect "the edit made while the agent was gone is still there" grep -q "down=while-down" <<<"$out"
out=$("$portenv" app point d 2>&1) || true; echo "  $out"
expect "the next save goes through" grep -q "^save point " <<<"$out"
"$portenv" app close d >/dev/null 2>&1 || true
# Prove it is in the save: drop this machine's copy of the box entirely.
docker rm -f "$c" >/dev/null 2>&1 || true
docker volume rm -f "portenv-home-daemon-$(box_id)" >/dev/null 2>&1 || true
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "reopen with no local home restores from storage (rule 5)" grep -q "rule 5" <<<"$out"
out=$(type_in 'echo down=$(cat ~/down.txt)')
expect "the save includes the edit made while the agent was gone" grep -q "down=while-down" <<<"$out"
"$portenv" app close d >/dev/null 2>&1 || true

# An impostor on the agent's port (ADR 0010, conditions): root in the box
# kills the agent and serves a self-made certificate there. portenvd
# refuses it and reports the box agent as unavailable; nothing is saved.
"$portenv" app open d >/dev/null 2>&1 || true
c=portenv-daemon-$(box_id)
docker exec "$c" sh -c "pkill -f '^portenv-agent serve'; sleep 1; openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=portenv-agent -days 1 -keyout /tmp/i.key -out /tmp/i.crt 2>/dev/null; setsid sh -c 'openssl s_server -accept 7700 -cert /tmp/i.crt -key /tmp/i.key -quiet >/tmp/impostor.log 2>&1' >/dev/null 2>&1 & sleep 1"
out=$("$portenv" app point d 2>&1) || true; echo "  $out"
expect "an impostor on the agent's port: portenvd reports the box agent unavailable" grep -q "the box agent is unavailable" <<<"$out"

if (( failures )); then echo "$failures check(s) failed"; sed 's/^/  portenvd: /' "$root/portenvd.log" | tail -20; exit 1; fi
echo "all checks passed"
