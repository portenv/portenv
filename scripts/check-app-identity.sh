#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# check-app-identity.sh APP [INSTANCE]: fails unless Portenv.app carries
# exactly the release identity (no INSTANCE) or a dev lane's (INSTANCE=dev-X):
# bundle ID, PortenvInstance, and one launch agent with the matching file
# name, label, associated bundle ID and PORTENV_INSTANCE.
set -euo pipefail
app=$1 inst=${2:-}
pb=/usr/libexec/PlistBuddy
info=$app/Contents/Info.plist
agents=$app/Contents/Library/LaunchAgents
fail() { echo "check-app-identity: $*" >&2; exit 1; }
if [[ -z $inst ]]; then id=com.portenv.Portenv label=com.portenv.portenvd; else id=com.portenv.Portenv.$inst label=com.portenv.portenvd.$inst; fi
[[ $($pb -c "Print :CFBundleIdentifier" "$info") == "$id" ]] || fail "bundle ID isn't $id"
got=$($pb -c "Print :PortenvInstance" "$info" 2>/dev/null || true)
[[ $got == "$inst" ]] || fail "PortenvInstance is '$got', want '$inst'"
files=$(cd "$agents" && ls)
[[ $files == "$label.plist" ]] || fail "launch agents are '$files', want $label.plist only"
plist=$agents/$label.plist
[[ $($pb -c "Print :Label" "$plist") == "$label" ]] || fail "launch agent label isn't $label"
[[ $($pb -c "Print :AssociatedBundleIdentifiers:0" "$plist") == "$id" ]] || fail "associated bundle ID isn't $id"
env=$($pb -c "Print :EnvironmentVariables:PORTENV_INSTANCE" "$plist" 2>/dev/null || true)
[[ $env == "$inst" ]] || fail "portenvd's PORTENV_INSTANCE is '$env', want '$inst'"
echo "identity ok: $id, $label${inst:+ ($inst)}"
