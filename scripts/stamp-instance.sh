#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# stamp-instance.sh APP INSTANCE: gives a dev lane's Portenv.app its own
# identity (gmake app LANE=X): bundle ID com.portenv.Portenv.<instance>, a
# PortenvInstance key, and portenvd's launch agent renamed and relabelled
# com.portenv.portenvd.<instance>, with PORTENV_INSTANCE for portenvd. Release
# builds are never stamped. The caller signs the bundle afterwards.
set -euo pipefail
app=$1 inst=$2
[[ $inst =~ ^dev-[A-Z]$ ]] || { echo "stamp-instance: $inst isn't dev-<one capital letter>" >&2; exit 1; }
pb=/usr/libexec/PlistBuddy
info=$app/Contents/Info.plist
$pb -c "Set :CFBundleIdentifier com.portenv.Portenv.$inst" "$info"
$pb -c "Add :PortenvInstance string $inst" "$info"
agents=$app/Contents/Library/LaunchAgents
mv "$agents/com.portenv.portenvd.plist" "$agents/com.portenv.portenvd.$inst.plist"
plist=$agents/com.portenv.portenvd.$inst.plist
$pb -c "Set :Label com.portenv.portenvd.$inst" "$plist"
$pb -c "Set :AssociatedBundleIdentifiers:0 com.portenv.Portenv.$inst" "$plist"
$pb -c "Add :EnvironmentVariables:PORTENV_INSTANCE string $inst" "$plist"
