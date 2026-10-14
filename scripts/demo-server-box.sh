#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# For the demo's Move To step (milestone 1.0b): brings the server up to this
# build (portenv, portenv-runner, the SSH user portenv for this Mac's key),
# creates a box whose storage is on the server (SFTP, with the REST server),
# registers its storage key and REST user there, and adds portenv@HOST to
# this Mac's servers with HOST-KEY pinned (ADR 0021).
#
#   PORTENV_SSH="ssh -i KEY -o ..." scripts/demo-server-box.sh HOST "ssh-ed25519 AAAA..." [BOX] [IMAGE]
#
# Admin steps go as ubuntu@HOST; the app's own calls go as portenv@HOST,
# whose sudo allows only /usr/local/bin/portenv. MAC_KEY_FILE is this Mac's
# public key (default: KEY.pub from PORTENV_SSH's -i). The server must have
# IMAGE (built from the same source). Run it after the server has started:
# the box's storage address names the server's current address.
set -euo pipefail
host=${1:?usage: demo-server-box.sh HOST HOST-KEY [BOX] [IMAGE]}
host_key=${2:?usage: demo-server-box.sh HOST HOST-KEY [BOX] [IMAGE]}
box=${3:-demo}
image=${4:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/.." && pwd)
portenv=$repo/bin/portenv
read -r -a ssh_cmd <<<"${PORTENV_SSH:-ssh}"
home=${PORTENV_HOME:-$HOME/Library/Application Support/Portenv${PORTENV_INSTANCE:+-$PORTENV_INSTANCE}}
key=""
for ((i = 0; i < ${#ssh_cmd[@]}; i++)); do [[ ${ssh_cmd[i]} == -i ]] && key=${ssh_cmd[i + 1]}; done
mac_key=$(cut -d' ' -f1,2 "${MAC_KEY_FILE:-$key.pub}")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
A() { "${ssh_cmd[@]}" -- "ubuntu@$host" "$@" </dev/null; }
Ain() { "${ssh_cmd[@]}" -- "ubuntu@$host" "$@"; }

arch=$(A dpkg --print-architecture)
# The CLI CI built for this commit (PORTENV_LOCAL_BUILD=1 builds it here).
# shellcheck source=server-bins.sh
. "$repo/scripts/server-bins.sh"
server_bins "$arch" "$tmp/bins"
for b in portenv portenv-runner; do
	gzip -9c "$tmp/bins/$b" | Ain "gunzip > /tmp/$b && chmod +x /tmp/$b"
done
Ain 'cat > /tmp/portenv-setup.sh' < "$repo/server/setup.sh"
A sudo install -m 0755 /tmp/portenv /usr/local/bin/portenv
A sudo bash /tmp/portenv-setup.sh --only runner --runner-bin /tmp/portenv-runner --mac-key "'$mac_key'" >/dev/null

"$portenv" init "$box" --image "$image" --storage "sftp:portenv-storage@$host:/storage" \
	--storage-host-key "$host_key" --storage-rest 127.0.0.1:7422 > "$tmp/init.txt"
pub=$(grep '^ssh-ed25519 ' "$tmp/init.txt")
rest_user=$(sed -n 's/^rest-user //p' "$tmp/init.txt")
A sudo bash /tmp/portenv-setup.sh --only storage-account --storage-key "'$pub'" --rest-user "'$rest_user'" >/dev/null
# The test server's address changes at every start: keep only the current one.
"$portenv" app servers "$box" | while read -r old; do
	[[ $old == "portenv@$host" ]] || "$portenv" app remove-server "$old"
done
"$portenv" app trust-server "portenv@$host" "$host_key"
echo "box $box: storage on $host; Move To ▸ portenv@$host is in $home/servers.json"
