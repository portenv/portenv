#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The dev-only attached terminal (ADR 0020, decision 12) never reaches a
# release build: the Makefile refuses ATTACHED_TERMINAL without LANE, and
# check-app-identity.sh refuses a release app with its Info.plist marker or
# its binary's string. Checked on a planted app.
#
# check-app-identity.sh is macOS-only by nature (PlistBuddy, codesign'd
# helpers), so its half runs on macOS (gmake test here, and CI's macOS job);
# the Makefile and release-local.sh halves run everywhere.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
make=${MAKE:-gmake}
fail() { echo "FAIL: $*" >&2; exit 1; }

if out=$("$make" -n app ATTACHED_TERMINAL=1 LANE= 2>&1); then
	fail "the Makefile built ATTACHED_TERMINAL without LANE"
fi
[[ $out == *"for a dev lane's build only"* ]] || fail "the Makefile's refusal didn't say why: $out"
out=$("$make" -n app ATTACHED_TERMINAL=1 LANE=Z 2>&1) || fail "the Makefile refused a lane's ATTACHED_TERMINAL build: $out"
[[ $out == *"-DPORTENV_ATTACHED_TERMINAL"* && $out == *"PortenvAttachedTerminal"* ]] ||
	fail "a lane's ATTACHED_TERMINAL build doesn't set the condition and the marker"
out=$("$make" -n app LANE=Z 2>&1) || fail "gmake -n app LANE=Z: $out"
[[ $out != *"PORTENV_ATTACHED_TERMINAL"* && $out != *"PortenvAttachedTerminal"* ]] ||
	fail "a lane's build sets the attached terminal without ATTACHED_TERMINAL"

# release-local.sh's own guard (sourced, so no release runs), and that the
# script really calls it before it builds.
# shellcheck source=scripts/release-guards.sh
source scripts/release-guards.sh
(ATTACHED_TERMINAL=1 refuse_attached_terminal 2>/dev/null) && fail "release-local.sh's guard let ATTACHED_TERMINAL=1 through"
(ATTACHED_TERMINAL= refuse_attached_terminal) || fail "release-local.sh's guard refused an empty ATTACHED_TERMINAL"
(unset ATTACHED_TERMINAL; refuse_attached_terminal) || fail "release-local.sh's guard refused a release without ATTACHED_TERMINAL"
guard=$(grep -n '^refuse_attached_terminal || die' scripts/release-local.sh | cut -d: -f1 || true)
build=$(grep -n '"$make_cmd" -C "$root" app' scripts/release-local.sh | cut -d: -f1 || true)
[[ -n $guard && -n $build && $guard -lt $build ]] || fail "release-local.sh doesn't call refuse_attached_terminal before it builds"

if [[ $(uname) != Darwin ]]; then
	echo "attached terminal guard: ok (the Makefile and release-local.sh; check-app-identity.sh's half runs on macOS only)"
	exit 0
fi

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
pb=/usr/libexec/PlistBuddy
plant() { # plant INSTANCE: a minimal app with that identity
	local inst=$1 app=$dir/Portenv.app id=com.portenv.Portenv label=com.portenv.portenvd
	[[ -n $inst ]] && id=$id.$inst label=$label.$inst
	rm -rf "$app"
	mkdir -p "$app/Contents/MacOS" "$app/Contents/Helpers" "$app/Contents/Library/LaunchAgents"
	$pb -c "Add :CFBundleIdentifier string $id" "$app/Contents/Info.plist" >/dev/null
	[[ -n $inst ]] && $pb -c "Add :PortenvInstance string $inst" "$app/Contents/Info.plist" >/dev/null
	local agent=$app/Contents/Library/LaunchAgents/$label.plist
	$pb -c "Add :Label string $label" -c "Add :AssociatedBundleIdentifiers array" \
		-c "Add :AssociatedBundleIdentifiers:0 string $id" "$agent" >/dev/null
	[[ -n $inst ]] && $pb -c "Add :EnvironmentVariables:PORTENV_INSTANCE string $inst" "$agent" >/dev/null
	for h in portenv portenvd; do
		printf '#!/bin/sh\necho "%s 1.0%s"\n' "$h" "${inst:+ [$inst]}" >"$app/Contents/Helpers/$h"
		chmod +x "$app/Contents/Helpers/$h"
	done
	printf 'a binary\n' >"$app/Contents/MacOS/Portenv"
	echo "$app"
}
check() { scripts/check-app-identity.sh "$@" >/dev/null 2>&1; }

app=$(plant "")
check "$app" || fail "a plain release app was refused"
$pb -c "Add :PortenvAttachedTerminal bool true" "$app/Contents/Info.plist"
check "$app" && fail "a release app with the PortenvAttachedTerminal marker passed"
app=$(plant "")
printf 'x PORTENV_ATTACHED_TERMINAL_BUILD y\n' >>"$app/Contents/MacOS/Portenv"
check "$app" && fail "a release app whose binary was built with ATTACHED_TERMINAL passed"
app=$(plant dev-Z)
$pb -c "Add :PortenvAttachedTerminal bool true" "$app/Contents/Info.plist"
printf 'PORTENV_ATTACHED_TERMINAL_BUILD\n' >>"$app/Contents/MacOS/Portenv"
check "$app" dev-Z || fail "a lane's app with the attached terminal was refused"
echo "attached terminal guard: ok"
