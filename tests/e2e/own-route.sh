#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Own-route guarantee (ADR 0008): the developer-managed route (this machine,
# the developer's servers and storage, agents over SSH and the CLI) never
# depends on Portenv's hosted services. This runs the own-route journey with
# every hosted endpoint unreachable, blocked at DNS and at the firewall for
# this machine and its containers:
#
#   tests/e2e/own-route.sh [IMAGE]     (Linux CI runner: blocking uses sudo)
#
# It uses only locally built artifacts (CLI, image), so no distribution URL
# is on its path. Steps that do not exist yet are listed as pending; each
# joins the journey in the milestone that builds it.
set -euo pipefail
image=${1:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
list=$repo/tests/e2e/hosted-endpoints.txt
mapfile -t hosts < <(grep -vE '^\s*(#|$)' "$list")
[[ $(uname -s) == Linux ]] || { echo "own-route.sh changes /etc/hosts and the firewall: run it on a disposable Linux runner" >&2; exit 2; }

echo "== every portenv.com host named in the source is in hosted-endpoints.txt"
unlisted=$(cd "$repo" && git ls-files -z -- ':!docs/' ':!tests/e2e/hosted-endpoints.txt' |
	xargs -0 grep -ohIE '\b([a-z0-9-]+\.)*portenv\.com\b' 2>/dev/null | sort -u |
	while read -r h; do grep -qxF "$h" <(printf '%s\n' "${hosts[@]}") || echo "$h"; done)
if [[ -n $unlisted ]]; then echo "FAIL  not in hosted-endpoints.txt: $unlisted"; exit 1; fi
echo "ok    none unlisted"

echo "== block hosted endpoints at DNS and the firewall"
# Resolve first, then drop those addresses for this machine (OUTPUT) and its
# containers (DOCKER-USER), then point the names nowhere.
for h in "${hosts[@]}"; do
	for ip in $(getent ahosts "$h" 2>/dev/null | awk '{print $1}' | sort -u); do
		if [[ $ip == *:* ]]; then t=ip6tables; else t=iptables; fi
		sudo "$t" -I OUTPUT -d "$ip" -j REJECT
		sudo "$t" -I DOCKER-USER -d "$ip" -j REJECT 2>/dev/null || true
	done
	printf '0.0.0.0 %s\n:: %s\n' "$h" "$h" | sudo tee -a /etc/hosts >/dev/null
done
# Containers do not read the host's /etc/hosts, so inside a box the names
# may still resolve; the firewall drops their addresses there. Check both
# paths, failing closed: only curl's "could not resolve", "could not
# connect" and "timed out" (exit 6, 7, 28) count as blocked.
blocked() { local rc=0; "$@" || rc=$?; [[ $rc == 6 || $rc == 7 || $rc == 28 ]]; }
for h in "${hosts[@]}"; do
	blocked curl -sS -m 5 -o /dev/null "https://$h/" 2>/dev/null || { echo "FAIL  $h is reachable from this machine (or the check could not run)"; exit 1; }
	blocked docker run --rm --entrypoint curl "$image" -sS -m 5 -o /dev/null "https://$h/" 2>/dev/null || { echo "FAIL  $h is reachable from a box (or the check could not run)"; exit 1; }
done
echo "ok    ${#hosts[@]} hosted endpoints unreachable from this machine and from boxes"

echo "== own-route journey"
# Create a box, autosave, move between machines with matching checksums
# (two machines sharing storage), take over, keep unsaved work.
"$repo/tests/e2e/two-machines.sh" "$image"
# A server reached over SSH (SFTP storage), offline resume.
"$repo/tests/e2e/sftp-storage.sh" "$image"
echo "pending  Revert To ▸ Last Save Point (milestone 1.0)"
echo "pending  the app's own-route flows with hosted options visible and untouched (Phase 1)"
echo "pending  first run completes through Continue without an account on the Welcome screen (Phase 3 first run)"
echo "pending  an agent connecting over SSH and the CLI and relaying a question (milestone 2.7)"
echo
echo "own route: all checks passed with every hosted endpoint unreachable"
