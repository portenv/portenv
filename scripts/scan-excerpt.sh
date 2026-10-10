#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# scan-excerpt.sh LOG: print a short excerpt of a nightly channel-scan log,
# safe to post in a public GitHub issue. It keeps the failing test lines and
# the last 40 lines, then redacts:
#   - every run of 64 or more hex characters (the channel token's form);
#   - every value listed in $REDACT (space-separated);
#   - any PEM block (BEGIN ... END), whole.
# The output is capped well under GitHub's issue size limit.
set -euo pipefail
log=${1:?usage: scan-excerpt.sh LOG}

excerpt=$(
	{
		grep -E -- '--- FAIL|^FAIL|_test\.go:[0-9]+:|panic:|timed out|deadline' "$log" || true
		echo '...'
		tail -n 40 "$log"
	} | sed -E '/-----BEGIN [A-Z ]+-----/,/-----END [A-Z ]+-----/c\
[redacted PEM block]' | sed -E 's/[0-9A-Fa-f]{64,}/[redacted]/g'
)
for v in ${REDACT:-}; do
	[[ -n $v ]] || continue
	excerpt=${excerpt//"$v"/[redacted]}
done
printf '%s\n' "$excerpt" | head -c 60000
