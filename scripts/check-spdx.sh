#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Fails if a source file lacks an SPDX-License-Identifier header in its first
# five lines. Generated code (proto/gen) is exempt.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

missing=0
while IFS= read -r f; do
	[[ -f "$f" ]] || continue
	if ! head -n 5 "$f" | grep -q 'SPDX-License-Identifier: Apache-2.0'; then
		echo "missing SPDX header: $f" >&2
		missing=1
	fi
done < <(git ls-files --cached --others --exclude-standard -- \
	'*.go' '*.proto' '*.swift' '*.sh' '*.yml' '*.yaml' 'Makefile' '**/Makefile' '**/Dockerfile' \
	':!proto/gen/**' ':!**/testdata/**')

exit "$missing"
