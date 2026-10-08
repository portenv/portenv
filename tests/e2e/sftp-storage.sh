#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# SFTP storage end to end, locally: an Ubuntu container stands in for a
# server (sshd plus the storage account from server/setup.sh), and a box on
# this machine saves to it and resumes. Covers the storage account when uid
# and gid 990 are already taken, the authorized-keys file being readable by
# sshd, the agent serving the key from memory and pinning the host key, and
# restic's cache surviving restarts, and restic on this machine reaching the
# repository through the REST server over the storage account's SSH.
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
	[[ -n ${dpid:-} ]] && kill "$dpid" 2>/dev/null
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
docker run -d --name "$srv" -p "$bind:2222:22" ubuntu:24.04 sleep infinity >/dev/null
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

out=$("$portenv" resume sftp </dev/null 2>&1) || { echo "$out" | sed 's/^/  /'; }; out=$(tail -1 <<<"$out"); echo "  $out"
expect "rule 1 with SFTP storage" grep -q "rule 1" <<<"$out"
in_box 'echo "over sftp" > note.txt'
expect "save over SFTP" "$portenv" save sftp </dev/null
expect "no key or helper left in the box after the run" bash -c "[[ -z \$(docker exec portenv-sftp-$(box_id) sh -c 'ls -A /var/cache/portenv-sync | grep sftp-') ]]"
expect "listing and lease tags go through the REST server" docker exec "$srv" grep -q "GET /$(box_id)/snapshots" /tmp/rest.log
expect "the repository is on the server, owned by portenv-storage" docker exec "$srv" bash -c "test -f /srv/portenv/storage-root/storage/boxes/$(box_id)/config && [[ \$(stat -c %U /srv/portenv/storage-root/storage/boxes/$(box_id)/config) == portenv-storage ]]"
expect "close" "$portenv" close sftp </dev/null
out=$("$portenv" resume sftp </dev/null 2>&1) || { echo "$out" | sed 's/^/  /'; }; out=$(tail -1 <<<"$out"); echo "  $out"
expect "same-machine resume over SFTP (rule 3)" grep -q "rule 3" <<<"$out"
expect "the file is there" in_box 'grep -qx "over sftp" note.txt'
expect "restic's cache survived the restart" bash -c "[[ -n \$(docker exec portenv-sftp-$(box_id) sh -c 'ls /var/cache/portenv-sync') ]]"
docker exec "$srv" bash -c "sed -i 's/^restrict/#restrict/' /etc/ssh/portenv-storage.authorized_keys"
expect "an unknown key is refused (no save without the server's consent)" bash -c "! $portenv save sftp </dev/null"
expect "close after restoring the key" bash -c "docker exec $srv sed -i 's/^#restrict/restrict/' /etc/ssh/portenv-storage.authorized_keys && $portenv close sftp </dev/null"

echo "== the same storage through portenvd and the agent channel"
# The CLI above runs restic with docker exec as root in the box; portenvd
# runs it through the agent channel's API process (portenv-agent, uid 991,
# few capabilities). Saving over SFTP there is a different path and must be
# tested on its own: it once failed while everything above passed.
"$repo/bin/portenvd" 2>"$root/portenvd.log" &
dpid=$!
for _ in $(seq 50); do [[ -S $root/mac/portenvd.sock ]] && break; sleep 0.1; done
out=$("$portenv" app open sftp 2>&1) || true; echo "  $out"
expect "portenvd opens the box (rule 3)" grep -q "rule 3" <<<"$out"
{ printf 'echo through-the-channel > ~/channel.txt\r'; sleep 2; } | "$portenv" attach sftp >/dev/null 2>&1 || true
out=$("$portenv" app point sftp 2>&1) || true; echo "  $out"
expect "a save over SFTP through the agent channel" grep -q "^save point" <<<"$out"
# The REST forward's SSH connection drops (network change, idle limit): the
# next save rebuilds it.
pkill -f "ssh .*-M -N -f .*ControlPersist=4h" || true
sleep 1
out=$("$portenv" app point sftp 2>&1) || true; echo "  $out"
expect "a save after the REST forward's connection dropped" grep -q "^save point" <<<"$out"
out=$("$portenv" app close sftp 2>&1) || true; echo "  $out"
expect "close over SFTP through the agent channel" grep -q "closed and released" <<<"$out"
kill "$dpid" 2>/dev/null; wait "$dpid" 2>/dev/null || true

echo "== offline: the storage server is down"
docker stop -t 1 "$srv" >/dev/null
t0=$(python3 -c 'import time; print(time.time())')
out=$("$portenv" resume sftp </dev/null 2>&1) || true
t1=$(python3 -c 'import time; print(time.time())')
echo "  $(tail -1 <<<"$out")"
offline_s=$(python3 -c "print(round($t1 - $t0, 1))")
expect "resume after a clean close starts offline" grep -q "Offline · will save later" <<<"$out"
expect "offline resume takes under 5 s (${offline_s} s)" python3 -c "import sys; sys.exit(0 if $t1 - $t0 < 5 else 1)"
in_box 'echo "written offline" > offline.txt'
out=$("$portenv" save sftp </dev/null 2>&1) || true
expect "a save while offline waits, and says so" grep -q "will save later" <<<"$out"
expect "the offline work is still in the box" in_box 'grep -qx "written offline" offline.txt'
docker start "$srv" >/dev/null && docker exec "$srv" bash -c 'mkdir -p /run/sshd && /usr/sbin/sshd' && start_rest && sleep 1
expect "back online, the save goes through" "$portenv" save sftp </dev/null
expect "close" "$portenv" close sftp </dev/null

echo
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
