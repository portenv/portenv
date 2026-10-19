#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# counting-snapshot.sh [--share]: the owner's daily snapshot (PLAN.md 1.8).
# Reads the counting Worker's counters with the owner's own Cloudflare
# sign-in (`wrangler login`; no credential in the repository), adds the
# download archive (scripts/archive-downloads.sh), and writes daily.csv,
# cohorts.csv and snapshot.html to $PORTENV_METRICS_DIR/snapshot/<date>/
# (default ~/Portenv-metrics). Open snapshot.html locally; nothing is
# published. --share hides cells from 1 to 9, for anything shown outside the
# owner (R-0010 point 2).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

wrangler=${WRANGLER:-npx --yes wrangler@4.145.0}
dir=${PORTENV_METRICS_DIR:-$HOME/Portenv-metrics}
today=$(date -u +%F)
out=$dir/snapshot/$today${1:+-share}
mkdir -p "$out"

counts=$out/counts.json
# shellcheck disable=SC2086 # $wrangler is a command with its arguments
(cd tools/counting/worker && $wrangler d1 execute portenv-counting --remote --json \
	--command "SELECT day, field, value, n FROM counts ORDER BY day") >"$counts"

node tools/counting/snapshot.mjs --counts "$counts" --metrics "$dir" --out "$out" "$@"
