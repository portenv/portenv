#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# For the 1.0 demo's Move To step: creates a box whose storage is on a
# server set up by server/setup.sh (SFTP, with the REST server), registers
# its storage key and REST user on that server, and adds the server to this
# Mac's Move To list (machine.json).
#
#   PORTENV_SSH="ssh -i KEY -o ..." scripts/demo-server-box.sh USER@HOST "ssh-ed25519 AAAA..." [BOX] [IMAGE]
#
# Run it after the server has started: the box's storage address names the
# server's current address.
set -euo pipefail
target=${1:?usage: demo-server-box.sh USER@HOST HOST-KEY [BOX] [IMAGE]}
host_key=${2:?usage: demo-server-box.sh USER@HOST HOST-KEY [BOX] [IMAGE]}
box=${3:-demo}
image=${4:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/.." && pwd)
portenv=$repo/bin/portenv
read -r -a ssh_cmd <<<"${PORTENV_SSH:-ssh}"
home=${PORTENV_HOME:-$HOME/Library/Application Support/Portenv}
out=$(mktemp)
trap 'rm -f "$out"' EXIT

"$portenv" init "$box" --image "$image" --storage "sftp:portenv-storage@${target#*@}:/storage" \
	--storage-host-key "$host_key" --storage-rest 127.0.0.1:7422 > "$out"
pub=$(grep '^ssh-ed25519 ' "$out")
rest_user=$(sed -n 's/^rest-user //p' "$out")
"${ssh_cmd[@]}" -- "$target" 'cat > /tmp/portenv-setup.sh' < "$repo/server/setup.sh"
"${ssh_cmd[@]}" -- "$target" sudo bash /tmp/portenv-setup.sh --only storage-account \
	--storage-key "'$pub'" --rest-user "'$rest_user'" </dev/null >/dev/null
python3 - "$home/machine.json" "$target" <<'PY'
import json, os, sys
path, target = sys.argv[1], sys.argv[2]
cfg = json.load(open(path)) if os.path.exists(path) else {}
# The test server's address changes at every start: keep only the current one.
cfg["servers"] = [target]
json.dump(cfg, open(path, "w"), indent=2)
os.chmod(path, 0o600)
PY
echo "box $box: storage on $target; Move To ▸ $target is in $home/machine.json"
