#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Move To through portenvd (milestone 1.0), against a server set up by
# server/setup.sh with the toolbox image:
#
#   PORTENV_SSH="ssh -i KEY -o ..." tests/e2e/daemon-server.sh USER@HOST "ssh-ed25519 AAAA..." [IMAGE]
#
# A box with storage on the server: open it on this Mac through portenvd,
# write files through the terminal, Move To the server (it restores them),
# change one there, Move To This Mac (the change comes back). On this Mac
# nothing uses docker exec; the server still runs the Phase 0 CLI.
set -euo pipefail
target=${1:?usage: daemon-server.sh USER@HOST HOST-KEY [IMAGE]}
host_key=${2:?usage: daemon-server.sh USER@HOST HOST-KEY [IMAGE]}
image=${3:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/pds.XXXX")
read -r -a ssh_cmd <<<"${PORTENV_SSH:-ssh}"
export PORTENV_KEYS=file PORTENV_HOME=$root/mac PORTENV_DOCKER_NAMESPACE=dmove
box=move-$(date +%Y%m%d%H%M%S)
failures=0
R() { "${ssh_cmd[@]}" -- "$target" "$@" </dev/null; }
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/$box.json"; }
type_in() { { printf '%s\r' "$1"; sleep "${2:-2}"; } | "$portenv" attach "$box" 2>/dev/null | tr -d '\r' || true; }
cleanup() {
	# Nothing in the cleanup may fail: under set -e that would fail the run.
	if [[ -n ${dpid:-} ]]; then kill "$dpid" 2>/dev/null || true; wait "$dpid" 2>/dev/null || true; fi
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-dmove-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-dmove-$id" "portenv-cache-dmove-$id" >/dev/null 2>&1 || true
	fi
	rm -rf "$root" || true
}
trap cleanup EXIT

mkdir -p "$root/mac"
printf '{"servers": ["%s"]}\n' "$target" > "$root/mac/machine.json"
"$portenv" init "$box" --image "$image" --storage "sftp:portenv-storage@${target#*@}:/storage" --storage-host-key "$host_key" --storage-rest 127.0.0.1:7422 > "$root/init.txt"
pub=$(grep '^ssh-ed25519 ' "$root/init.txt")
rest_user=$(sed -n 's/^rest-user //p' "$root/init.txt")
"${ssh_cmd[@]}" -- "$target" 'cat > /tmp/portenv-setup.sh' < "$repo/server/setup.sh"
R sudo bash /tmp/portenv-setup.sh --only storage-account --storage-key "'$pub'" --rest-user "'$rest_user'" >/dev/null

"$repo/bin/portenvd" 2>"$root/portenvd.log" &
dpid=$!
for _ in $(seq 50); do [[ -S $root/mac/portenvd.sock ]] && break; sleep 0.1; done

out=$("$portenv" app open "$box" 2>&1) || true; echo "  $out"
expect "open on this Mac through portenvd (rule 1)" grep -q "rule 1" <<<"$out"
type_in 'echo from-the-mac > ~/note.txt; head -c 5000000 /dev/urandom > ~/data.bin; sha256sum ~/data.bin | cut -c1-16 > ~/data.sum' 3 >/dev/null
sum=""
for _ in 1 2 3; do
	sum=$(type_in 'echo sum=$(cat ~/data.sum)' 3 | grep -oE 'sum=[0-9a-f]{16}' | tail -1 || true)
	[[ -n $sum ]] && break
done
echo "  on this Mac: ${sum:-(none read)}"
[[ -n $sum ]] || { fail "read the 5 MB file's checksum through the terminal"; exit 1; }

t0=$(python3 -c 'import time; print(time.time())')
out=$("$portenv" app move "$box" "$target" 2>&1) || true; echo "  $out"
t1=$(python3 -c 'import time; print(time.time())')
expect "Move To ▸ $target ($(python3 -c "print(round($t1 - $t0, 1))") s)" grep -q "open on $target" <<<"$out"
on_server() { R sudo docker exec -u work -w /home/work "portenv-$(box_id)" bash -lc "'$*'"; }
expect "the server has the Mac's files" bash -c "[[ \$(${ssh_cmd[*]} -- $target sudo docker exec -u work portenv-$(box_id) cat /home/work/note.txt </dev/null) == from-the-mac ]]"
server_sum="sum=$(R sudo docker exec -u work "portenv-$(box_id)" sh -c "'sha256sum /home/work/data.bin | cut -c1-16'")"
expect "the 5 MB file is intact on the server ($server_sum)" test "$server_sum" = "$sum"
on_server 'echo changed-on-the-server > note.txt'

t0=$(python3 -c 'import time; print(time.time())')
out=$("$portenv" app move "$box" this-mac 2>&1) || true; echo "  $out"
t1=$(python3 -c 'import time; print(time.time())')
expect "Move To ▸ This Mac ($(python3 -c "print(round($t1 - $t0, 1))") s, rule 5)" grep -q "rule 5" <<<"$out"
out=$(type_in 'echo note=$(cat ~/note.txt) sum=$(sha256sum ~/data.bin | cut -c1-16)')
expect "the server's change is back on this Mac" grep -q "note=changed-on-the-server" <<<"$out"
expect "the 5 MB file is intact back on this Mac" grep -q "$sum" <<<"$out"
"$portenv" app close "$box" >/dev/null 2>&1 || true

if (( failures )); then echo "$failures check(s) failed"; tail -20 "$root/portenvd.log" | sed 's/^/  portenvd: /'; exit 1; fi
echo "all checks passed"
