#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Fails if any tracked file is a Mach-O or ELF binary. Builds go to bin/
# (ignored); a binary in the repository is public, and a local build can
# carry the builder's paths (a stray one did, in #15).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

found=0
while IFS= read -r -d '' f; do
	[[ -f $f ]] || continue
	if file -b "$f" | grep -qE '^(Mach-O|ELF)'; then
		echo "tracked binary: $f" >&2
		found=1
	fi
done < <(git ls-files -z)
exit "$found"
