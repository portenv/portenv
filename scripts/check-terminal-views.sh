#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Every terminal view the app shows is made by TerminalViews, which puts
# TerminalGuard in front of it (R-0134): no program in a workspace reads or
# sets the Mac's clipboard or opens a link by itself. Fails on a terminal
# view constructed anywhere else in the app's sources.
#
#   scripts/check-terminal-views.sh [folder]   (default: apps/mac/Sources)
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
src=${1:-apps/mac/Sources}
factory=PortenvTerm/TerminalViews.swift

# A construction: Type(, Type.init(, or SwiftTerm.Type( for SwiftTerm's
# terminal views and the app's own (MarkedTerminalView).
pattern='(^|[^A-Za-z0-9_])(SwiftTerm\.)?(LocalProcessTerminalView|MarkedTerminalView|TerminalView)(\.init)?\('
found=0
while IFS= read -r -d '' f; do
	case "$f" in */"$factory") continue ;; esac
	if grep -nE "$pattern" "$f" | grep -vE '^[0-9]+:[[:space:]]*//'; then
		echo "$f: make terminal views with TerminalViews, which installs TerminalGuard" >&2
		found=1
	fi
done < <(find "$src" -name '*.swift' -print0)

# A terminal view's delegate is its guard, set where the view is made and
# nowhere else (R-0167): only the factory and the tab view itself assign it.
delegate='(^|[^A-Za-z0-9_])terminalDelegate[[:space:]]*=([^=]|$)'
while IFS= read -r -d '' f; do
	case "$f" in */"$factory" | */PortenvTerm/MarkedTerminalView.swift) continue ;; esac
	if grep -nE "$delegate" "$f" | grep -vE '^[0-9]+:[[:space:]]*//'; then
		echo "$f: only TerminalViews and MarkedTerminalView set a terminal view's delegate (its guard)" >&2
		found=1
	fi
done < <(find "$src" -name '*.swift' -print0)
exit "$found"
