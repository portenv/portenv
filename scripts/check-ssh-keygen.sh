#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# No code, test or script runs ssh-keygen on the person's own files (R-0183).
# Without -f, ssh-keygen works on ~/.ssh: `ssh-keygen -H` once hashed the
# owner's real known_hosts in place and overwrote known_hosts.old. So every
# run names its file with -f, and a run that rewrites one (-H, -R, -A)
# never names a path under ~/.ssh or $HOME: tests point HOME at a temporary
# folder and pass files of their own. Comments and plain mentions pass.
#
#   scripts/check-ssh-keygen.sh [folder]   (default: the repository's files)
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

files() {
	if [[ $# -gt 0 ]]; then
		find "$1" -type f \( -name '*.go' -o -name '*.sh' -o -name '*.bash' -o -name '*.py' -o -name '*.yml' -o -name '*.yaml' -o -name '*.swift' -o -name Makefile \) -print0
	else
		# This check and its test plant the very lines they look for.
		git ls-files -z -- '*.go' '*.sh' '*.bash' '*.py' '*.yml' '*.yaml' '*.swift' Makefile \
			':!scripts/check-ssh-keygen.sh' ':!scripts/test-check-ssh-keygen.sh'
	fi
}

# A run: "ssh-keygen", as an argument in a list, or ssh-keygen followed by
# an option, in a command line or a string.
run='ssh-keygen("[[:space:]]*,|[[:space:]]+-)'
named='(^|[[:space:]"'"'"'])-f([[:space:]"'"'"',]|$)'
rewrites='(^|[[:space:]"'"'"'])-[HRA]([[:space:]"'"'"',)]|$)'
# shellcheck disable=SC2016 # literal text to find, not to expand
home='(\.ssh|\$HOME|\$\{HOME|~/|UserHomeDir)'
found=0
while IFS= read -r -d '' f; do
	while IFS= read -r hit; do
		n=${hit%%:*}
		line=${hit#*:}
		[[ $line =~ ^[[:space:]]*(//|#) ]] && continue
		if ! [[ $line =~ $named ]]; then
			echo "$f:$n: ssh-keygen without -f works on ~/.ssh; give it a file of the test's own" >&2
			found=1
		elif [[ $line =~ $rewrites && $line =~ $home ]]; then
			echo "$f:$n: ssh-keygen -H, -R or -A rewrites a file under the person's home; use a temporary HOME and a file of its own" >&2
			found=1
		fi
	done < <(grep -nE "$run" "$f" || true)
done < <(files "$@")
exit "$found"
