#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# An in-place app update, as Sparkle does it (R-0198): the bundle is
# replaced where it is, the app is opened again, and the running portenvd,
# still the old binary, is restarted from the new bundle. No logout.
#
#   tests/e2e/app-update-in-place.sh LANE APP   (APP: that lane's installed Portenv.app)
#
# A dev lane's app only (its own login item): never the release identity,
# never another lane's. Builds the lane's app twice (gmake app LANE=…), each
# with its own build ID, and checks portenvd follows each one.
set -euo pipefail
lane=${1:?usage: app-update-in-place.sh LANE APP}
app=${2:?usage: app-update-in-place.sh LANE APP}
repo=$(cd "$(dirname "$0")/../.." && pwd)
pb=/usr/libexec/PlistBuddy
inst=$($pb -c "Print :PortenvInstance" "$app/Contents/Info.plist" 2>/dev/null) || { echo "$app isn't a dev lane's app: refusing" >&2; exit 1; }
[[ $inst == "dev-$lane" ]] || { echo "$app is $inst, not dev-$lane: refusing" >&2; exit 1; }
bundle_id=$($pb -c "Print :CFBundleIdentifier" "$app/Contents/Info.plist")
cli=$app/Contents/Helpers/portenv
failures=0
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# The lane's own login item's portenvd, as launchd runs it.
daemon_pid() { launchctl print "gui/$(id -u)/com.portenv.portenvd.$inst" 2>/dev/null | awk '/^\tpid =/{print $3}'; }

update_once() {
	local n=$1
	(cd "$repo" && gmake app LANE="$lane" >/dev/null)
	local new_build; new_build=$($pb -c "Print :PortenvBuildID" "$repo/bin/Portenv.app/Contents/Info.plist")
	local before_pid; before_pid=$(daemon_pid || true)
	local before_build; before_build=$("$cli" app daemon-build 2>/dev/null || true)
	osascript -e "quit app id \"$bundle_id\"" 2>/dev/null || true
	sleep 2
	# Replaced in place: the new bundle beside it, then swapped in.
	rm -rf "$app.new" "$app.old"
	cp -R "$repo/bin/Portenv.app" "$app.new"
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
update_once 2
osascript -e "quit app id \"$bundle_id\"" 2>/dev/null || true
[[ $failures -eq 0 ]] && echo "app-update-in-place: ok" || { echo "app-update-in-place: $failures failed" >&2; exit 1; }
