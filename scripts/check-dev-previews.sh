#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# check-dev-previews.sh: the dev-only previews (Portenv --blocks-preview,
# R-0220) never reach a release build.
#   check-dev-previews.sh [ROOT]          sources: every mention of the
#       preview sits inside `#if PORTENV_DEV_PREVIEWS` (make lint)
#   check-dev-previews.sh --binary APP    a built app's binary holds no
#       trace of it (make app, for a release build)
set -euo pipefail

if [[ ${1:-} == --binary ]]; then
	bin=${2:?the path to the app binary}
	if grep -aq -e 'blocks-preview' -e 'BlocksPreview' "$bin"; then
		echo "$bin carries the dev-only blocks preview: a release build must not (R-0220)" >&2
		exit 1
	fi
	exit 0
fi

root=${1:-$(cd "$(dirname "$0")/.." && pwd)}
bad=0
while IFS= read -r f; do
	# Lines that mention the preview outside an #if PORTENV_DEV_PREVIEWS
	# block (nested #if/#endif counted).
	out=$(awk '
		/^[[:space:]]*#if[[:space:]]+PORTENV_DEV_PREVIEWS/ { if (!depth) inside = 1; if (inside) depth++; next }
		/^[[:space:]]*#if/ { if (inside) depth++; next }
		/^[[:space:]]*#endif/ { if (inside) { depth--; if (!depth) inside = 0 }; next }
		/blocks-preview|BlocksPreview/ { if (!inside) print FILENAME ":" FNR ": " $0 }
	' "$f")
	if [[ -n $out ]]; then
		echo "$out" >&2
		bad=1
	fi
done < <(find "$root/apps/mac/Sources" -name '*.swift')
if [[ $bad == 1 ]]; then
	echo "The blocks preview is dev-only: keep it inside #if PORTENV_DEV_PREVIEWS (R-0220)." >&2
	exit 1
fi
echo "dev previews: ok"
