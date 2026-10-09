#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Fails if a built binary carries the path of the machine that built it: a
# home directory on a Mac (/Users/NAME/, or NAME in a folder name such as
# -Users-NAME-) or a CI runner's (/home/runner/).
# Go builds use -trimpath and Swift release builds remap their source paths;
# this checks they did.
#
#   scripts/check-build-paths.sh FILE...
set -euo pipefail
bad=0
for f in "$@"; do
	[[ -f $f ]] || { echo "no such file: $f" >&2; bad=1; continue; }
	n=$(LC_ALL=C grep -acE '[/-]Users[/-][A-Za-z0-9._]+[/-]|/home/runner/' "$f" || true)
	if [[ $n != 0 ]]; then
		echo "$f: carries a build machine's paths ($n lines); build with -trimpath (Go) or the Makefile's path remap (Swift)" >&2
		bad=1
	fi
done
exit "$bad"
