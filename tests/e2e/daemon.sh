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
	[[ -n ${dpid:-} ]] && kill "$dpid" 2>/dev/null && wait "$dpid" 2>/dev/null
	[[ -n ${epid:-} ]] && kill "$epid" 2>/dev/null
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-daemon-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-daemon-$id" "portenv-cache-daemon-$id" >/dev/null 2>&1 || true
	fi
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
expect "portenvd serves its socket, mode 0600" bash -c "[[ \$(stat -f %Lp '$root/mac/portenvd.sock' 2>/dev/null || stat -c %a '$root/mac/portenvd.sock') == 600 ]]"

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

if (( failures )); then echo "$failures check(s) failed"; sed 's/^/  portenvd: /' "$root/portenvd.log" | tail -20; exit 1; fi
echo "all checks passed"
