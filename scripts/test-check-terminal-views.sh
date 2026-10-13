#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Tests scripts/check-terminal-views.sh on planted files: each bare
# construction fails, the factory and plain mentions pass.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
mkdir -p "$dir/PortenvKit" "$dir/PortenvTerm" "$dir/Portenv"
printf 'let v = LocalProcessTerminalView(frame: .zero)\nlet t = MarkedTerminalView(frame: .zero)\n' >"$dir/PortenvTerm/TerminalViews.swift"
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
	for where in Portenv PortenvTerm PortenvKit; do
		printf '%s\n' "$planted" >"$dir/$where/Bad.swift"
		if check; then
			echo "FAIL: not caught in $where: $planted" >&2
			exit 1
		fi
		rm "$dir/$where/Bad.swift"
	done
done
# A delegate set anywhere but the factory and the tab view is refused;
# comparing it isn't.
printf 'if view.terminalDelegate !== relay {}\nlet same = v.terminalDelegate === g\n' >"$dir/Portenv/Compare.swift"
check || { echo "FAIL: comparing terminalDelegate was refused" >&2; exit 1; }
for planted in 'view.terminalDelegate = spy' 'v.terminalDelegate=nil' 'terminalDelegate = other'; do
	for where in Portenv PortenvTerm PortenvKit; do
		printf '%s\n' "$planted" >"$dir/$where/Bad.swift"
		if check; then
			echo "FAIL: delegate not caught in $where: $planted" >&2
			exit 1
		fi
		rm "$dir/$where/Bad.swift"
	done
done
printf 'terminalDelegate = sizes\n' >"$dir/PortenvTerm/MarkedTerminalView.swift"
check || { echo "FAIL: the tab view's own delegate was refused" >&2; exit 1; }
printf 'view.terminalDelegate = guardian\n' >>"$dir/PortenvTerm/TerminalViews.swift"
check || { echo "FAIL: the factory's delegate was refused" >&2; exit 1; }
# The old factory file is no exception any more.
printf 'let v = LocalProcessTerminalView(frame: .zero)\n' >"$dir/PortenvKit/TerminalGuard.swift"
if check; then
	echo "FAIL: a construction in PortenvKit/TerminalGuard.swift wasn't caught" >&2
	exit 1
fi
rm "$dir/PortenvKit/TerminalGuard.swift"
echo "check-terminal-views: ok"
