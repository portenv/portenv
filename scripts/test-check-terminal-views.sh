#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Tests scripts/check-terminal-views.sh on planted files: each bare
# construction fails, the factory and plain mentions pass.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
mkdir -p "$dir/PortenvKit" "$dir/Portenv"
printf 'let v = LocalProcessTerminalView(frame: .zero)\n' >"$dir/PortenvKit/TerminalGuard.swift"
check() { scripts/check-terminal-views.sh "$dir" >/dev/null 2>&1; }

printf 'final class PaddedTerminal: NSView {\n    let terminal: LocalProcessTerminalView\n    // LocalProcessTerminalView(frame:) is made by TerminalViews.\n}\n' >"$dir/Portenv/Ok.swift"
check || { echo "FAIL: the factory, a type annotation and a comment were refused" >&2; exit 1; }

for planted in \
	'let t = LocalProcessTerminalView(frame: .zero)' \
	'let t = TerminalView(frame: r)' \
	'let t = SwiftTerm.TerminalView(frame: r)' \
	'let t = MarkedTerminalView(frame: .zero)' \
	'let t: LocalProcessTerminalView = LocalProcessTerminalView.init(frame: .zero)' \
	'views.append(TerminalView(frame: r))'; do
	printf '%s\n' "$planted" >"$dir/Portenv/Bad.swift"
	if check; then
		echo "FAIL: not caught: $planted" >&2
		exit 1
	fi
done
rm "$dir/Portenv/Bad.swift"
echo "check-terminal-views: ok"
