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
# comparing checksums of /home, and measures the save and resume budgets.
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
# R runs a command on the server with no input (an ssh that inherits an
# open stdin can wait forever after the remote command ends); Rin passes
# stdin through for uploads.
R() { "${ssh_cmd[@]}" -- "$target" "$@" </dev/null; }
Rin() { "${ssh_cmd[@]}" -- "$target" "$@"; }
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/gate.json"; }
mac_box() { docker exec -u work -w /home/work "portenv-gate-$(box_id)" bash -lc "$*"; }
server_box() { R sudo docker exec -u work -w /home/work "portenv-$(box_id)" bash -lc "'$*'"; }
sums_cmd='cd /home && find . -type f -not -path "./work/.cache/*" -print0 | sort -z | xargs -0 sha256sum'
seconds() { python3 -c 'import time; print(f"{time.time():.3f}")'; }

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
"$portenv" init gate --image "$image" --storage "sftp:portenv-storage@${target#*@}:/storage" --storage-host-key "$host_key" > "$root/init.txt"
storage_pub=$(grep '^ssh-ed25519 ' "$root/init.txt")
listening() { R sudo ss -Htln | awk '{print $4}' | grep -vE '^(127\.|\[::1\]|.*%lo:)' | sed 's/.*://' | sort -u | tr '\n' ' '; }
ports_before=$(listening)
Rin 'cat > /tmp/portenv-setup.sh' < "$repo/server/setup.sh"
if [[ $(R 'sha256sum /tmp/portenv 2>/dev/null | cut -d" " -f1') == $(shasum -a 256 "$root/portenv-linux" | cut -d' ' -f1) ]]; then
	echo "  the server already has this portenv build"
else
	gzip -9c "$root/portenv-linux" | Rin 'gunzip > /tmp/portenv && chmod +x /tmp/portenv'
fi
R sudo bash /tmp/portenv-setup.sh --portenv-bin /tmp/portenv --storage-key "'$storage_pub'" | sed 's/^/  /'
ports_after=$(listening)
if [[ "$ports_before" == "$ports_after" ]]; then pass "no new listening port on the server (only: $ports_after)"; else fail "no new listening port on the server (before: $ports_before, after: $ports_after)"; fi
expect "the homes volume is LUKS-encrypted and mounted" R 'sudo cryptsetup status portenv-homes | grep -q "type:.*LUKS2" && mountpoint -q /var/lib/portenv/homes'
expect "the LUKS key file is root-only" R '[[ $(sudo stat -c "%a %U" /etc/portenv/luks/homes.key) == "400 root" ]]'

echo "== toolbox image (built on the server from the same Dockerfile)"
# Sending the source (a few MB) and building there is far faster than
# uploading the image from a slow connection, and is what a machine without
# the registry does anyway.
(cd "$repo" && git ls-files -z | COPYFILE_DISABLE=1 tar --no-mac-metadata --null -T - -czf - ) | Rin 'rm -rf /tmp/portenv-src && mkdir -p /tmp/portenv-src && tar -xzf - -C /tmp/portenv-src'
R "cd /tmp/portenv-src && sudo docker build --progress=plain -f images/toolbox-node/Dockerfile -t '$image' . 2>&1 | grep -E '^#[0-9]+ (DONE|ERROR)|naming to|ERROR' | tail -3" | sed 's/^/  /'
expect "the toolbox image is on the server" R sudo docker image inspect "$image"

echo "== link from this machine to the server"
t0=$(seconds); R true; t1=$(seconds)
rtt_s=$(python3 -c "print(round($t1 - $t0, 2))")
head -c 2000000 /dev/urandom > "$root/probe.bin"
t0=$(seconds); Rin 'cat > /dev/null' < "$root/probe.bin"; t1=$(seconds)
up_bps=$(python3 -c "print(int(2000000 / max($t1 - $t0 - $rtt_s, 0.001)))")
up_mbit=$(python3 -c "print(round($up_bps * 8 / 1e6, 2))")
echo "  upload ${up_mbit} Mbit/s, SSH connection setup ${rtt_s} s"

echo "== Mac: new box with storage on the server (SFTP)"
out=$("$portenv" resume gate 2>&1 | tail -1); echo "  $out"
expect "rule 1 on the Mac" grep -q "rule 1" <<<"$out"
mac_box 'mkdir -p acme-api && echo "hello from the Mac" > acme-api/README.md && head -c 20000000 /dev/urandom > acme-api/data.bin && echo jq >> .portenv/apt-packages.txt'
expect "first save over SFTP" "$portenv" save gate
mac_box 'head -c 5000000 /dev/urandom > acme-api/change.bin'
t0=$(seconds); "$portenv" save gate >/dev/null; t1=$(seconds)
save_s=$(python3 -c "print(round($t1 - $t0, 1))")
transfer_s=$(python3 -c "print(round(5000000 / $up_bps, 1))")
overhead_s=$(python3 -c "print(round($t1 - $t0 - 5000000 / $up_bps, 1))")
expect "save overhead beyond the transfer under 3 s (${overhead_s} s; save ${save_s} s, transfer ${transfer_s} s at ${up_mbit} Mbit/s)" python3 -c "import sys; sys.exit(0 if $overhead_s < 3 else 1)"
if python3 -c "import sys; sys.exit(0 if $up_mbit >= 20 else 1)"; then
	expect "a 5 MB change saves in under 10 s at ${up_mbit} Mbit/s (${save_s} s)" python3 -c "import sys; sys.exit(0 if $t1 - $t0 < 10 else 1)"
else
	echo "skip  the 10 s budget applies at 20 Mbit/s or faster; this uplink is ${up_mbit} Mbit/s"
fi
expect "Mac closes" "$portenv" close gate
t0=$(seconds); out=$("$portenv" resume gate 2>&1 | tail -1); t1=$(seconds)
resume_s=$(python3 -c "print(round($t1 - $t0, 1))")
expect "same-machine resume is rule 3" grep -q "rule 3" <<<"$out"
expect "same-machine resume takes under 5 s, storage reachable (${resume_s} s, including starting the box; ${up_mbit} Mbit/s, SSH setup ${rtt_s} s)" python3 -c "import sys; sys.exit(0 if $t1 - $t0 < 5 else 1)"
before=$(mac_box "$sums_cmd")

echo "== Mac → server"
out=$("$portenv" move gate --to "$target" --join-storage sftp:portenv-storage@host.portenv.internal:/storage </dev/null 2>&1 | tail -1); echo "  $out"
expect "the server restores the box (rule 5)" grep -q "rule 5" <<<"$out"
after=$(server_box "$sums_cmd")
if [[ -n $before && "$before" == "$after" ]]; then pass "checksums of /home match on the server"; else fail "checksums of /home match on the server"; fi
expect "apt-packages.txt replayed on the server" server_box 'command -v jq'
expect "the home is on the encrypted volume" R "sudo test -d /var/lib/portenv/homes/portenv-home-$(box_id)/work"
server_box 'echo "edited on the server" >> acme-api/README.md'
before=$(server_box "$sums_cmd")
expect "server closes" R sudo portenv close gate

echo "== server → Mac"
out=$("$portenv" resume gate 2>&1 | tail -1); echo "  $out"
expect "the Mac restores the server's change (rule 5)" grep -q "rule 5" <<<"$out"
after=$(mac_box "$sums_cmd")
if [[ -n $before && "$before" == "$after" ]]; then pass "checksums of /home match back on the Mac"; else fail "checksums of /home match back on the Mac"; fi
expect "Mac closes" "$portenv" close gate
"$portenv" history gate | sed 's/^/  /'

echo
echo "link: upload ${up_mbit} Mbit/s, SSH connection setup ${rtt_s} s"
echo "budgets: 5 MB save ${save_s} s (overhead ${overhead_s} s), same-machine resume ${resume_s} s"
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
