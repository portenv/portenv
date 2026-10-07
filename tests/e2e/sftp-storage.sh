#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# SFTP storage end to end, locally: an Ubuntu container stands in for a
# server (sshd plus the storage account from server/setup.sh), and a box on
# this machine saves to it and resumes. Covers the storage account when uid
# and gid 990 are already taken, the authorized-keys file being readable by
# sshd, the agent serving the key from memory and pinning the host key, and
# restic's cache surviving restarts.
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
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-sftp-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-sftp-$id" "portenv-cache-sftp-$id" >/dev/null 2>&1 || true
	fi
	docker rm -f "$srv" >/dev/null 2>&1 || true
	rm -rf "$root" || true
}
trap cleanup EXIT

echo "== stand-in server: sshd, with uid and gid 990 already taken"
docker run -d --name "$srv" ubuntu:24.04 sleep infinity >/dev/null
docker exec "$srv" bash -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-server >/dev/null && groupadd -g 990 taken && useradd -u 990 -g 990 -M taken && mkdir -p /run/sshd && /usr/sbin/sshd'
docker cp "$repo/server/setup.sh" "$srv:/tmp/setup.sh" >/dev/null
ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$srv")
host_key=$(docker exec "$srv" cut -d' ' -f1,2 /etc/ssh/ssh_host_ed25519_key.pub)

echo "== box with storage on the stand-in server"
"$portenv" init sftp --image "$image" --storage "sftp:portenv-storage@$ip:/storage" --storage-host-key "$host_key" > "$root/init.txt"
pub=$(grep '^ssh-ed25519 ' "$root/init.txt")
docker exec "$srv" bash /tmp/setup.sh --only storage-account --storage-key "$pub" | sed 's/^/  /'
expect "the storage account exists without uid 990" bash -c "[[ \$(docker exec $srv id -u portenv-storage) != 990 ]]"
expect "sshd can read the authorized keys as portenv-storage" docker exec "$srv" runuser -u portenv-storage -- test -r /etc/ssh/portenv-storage.authorized_keys

out=$("$portenv" resume sftp </dev/null 2>&1 | tail -1); echo "  $out"
expect "rule 1 with SFTP storage" grep -q "rule 1" <<<"$out"
in_box 'echo "over sftp" > note.txt'
expect "save over SFTP" "$portenv" save sftp </dev/null
expect "no key or helper left in the box after the run" bash -c "[[ -z \$(docker exec portenv-sftp-$(box_id) sh -c 'ls -A /var/cache/portenv-sync | grep sftp-') ]]"
expect "the repository is on the server, owned by portenv-storage" docker exec "$srv" bash -c "test -f /srv/portenv/storage-root/storage/boxes/$(box_id)/config && [[ \$(stat -c %U /srv/portenv/storage-root/storage/boxes/$(box_id)/config) == portenv-storage ]]"
expect "close" "$portenv" close sftp </dev/null
out=$("$portenv" resume sftp </dev/null 2>&1 | tail -1); echo "  $out"
expect "same-machine resume over SFTP (rule 3)" grep -q "rule 3" <<<"$out"
expect "the file is there" in_box 'grep -qx "over sftp" note.txt'
expect "restic's cache survived the restart" bash -c "[[ -n \$(docker exec portenv-sftp-$(box_id) sh -c 'ls /var/cache/portenv-sync') ]]"
docker exec "$srv" bash -c "sed -i 's/^restrict /#restrict /' /etc/ssh/portenv-storage.authorized_keys"
expect "an unknown key is refused (no save without the server's consent)" bash -c "! $portenv save sftp </dev/null"
expect "close after restoring the key" bash -c "docker exec $srv sed -i 's/^#restrict /restrict /' /etc/ssh/portenv-storage.authorized_keys && $portenv close sftp </dev/null"

echo
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
