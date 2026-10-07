#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Spike: restic's REST server on the storage server's loopback, reached from
# this Mac through a port forward over the storage account's reused SSH
# connection, for everything restic does on the host (listing, lease tags).
# Backup and restore in the box still use SFTP. Measures online resume
# (median and worst of 5) and close with a 5 MB change, first with SFTP and
# then with REST, on the same box and link, with the gate's trace.
#
#   PORTENV_SSH="ssh -i KEY -o ..." tests/e2e/rest-spike.sh USER@HOST "ssh-ed25519 AAAA..." [IMAGE]
#
# Needs a server set up by the gate (server/setup.sh, the toolbox image) and
# rest-server for the server's architecture at $REST_SERVER_BIN.
set -euo pipefail
target=${1:?usage: rest-spike.sh USER@HOST HOST-KEY [IMAGE]}
host_key=${2:?usage: rest-spike.sh USER@HOST HOST-KEY [IMAGE]}
image=${3:-portenv/toolbox-node:dev}
rest_bin=${REST_SERVER_BIN:?set REST_SERVER_BIN to rest-server built for the server}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/portenv-spike.XXXXXX")
read -r -a ssh_cmd <<<"${PORTENV_SSH:-ssh}"
export PORTENV_KEYS=file PORTENV_HOME=$root/mac PORTENV_DOCKER_NAMESPACE=spike
box=spike-$(date +%Y%m%d%H%M%S)
R() { "${ssh_cmd[@]}" -- "$target" "$@" </dev/null; }
Rin() { "${ssh_cmd[@]}" -- "$target" "$@"; }
seconds() { python3 -c 'import time; print(f"{time.time():.3f}")'; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/$box.json"; }
mac_box() { docker exec -u work -w /home/work "portenv-spike-$(box_id)" bash -lc "$*"; }
timed() {
	local name=$1 t0 t1 all; shift
	t0=$(seconds)
	all=$(PORTENV_TRACE=1 "$@" </dev/null 2>&1) || { echo "$all" | sed 's/^/  /'; echo "FAIL  $name"; exit 1; }
	t1=$(seconds)
	took=$(python3 -c "print(round($t1 - $t0, 2))")
	echo "  $name: ${took} s"; grep '^trace' <<<"$all" | sed 's/^/    /' || true
}
stats() { python3 -c 'import statistics, sys; v = [float(x) for x in sys.argv[1:]]; print(round(statistics.median(v), 2), round(max(v), 2))' "$@"; }
cleanup() {
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-spike-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-spike-$id" "portenv-cache-spike-$id" >/dev/null 2>&1 || true
	fi
	rm -rf "$root" || true
}
trap cleanup EXIT

echo "== link"
rtt_ms=$(python3 - "${target#*@}" <<'PY'
import socket, statistics, sys, time
ts = []
for _ in range(7):
    t = time.time(); s = socket.create_connection((sys.argv[1], 22), 5); ts.append((time.time() - t) * 1000); s.close()
print(round(statistics.median(ts)))
PY
)
head -c 2000000 /dev/urandom > "$root/probe.bin"
t0=$(seconds); R true; t1=$(seconds); ssh_s=$(python3 -c "print(round($t1 - $t0, 2))")
t0=$(seconds); Rin 'cat > /dev/null' < "$root/probe.bin"; t1=$(seconds)
up_mbit=$(python3 -c "print(round(2000000 * 8 / 1e6 / max($t1 - $t0 - $ssh_s, 0.001), 2))")
echo "  upload ${up_mbit} Mbit/s, round trip ${rtt_ms} ms"

echo "== box with SFTP storage"
"$portenv" init "$box" --image "$image" --storage "sftp:portenv-storage@${target#*@}:/storage" --storage-host-key "$host_key" > "$root/init.txt"
pub=$(grep '^ssh-ed25519 ' "$root/init.txt")
# The spike's one server change: this key may also open the REST port (and
# nothing else); sshd allows local forwards to that port only.
R "echo 'restrict,port-forwarding,permitopen=\"127.0.0.1:8000\" $pub' | sudo tee -a /etc/ssh/portenv-storage.authorized_keys >/dev/null"
R "sudo sed -i 's/^\tAllowTcpForwarding no/\tAllowTcpForwarding local\n\tPermitOpen 127.0.0.1:8000/' /etc/ssh/sshd_config.d/50-portenv-storage.conf && sudo sshd -t && sudo systemctl reload ssh"
"$portenv" resume "$box" >/dev/null 2>&1
mac_box 'mkdir -p acme-api && head -c 20000000 /dev/urandom > acme-api/data.bin'
"$portenv" save "$box" >/dev/null

echo "== REST server on the server's loopback (rest-server $("$rest_bin" --version 2>/dev/null | head -1 || true))"
gzip -9c "$rest_bin" | Rin 'gunzip > /tmp/rest-server && sudo install -m 0755 /tmp/rest-server /usr/local/bin/rest-server'
rest_pw=$(python3 -c 'import secrets; print(secrets.token_urlsafe(24))')
R 'sudo apt-get install -y -qq apache2-utils >/dev/null'
printf '%s' "$rest_pw" | Rin 'sudo htpasswd -B -i -c /srv/portenv/rest.htpasswd portenv-host 2>/dev/null && sudo chown root:portenv-storage /srv/portenv/rest.htpasswd && sudo chmod 0640 /srv/portenv/rest.htpasswd'
R 'sudo systemctl stop portenv-rest 2>/dev/null; sudo systemd-run --unit portenv-rest -p User=portenv-storage -p Group=portenv-storage /usr/local/bin/rest-server --path /srv/portenv/storage-root/storage/boxes --listen 127.0.0.1:8000 --htpasswd-file /srv/portenv/rest.htpasswd >/dev/null; sleep 1; sudo ss -Htln | grep -q "127.0.0.1:8000"' && echo "  listening on 127.0.0.1:8000 only"

measure() {
	local label=$1
	echo "== $label: close with a 5 MB change"
	mac_box "head -c 5000000 /dev/urandom > acme-api/change-$label.bin"
	timed "close" "$portenv" close "$box"
	eval "close_$label=$took"
	echo "== $label: online resume, 5 runs"
	local runs=() i
	for i in 1 2 3 4 5; do
		timed "resume $i" "$portenv" resume "$box"
		runs+=("$took")
		[[ $i == 5 ]] || "$portenv" close "$box" >/dev/null
	done
	read -r med worst <<<"$(stats "${runs[@]}")"
	eval "resume_$label='$med (worst $worst)'"
}
measure sftp
export PORTENV_SPIKE_REST="portenv-host:$rest_pw@127.0.0.1:8000"
measure rest
unset PORTENV_SPIKE_REST
"$portenv" close "$box" >/dev/null

echo
echo "link: upload ${up_mbit} Mbit/s, round trip ${rtt_ms} ms"
echo "SFTP: resume median ${resume_sftp} s; close with 5 MB ${close_sftp} s"
echo "REST: resume median ${resume_rest} s; close with 5 MB ${close_rest} s"
