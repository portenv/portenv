#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# SFTP storage end to end, locally: an Ubuntu container stands in for a
# server (sshd plus the storage account from server/setup.sh), and a box on
# this machine saves to it and opens again. Covers the storage account when
# uid and gid 990 are already taken, the authorized-keys file being readable
# by sshd, the agent serving the key from memory and pinning the host key,
# restic's cache surviving restarts, restic on this machine reaching the
# repository through the REST server over the storage account's SSH, and
# opening offline. The box is driven only through portenvd and the agent
# channel, as the app drives it; docker exec only inspects.
#
#   tests/e2e/sftp-storage.sh [IMAGE]
set -euo pipefail
image=${1:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/portenv-sftp.XXXXXX")
srv=portenv-sftp-$$
export PORTENV_KEYS=file PORTENV_HOME=$root/mac PORTENV_DOCKER_NAMESPACE=sftp
failures=0
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/sftp.json"; }
in_box() { docker exec -u work -w /home/work "portenv-sftp-$(box_id)" bash -lc "$*"; }
cleanup() {
	# Nothing in the cleanup may fail: under set -e that would fail the run.
	if [[ -n ${dpid:-} ]]; then kill "$dpid" 2>/dev/null || true; wait "$dpid" 2>/dev/null || true; fi
	if (( failures )) && [[ -f $root/portenvd.log ]]; then echo "== portenvd log"; sed 's/^/  /' "$root/portenvd.log" || true; fi
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-sftp-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-sftp-$id" "portenv-cache-sftp-$id" >/dev/null 2>&1 || true
	fi
	docker rm -f "$srv" >/dev/null 2>&1 || true
	rm -rf "$root" || true
}
trap cleanup EXIT
# The stand-in server has no systemd: start the REST server by hand.
start_rest() { docker exec -d "$srv" runuser -u portenv-storage -- /usr/local/bin/rest-server --path /srv/portenv/storage-root/storage/boxes --listen 127.0.0.1:7422 --htpasswd-file /etc/portenv-rest.htpasswd --log /tmp/rest.log; }

echo "== stand-in server: sshd, with uid and gid 990 already taken"
# Its SSH port is published on this machine: boxes reach it through
# host.portenv.internal, the host itself (listing, lease tags) through the
# loopback address.
bind=127.0.0.1; [[ $(uname) == Linux ]] && bind=0.0.0.0
docker run -d --name "$srv" -p "$bind:2222:22" mirror.gcr.io/library/ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55 sleep infinity >/dev/null
docker exec "$srv" bash -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-server curl ca-certificates iproute2 >/dev/null && groupadd -g 990 taken && useradd -u 990 -g 990 -M taken && mkdir -p /run/sshd && /usr/sbin/sshd'
docker cp "$repo/server/setup.sh" "$srv:/tmp/setup.sh" >/dev/null
host_key=$(docker exec "$srv" cut -d' ' -f1,2 /etc/ssh/ssh_host_ed25519_key.pub)

echo "== box with storage on the stand-in server"
"$portenv" init sftp --image "$image" --storage "sftp://portenv-storage@host.portenv.internal:2222//storage" --storage-host-key "$host_key" --storage-rest 127.0.0.1:7422 > "$root/init.txt"
pub=$(grep '^ssh-ed25519 ' "$root/init.txt")
rest_user=$(sed -n 's/^rest-user //p' "$root/init.txt")
docker exec "$srv" bash /tmp/setup.sh --only storage-account --storage-key "$pub" --rest-user "$rest_user" | sed 's/^/  /'
docker exec "$srv" bash /tmp/setup.sh --only rest-server | grep -v '^no systemd' | sed 's/^/  /'
start_rest
expect "the REST server listens on the loopback address only" docker exec "$srv" bash -c 'sleep 1; ss -Htln | grep -q "127.0.0.1:7422" && ! ss -Htln | grep -qE "(0\.0\.0\.0|\*|\[::\]):7422"'
expect "only the bcrypt hash of the REST password is on the server" docker exec "$srv" grep -qF ':$2a$' /etc/portenv-rest.htpasswd
expect "the storage account exists without uid 990" bash -c "[[ \$(docker exec $srv id -u portenv-storage) != 990 ]]"
expect "sshd can read the authorized keys as portenv-storage" docker exec "$srv" runuser -u portenv-storage -- test -r /etc/ssh/portenv-storage.authorized_keys

echo "== the box, through portenvd and the agent channel (as the app does)"
# Everything the app relies on goes through portenvd, which reaches the box
# only through the agent channel (portenv-agent, uid 991, few capabilities);
# docker exec below only inspects. Files are written in the box's terminal.
# Short restic deadlines (test only), so a run against a server that stopped
# answering ends in seconds; real deadlines are minutes (ADR 0012).
PORTENV_TEST_RESTIC_DEADLINES=20s "$repo/bin/portenvd" 2>"$root/portenvd.log" &
dpid=$!
for _ in $(seq 50); do [[ -S $root/mac/portenvd.sock ]] && break; sleep 0.1; done
# Each call has a time limit: a hang fails its check instead of the run.
# (SIGTERM, then SIGKILL: a Go program doesn't exit on SIGALRM.) The
# watcher's output goes nowhere, so $(...) never waits for it.
limit() {
	"$@" &
	local p=$! st=0
	( sleep 180; kill -TERM "$p" 2>/dev/null; sleep 5; kill -KILL "$p" 2>/dev/null ) >/dev/null 2>&1 &
	local w=$!
	wait "$p" || st=$?
	kill "$w" 2>/dev/null || true
	wait "$w" 2>/dev/null || true
	return "$st"
}
app() { limit "$portenv" app "$@" 2>&1; }
state() { app state sftp | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])'; }
type_in() { { printf '%s\r' "$1"; sleep 2; } | "$portenv" attach sftp >/dev/null 2>&1 || true; }
out=$(app open sftp) || true; echo "  $out"
expect "rule 1 with SFTP storage" grep -q "rule 1" <<<"$out"
expect "the state says not saved yet" test "$(state)" = SAVE_STATE_NOT_SAVED_YET
type_in 'echo "over sftp" > ~/note.txt'
out=$(app point sftp) || true; echo "  $out"
expect "a save over SFTP" grep -q "^save point" <<<"$out"
expect "the state says saved" test "$(state)" = SAVE_STATE_SAVED
# The agent's SFTP helpers (key socket, pinned host key) live in
# /run/portenv-sftp for the run only.
expect "no key or helper left in the box after the run" bash -c "docker exec portenv-sftp-$(box_id) test -d /run/portenv-sftp && [[ -z \$(docker exec portenv-sftp-$(box_id) sh -c 'ls -A /run/portenv-sftp; ls -A /var/cache/portenv-sync | grep sftp-') ]]"
expect "listing and lease tags go through the REST server" docker exec "$srv" grep -q "GET /$(box_id)/snapshots" /tmp/rest.log
expect "the repository is on the server, owned by portenv-storage" docker exec "$srv" bash -c "test -f /srv/portenv/storage-root/storage/boxes/$(box_id)/config && [[ \$(stat -c %U /srv/portenv/storage-root/storage/boxes/$(box_id)/config) == portenv-storage ]]"
# The REST forward's SSH connection drops (network change, idle limit): the
# next save rebuilds it.
pkill -f "ssh .*-M -N -f .*ControlPersist=4h" || true
sleep 1
out=$(app point sftp) || true; echo "  $out"
expect "a save after the REST forward's connection dropped" grep -q "^save point" <<<"$out"
out=$(app close sftp) || true; echo "  $out"
expect "close saves and releases" grep -q "closed and released" <<<"$out"
out=$(app open sftp) || true; echo "  $out"
expect "same-machine open over SFTP (rule 3)" grep -q "rule 3" <<<"$out"
expect "the file is there" in_box 'grep -qx "over sftp" note.txt'
expect "restic's cache survived the restart" bash -c "[[ -n \$(docker exec portenv-sftp-$(box_id) sh -c 'ls /var/cache/portenv-sync') ]]"
docker exec "$srv" bash -c "sed -i 's/^restrict/#restrict/' /etc/ssh/portenv-storage.authorized_keys"
st=0; app point sftp >/dev/null || st=$?
expect "an unknown key is refused (no save without the server's consent)" test "$st" -ne 0
docker exec "$srv" sed -i 's/^#restrict/restrict/' /etc/ssh/portenv-storage.authorized_keys
out=$(app close sftp) || true; echo "  $out"
expect "close after restoring the key" grep -q "closed and released" <<<"$out"

echo "== the storage server stops answering mid-session"
# The REST server is frozen (SIGSTOP): connections are accepted and never
# answered, the case that once hung a save for 29 minutes. Each restic run
# has a deadline; a run that misses it is stopped, the lock cleared, and run
# once more; then the save fails and the state line says so.
out=$(app open sftp) || true; echo "  $out"
saved_before=$(app state sftp | python3 -c 'import json,sys; print(json.load(sys.stdin).get("saved_at",""))')
docker exec "$srv" pkill -STOP -f rest-server
seen="$root/states.txt"; : >"$seen"
( for _ in $(seq 120); do state >>"$seen" 2>/dev/null || true; sleep 1; done ) &
watcher=$!
t0=$(python3 -c 'import time; print(time.time())')
out=$(app point sftp) && st=0 || st=$?
t1=$(python3 -c 'import time; print(time.time())')
kill "$watcher" 2>/dev/null || true; wait "$watcher" 2>/dev/null || true
took=$(python3 -c "print(round($t1 - $t0))")
echo "  $out (${took} s)"
expect "a save against a frozen server fails instead of hanging" test "$st" -ne 0
expect "within its deadlines (3 runs of 20 s, plus margins; ${took} s)" python3 -c "import sys; sys.exit(0 if $t1 - $t0 < 100 else 1)"
expect "while it retried, the state said retrying" grep -qx SAVE_STATE_RETRYING "$seen"
expect "then: not saved, since the last save" test "$(state)" = SAVE_STATE_NOT_SAVED
expect "and the time is still the last save's" test "$(app state sftp | python3 -c 'import json,sys; print(json.load(sys.stdin).get("saved_at",""))')" = "$saved_before"
docker exec "$srv" pkill -CONT -f rest-server
out=$(app point sftp) || true; echo "  $out"
expect "the server answers again: the next save goes through" grep -q "^save point" <<<"$out"
expect "and the state says saved" test "$(state)" = SAVE_STATE_SAVED
out=$(app close sftp) || true
expect "close" grep -q "closed and released" <<<"$out"

echo "== offline: the storage server is down"
# portenvd decides between online and offline alongside the box's start:
# with the network up it probes the storage server (at most 1 s); a fresh
# "no network" report from the app (a running process, at most 30 s old)
# skips the probe; a report whose sender has gone counts for nothing.
server_down() { docker stop -t 1 "$srv" >/dev/null; }
server_up() { docker start "$srv" >/dev/null && docker exec "$srv" bash -c 'mkdir -p /run/sshd && /usr/sbin/sshd' && start_rest && sleep 1; }
probes() { grep -c "storage probe" "$root/portenvd.log" || true; }
# Opens the box, prints its stage timings, and sets out, open_s and probed.
timed_open() {
	local before lines t0 t1; before=$(probes); lines=$(wc -l <"$root/portenvd.log")
	t0=$(python3 -c 'import time; print(time.time())')
	out=$(app open sftp) || true
	t1=$(python3 -c 'import time; print(time.time())')
	open_s=$(python3 -c "print(round($t1 - $t0, 2))")
	probed=$(( $(probes) - before ))
	echo "  $out (${open_s} s)"
	tail -n +"$((lines + 1))" "$root/portenvd.log" | grep 'after=' | sed -E 's/.*msg="?([^"]*[^" ])"? .*after=([^ ]*).*/    \2  \1/' || true
}
under() { python3 -c "import sys; sys.exit(0 if $1 < $2 else 1)"; }

server_down
"$portenv" app network up
timed_open
expect "network up, storage down: opens offline" grep -q "Offline · will save later" <<<"$out"
expect "network up, storage down: the storage was probed" test "$probed" -eq 1
expect "network up, storage down: under 5 s (${open_s} s)" under "$open_s" 5
expect "the state says offline" test "$(state)" = SAVE_STATE_OFFLINE
type_in 'echo "written offline" > ~/offline.txt'
out=$(app point sftp) && st=0 || st=$?; echo "  $out"
expect "a save point while offline is refused" test "$st" -ne 0
expect "and the state still says offline" test "$(state)" = SAVE_STATE_OFFLINE
expect "the offline work is still in the box" in_box 'grep -qx "written offline" offline.txt'
server_up
out=$(app point sftp) || true; echo "  $out"
expect "back online, the save goes through" grep -q "^save point" <<<"$out"
expect "the state says saved again" test "$(state)" = SAVE_STATE_SAVED
out=$(app close sftp) || true; echo "  $out"
expect "close" grep -q "closed and released" <<<"$out"

# This script stands in for the app: a report sent from here comes from a
# running process.
server_down
"$portenv" app network down
timed_open
expect "no network (fresh report): opens offline" grep -q "Offline · will save later" <<<"$out"
expect "no network (fresh report): no storage probe" test "$probed" -eq 0
expect "no network (fresh report): under 5 s (${open_s} s)" under "$open_s" 5
server_up
out=$(app close sftp) || true
expect "close once the server is back" grep -q "closed and released" <<<"$out"

# A "down" from a process that has exited (an app that quit) is not trusted.
server_down
bash -c '"$1" app network down; true' _ "$portenv"
timed_open
expect "a down from an app that quit: the storage is probed again" test "$probed" -eq 1
expect "and the box still opens offline" grep -q "Offline · will save later" <<<"$out"
server_up
out=$(app close sftp) || true
expect "close" grep -q "closed and released" <<<"$out"
kill "$dpid" 2>/dev/null || true; wait "$dpid" 2>/dev/null || true
dpid=

echo
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
