#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# An in-place app update, as Sparkle does it (R-0198): the bundle is
# replaced where it is, the app is opened again, and the running portenvd,
# still the old binary, is restarted from the new bundle. No logout.
#
#   tests/e2e/app-update-in-place.sh LANE APP      (APP: that lane's installed Portenv.app)
#   tests/e2e/app-update-in-place.sh release APP NEW-APP   (the signed-build check: NEW-APP replaces APP)
#
# A lane's run uses only that lane's own login item, never another's. It
# builds the lane's app twice (gmake app LANE=…), each with its own build
# ID, and checks portenvd follows each one. Ad-hoc builds are refused by
# launchd after a replace (exit 78, PLAN.md 1.8), so the check that gates
# the tester build is the release one, on the owner's Mac, with a signed
# NEW-APP (R-0203).
set -euo pipefail
lane=${1:?usage: app-update-in-place.sh LANE APP}
app=${2:?usage: app-update-in-place.sh LANE APP}
repo=$(cd "$(dirname "$0")/../.." && pwd)
pb=/usr/libexec/PlistBuddy
if [[ $lane == release ]]; then
	signed=${3:?usage: app-update-in-place.sh release APP NEW-APP}
	! $pb -c "Print :PortenvInstance" "$app/Contents/Info.plist" >/dev/null 2>&1 || { echo "$app is a dev lane's app, not the release: refusing" >&2; exit 1; }
	label=com.portenv.portenvd
else
	inst=$($pb -c "Print :PortenvInstance" "$app/Contents/Info.plist" 2>/dev/null) || { echo "$app isn't a dev lane's app: refusing" >&2; exit 1; }
	[[ $inst == "dev-$lane" ]] || { echo "$app is $inst, not dev-$lane: refusing" >&2; exit 1; }
	label=com.portenv.portenvd.$inst
fi
bundle_id=$($pb -c "Print :CFBundleIdentifier" "$app/Contents/Info.plist")
cli=$app/Contents/Helpers/portenv
failures=0
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# The lane's own login item's portenvd, as launchd runs it.
daemon_pid() { launchctl print "gui/$(id -u)/$label" 2>/dev/null | awk '/^\tpid =/{print $3}'; }

update_once() {
	local n=$1
	local new_app=$repo/bin/Portenv.app
	if [[ $lane == release ]]; then new_app=$signed; else (cd "$repo" && gmake app LANE="$lane" >/dev/null); fi
	local new_build; new_build=$($pb -c "Print :PortenvBuildID" "$new_app/Contents/Info.plist")
	local before_pid; before_pid=$(daemon_pid || true)
	local before_build; before_build=$("$cli" app daemon-build 2>/dev/null || true)
	osascript -e "quit app id \"$bundle_id\"" 2>/dev/null || true
	sleep 2
	# Replaced in place: the new bundle beside it, then swapped in.
	rm -rf "$app.new" "$app.old"
	cp -R "$new_app" "$app.new"
	mv "$app" "$app.old" && mv "$app.new" "$app" && rm -rf "$app.old"
	open "$app"
	local got=""
	for _ in $(seq 60); do
		got=$("$cli" app daemon-build 2>/dev/null || true)
		[[ $got == "$new_build" ]] && break
		sleep 0.5
	done
	local after_pid; after_pid=$(daemon_pid || true)
	echo "  update $n: build ${before_build:-<none>} (pid ${before_pid:-none}) -> ${got:-<none>} (pid ${after_pid:-none}); the app's is $new_build"
	if [[ $got == "$new_build" ]]; then pass "update $n: portenvd runs the new build"; else fail "update $n: portenvd still runs ${got:-nothing}"; fi
	if [[ -n $after_pid && $after_pid != "$before_pid" ]]; then pass "update $n: portenvd restarted"; else fail "update $n: portenvd wasn't restarted"; fi
}

update_once 1
[[ $lane == release ]] || update_once 2
osascript -e "quit app id \"$bundle_id\"" 2>/dev/null || true
if [[ $failures -ne 0 ]]; then
	echo "app-update-in-place: $failures failed" >&2
	exit 1
fi
echo "app-update-in-place: ok"
