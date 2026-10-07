#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Phase 0 gate against a real, disposable server:
#
#   PORTENV_SSH="ssh -i KEY -o ..." tests/e2e/server-gate.sh USER@HOST "ssh-ed25519 AAAA..." [IMAGE]
#
# PORTENV_SSH is the ssh command (with options) that reaches the server; the
# second argument is the server's host key, pinned for SFTP storage. The
# script sets the server up (server/setup.sh), loads the toolbox image, then
# runs a Mac → server → Mac round trip with SFTP storage on the server,
# comparing checksums of /home, and measures the Phase 0 budgets (resume,
# close, freshness) with the per-phase trace of each measured command.
# Uses docker exec to inspect boxes (Phase 0 tests only).
set -euo pipefail
target=${1:?usage: server-gate.sh USER@HOST HOST-KEY [IMAGE]}
host_key=${2:?usage: server-gate.sh USER@HOST HOST-KEY [IMAGE]}
[[ $host_key =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+$ ]] || { echo "HOST-KEY must be one line: ssh-ed25519 AAAA..." >&2; exit 2; }
image=${3:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/portenv-gate.XXXXXX")
read -r -a ssh_cmd <<<"${PORTENV_SSH:-ssh}"
export PORTENV_KEYS=file PORTENV_HOME=$root/mac PORTENV_DOCKER_NAMESPACE=gate
failures=0
# A new box name per run: the server keeps its boxes between runs, and the
# gate never deletes a box or its saves.
box=gate-$(date +%Y%m%d%H%M%S)
# R runs a command on the server with no input (an ssh that inherits an
# open stdin can wait forever after the remote command ends); Rin passes
# stdin through for uploads.
R() { "${ssh_cmd[@]}" -- "$target" "$@" </dev/null; }
Rin() { "${ssh_cmd[@]}" -- "$target" "$@"; }
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/$box.json"; }
mac_box() { docker exec -u work -w /home/work "portenv-gate-$(box_id)" bash -lc "$*"; }
server_box() { R sudo docker exec -u work -w /home/work "portenv-$(box_id)" bash -lc "'$*'"; }
sums_cmd='cd /home && find . -type f -not -path "./work/.cache/*" -print0 | sort -z | xargs -0 sha256sum'
seconds() { python3 -c 'import time; print(f"{time.time():.3f}")'; }
# timed NAME CMD...: runs a portenv command with the trace on, prints its
# trace lines and result indented, and sets $took (seconds) and $out (its
# last line). A failing command fails the gate.
timed() {
	local name=$1 t0 t1 all; shift
	t0=$(seconds)
	all=$(PORTENV_TRACE=1 "$@" </dev/null 2>&1) || { echo "$all" | sed 's/^/  /'; fail "$name: command failed"; exit 1; }
	t1=$(seconds)
	took=$(python3 -c "print(round($t1 - $t0, 2))"); out=$(grep -v '^trace' <<<"$all" | tail -1)
	echo "  $name: ${took} s"; grep '^trace' <<<"$all" | sed 's/^/    /' || true
}
# stats prints "median worst" of its arguments.
stats() { python3 -c 'import statistics, sys; v = [float(x) for x in sys.argv[1:]]; print(round(statistics.median(v), 2), round(max(v), 2))' "$@"; }
lt() { python3 -c "import sys; sys.exit(0 if $1 < $2 else 1)"; }

cleanup() {
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-gate-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-gate-$id" >/dev/null 2>&1 || true
	fi
	rm -rf "$root" || true
}
trap cleanup EXIT

echo "== server setup"
arch=$(R dpkg --print-architecture)
(cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "$root/portenv-linux" ./core/cmd/portenv)
"$portenv" init "$box" --image "$image" --storage "sftp:portenv-storage@${target#*@}:/storage" --storage-host-key "$host_key" --storage-rest 127.0.0.1:7422 > "$root/init.txt"
storage_pub=$(grep '^ssh-ed25519 ' "$root/init.txt")
rest_user=$(sed -n 's/^rest-user //p' "$root/init.txt")
listening() { R sudo ss -Htln | awk '{print $4}' | grep -vE '^(127\.|\[::1\]|.*%lo:)' | sed 's/.*://' | sort -u | tr '\n' ' '; }
ports_before=$(listening)
Rin 'cat > /tmp/portenv-setup.sh' < "$repo/server/setup.sh"
if [[ $(R 'sha256sum /tmp/portenv 2>/dev/null | cut -d" " -f1') == $(shasum -a 256 "$root/portenv-linux" | cut -d' ' -f1) ]]; then
	echo "  the server already has this portenv build"
else
	gzip -9c "$root/portenv-linux" | Rin 'gunzip > /tmp/portenv && chmod +x /tmp/portenv'
fi
R sudo bash /tmp/portenv-setup.sh --portenv-bin /tmp/portenv --storage-key "'$storage_pub'" --rest-user "'$rest_user'" | sed 's/^/  /'
ports_after=$(listening)
if [[ "$ports_before" == "$ports_after" ]]; then pass "no new listening port on the server (only: $ports_after)"; else fail "no new listening port on the server (before: $ports_before, after: $ports_after)"; fi
expect "the homes volume is LUKS-encrypted and mounted" R 'sudo cryptsetup status portenv-homes | grep -q "type:.*LUKS2" && mountpoint -q /var/lib/portenv/homes'
expect "the REST server listens on the loopback address only" R 'sudo ss -Htln | grep -q "127.0.0.1:7422" && ! sudo ss -Htln | grep -qE "(0\.0\.0\.0|\*|\[::\]):7422"'
expect "the REST password file holds only bcrypt hashes, readable by root and the storage account" R '[[ $(sudo stat -c "%a %U %G" /etc/portenv-rest.htpasswd) == "640 root portenv-storage" ]] && ! sudo grep -vqE "^box-[0-9a-f]+:\$2a\$" /etc/portenv-rest.htpasswd'
expect "the LUKS key file is root-only" R '[[ $(sudo stat -c "%a %U" /etc/portenv/luks/homes.key) == "400 root" ]]'

echo "== toolbox image (built on the server from the same Dockerfile)"
# Sending the source (a few MB) and building there is far faster than
# uploading the image from a slow connection, and is what a machine without
# the registry does anyway.
(cd "$repo" && git ls-files -z | COPYFILE_DISABLE=1 tar --no-mac-metadata --null -T - -czf - ) | Rin 'rm -rf /tmp/portenv-src && mkdir -p /tmp/portenv-src && tar -xzf - -C /tmp/portenv-src'
R "cd /tmp/portenv-src && sudo docker build --progress=plain -f images/toolbox-node/Dockerfile -t '$image' . 2>&1 | grep -E '^#[0-9]+ (DONE|ERROR)|naming to|ERROR' | tail -3" | sed 's/^/  /'
expect "the toolbox image is on the server" R sudo docker image inspect "$image"

echo "== link from this machine to the server"
# Round-trip time: the median TCP connect time to the server's SSH port.
rtt_ms=$(python3 - "${target#*@}" <<'PY'
import socket, statistics, sys, time
ts = []
for _ in range(7):
    t = time.time(); s = socket.create_connection((sys.argv[1], 22), 5); ts.append((time.time() - t) * 1000); s.close()
print(round(statistics.median(ts)))
PY
)
t0=$(seconds); R true; t1=$(seconds)
rtt_s=$(python3 -c "print(round($t1 - $t0, 2))")
head -c 2000000 /dev/urandom > "$root/probe.bin"
t0=$(seconds); Rin 'cat > /dev/null' < "$root/probe.bin"; t1=$(seconds)
up_bps=$(python3 -c "print(int(2000000 / max($t1 - $t0 - $rtt_s, 0.001)))")
up_mbit=$(python3 -c "print(round($up_bps * 8 / 1e6, 2))")
echo "  upload ${up_mbit} Mbit/s, round trip ${rtt_ms} ms, SSH connection setup ${rtt_s} s"

echo "== Mac: new box with storage on the server (SFTP)"
out=$("$portenv" resume "$box" 2>&1) || { echo "$out" | sed 's/^/  /'; fail "command failed: "; exit 1; }; out=$(tail -1 <<<"$out"); echo "  $out"
expect "rule 1 on the Mac" grep -q "rule 1" <<<"$out"
mac_box 'mkdir -p acme-api && echo "hello from the Mac" > acme-api/README.md && head -c 20000000 /dev/urandom > acme-api/data.bin && echo jq >> .portenv/apt-packages.txt'
expect "first save over SFTP" "$portenv" save "$box"
# The close budget is stated at 20 Mbit/s or faster up and 50 ms or less.
budget_link=0
if (( rtt_ms <= 50 )) && ! lt "$up_mbit" 20; then budget_link=1; fi

echo "== budget: close (final save and release with a 5 MB unsaved change)"
mac_box 'head -c 5000000 /dev/urandom > acme-api/change.bin'
timed "close with a 5 MB change" "$portenv" close "$box"
close_s=$took
# At another bandwidth, the same close with the transfer at 20 Mbit/s.
close_at20=$(python3 -c "print(round($close_s - 5000000 / $up_bps + 5000000 * 8 / 20e6, 1))")
if (( budget_link )); then
	expect "close with a 5 MB change under 15 s (${close_s} s at ${up_mbit} Mbit/s up, ${rtt_ms} ms)" lt "$close_s" 15
elif (( rtt_ms <= 50 )); then
	echo "skip  the close budget is stated at 20 Mbit/s up; this uplink is ${up_mbit} Mbit/s: ${close_s} s measured, ${close_at20} s with the transfer at 20 Mbit/s"
	expect "close with a 5 MB change, transfer scaled to 20 Mbit/s, under 15 s (${close_at20} s)" lt "$close_at20" 15
else
	echo "skip  the close budget is stated at a round trip of 50 ms or less; this link is ${rtt_ms} ms (close took ${close_s} s)"
fi

echo "== budget: same-machine resume, storage reachable (5 runs)"
runs=()
for i in 1 2 3 4 5; do
	timed "resume $i" "$portenv" resume "$box"
	[[ $i == 1 ]] && expect "same-machine resume is rule 3" grep -q "rule 3" <<<"$out"
	runs+=("$took")
	"$portenv" close "$box" >/dev/null
done
read -r resume_s resume_worst <<<"$(stats "${runs[@]}")"
if (( rtt_ms <= 50 )); then
	expect "same-machine resume under 5 s to a ready box (median ${resume_s} s, worst ${resume_worst} s, ${rtt_ms} ms round trip)" lt "$resume_s" 5
else
	echo "skip  the online resume budget applies at a round trip of 50 ms or less; this link is ${rtt_ms} ms (median ${resume_s} s, worst ${resume_worst} s)"
fi

echo "== budget: freshness (continuous editing, autosave every ${PORTENV_AUTOSAVE:-30} s)"
# The box edits files every second while the gate autosaves on a fixed
# interval from each save's start. An edit made just after a save started
# is in the next save, so the newest save is at most (next save's end -
# this save's start + 1 s) behind; the gate reports the worst bound.
interval=${PORTENV_AUTOSAVE:-30}
"$portenv" resume "$box" >/dev/null 2>&1
docker exec -d -u work -w /home/work "portenv-gate-$(box_id)" bash -c 'while :; do date +%s.%N > acme-api/stamp; head -c 200000 /dev/urandom > acme-api/edit.bin; echo "$RANDOM" >> acme-api/log.txt; sleep 1; done'
starts=() ends=()
for i in 1 2 3 4 5 6; do
	s=$(seconds); timed "autosave $i" "$portenv" save "$box"; e=$(seconds)
	starts+=("$s"); ends+=("$e")
	sleep "$(python3 -c "print(max(0, $s + $interval - $e))")"
done
lag=$(python3 - "${starts[*]}" "${ends[*]}" <<'PY'
import sys
s = [float(x) for x in sys.argv[1].split()]; e = [float(x) for x in sys.argv[2].split()]
print(round(max(e[k + 1] - s[k] + 1 for k in range(len(s) - 1)), 1))
PY
)
# Separate calls: a pkill pattern in the same command line would match it.
mac_box 'pkill -f "acme-api/[s]tamp" || true'
mac_box 'rm -f acme-api/stamp acme-api/edit.bin acme-api/log.txt'
"$portenv" save "$box" >/dev/null
expect "newest save never more than 60 s behind (worst ${lag} s with autosave every ${interval} s at ${up_mbit} Mbit/s)" lt "$lag" 60
"$portenv" close "$box" >/dev/null
"$portenv" resume "$box" >/dev/null 2>&1
before=$(mac_box "$sums_cmd")

echo "== Mac → server"
out=$("$portenv" move "$box" --to "$target" --join-storage sftp:portenv-storage@host.portenv.internal:/storage </dev/null 2>&1) || { echo "$out" | sed 's/^/  /'; fail "command failed: "; exit 1; }; out=$(tail -1 <<<"$out"); echo "  $out"
expect "the server restores the box (rule 5)" grep -q "rule 5" <<<"$out"
after=$(server_box "$sums_cmd")
if [[ -n $before && "$before" == "$after" ]]; then pass "checksums of /home match on the server"; else fail "checksums of /home match on the server"; fi
expect "apt-packages.txt replayed on the server" server_box 'command -v jq'
expect "the home is on the encrypted volume" R "sudo test -d /var/lib/portenv/homes/portenv-home-$(box_id)/work"
server_box 'echo "edited on the server" >> acme-api/README.md'
before=$(server_box "$sums_cmd")
expect "the server added its own repository key (it never stores the Mac's)" bash -c "[[ \$(${ssh_cmd[*]} -- $target sudo sha256sum /root/.config/Portenv/keys/$(box_id).key </dev/null | cut -d' ' -f1) != \$(shasum -a 256 $root/mac/keys/$(box_id).key | cut -d' ' -f1) ]]"
expect "server closes" R sudo portenv close "$box"
echo "== server: same-machine resume against its own storage, traced"
R "sudo PORTENV_TRACE=1 portenv resume "$box" 2>&1 | grep -E '^trace|rule'" | sed 's/^/  /'
expect "server closes again" R sudo portenv close "$box"

echo "== server → Mac"
out=$("$portenv" resume "$box" 2>&1) || { echo "$out" | sed 's/^/  /'; fail "command failed: "; exit 1; }; out=$(tail -1 <<<"$out"); echo "  $out"
expect "the Mac restores the server's change (rule 5)" grep -q "rule 5" <<<"$out"
after=$(mac_box "$sums_cmd")
if [[ -n $before && "$before" == "$after" ]]; then pass "checksums of /home match back on the Mac"; else fail "checksums of /home match back on the Mac"; fi
expect "Mac closes" "$portenv" close "$box"
"$portenv" history "$box" | sed 's/^/  /'

echo "== Mac: offline resume (storage pointed at an unreachable address)"
cfg=$root/mac/boxes/$box.json
cp "$cfg" "$cfg.online"
python3 - "$cfg" "${target#*@}" <<'PY'
import json, sys
c = json.load(open(sys.argv[1])); c["storage"] = c["storage"].replace(sys.argv[2], "192.0.2.1")
json.dump(c, open(sys.argv[1], "w"))
PY
# Each run starts from the same clean-close state (an offline resume marks
# the box open, which rightly blocks the next offline start).
state=$root/mac/state/$(box_id)
cp -R "$state" "$state.clean"
runs=()
for i in 1 2 3 4 5; do
	timed "offline resume $i" "$portenv" resume "$box"
	[[ $i == 1 ]] && expect "offline resume after a clean close (Offline · will save later)" grep -q "Offline · will save later" <<<"$out"
	runs+=("$took")
	docker stop -t 2 "portenv-gate-$(box_id)" >/dev/null 2>&1 || true
	rm -rf "$state" && cp -R "$state.clean" "$state"
done
read -r offline_s offline_worst <<<"$(stats "${runs[@]}")"
expect "offline resume under 5 s to a ready box (median ${offline_s} s, worst ${offline_worst} s)" lt "$offline_s" 5
rm -rf "$state.clean"
mv "$cfg.online" "$cfg"

echo
echo "link: upload ${up_mbit} Mbit/s, round trip ${rtt_ms} ms, SSH connection setup ${rtt_s} s"
echo "budgets: resume median ${resume_s} s (worst ${resume_worst} s) online, ${offline_s} s (worst ${offline_worst} s) offline;"
echo "         close with 5 MB ${close_s} s at ${up_mbit} Mbit/s (${close_at20} s at 20 Mbit/s); freshness worst ${lag} s with autosave every ${interval} s"
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
