#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Checks ADR 0005 against a toolbox image: while restic runs in the box with
# the repository password, no other process in the box can read it, root
# included, and it never reaches disk.
#
#   tests/e2e/restic-isolation.sh portenv/toolbox-node:dev
#
# Uses docker exec, which is acceptable only in Phase 0 tests.
set -euo pipefail
image=${1:?usage: restic-isolation.sh IMAGE}
here=$(cd "$(dirname "$0")" && pwd)
c=portenv-isolation-$$
trap 'docker rm -f "$c" >/dev/null 2>&1; docker volume rm -f "$c" >/dev/null 2>&1' EXIT

docker run -d --name "$c" -e PORTENV_INIT_HOME=1 -v "$c:/home" "$image" >/dev/null
for _ in $(seq 120); do
	[[ $(docker inspect -f '{{.State.Health.Status}}' "$c") == healthy ]] && break
	sleep 1
done
docker cp "$here/restic-isolation-probe.sh" "$c:/tmp/probe.sh" >/dev/null

results=$(docker exec "$c" bash /tmp/probe.sh sync; docker exec "$c" bash /tmp/probe.sh root)
echo "$results" | column -t
echo
# The design must deny every probe; the control (restic as plain root) shows
# what the design protects against.
if echo "$results" | grep '^sync ' | grep -qv 'DENIED$'; then
	echo "FAIL: the restic password is reachable from inside the box"; exit 1
fi
echo "ok: no process in the box can read the restic password"
