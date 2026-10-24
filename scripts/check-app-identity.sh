#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# check-app-identity.sh APP [INSTANCE [SIGN_ID]]: fails unless Portenv.app
# carries exactly the release identity (no INSTANCE) or a dev lane's
# (INSTANCE=dev-X): bundle ID, PortenvInstance, and one launch agent with the
# matching file name, label, associated bundle ID and PORTENV_INSTANCE, and Go
# helpers stamped with the same instance (or none).
#
# Signing (R-0328): both kinds of dev build pass. Without SIGN_ID nothing is
# asked of the helpers' signatures (ad-hoc, as gmake app signs by default; a
# release is signed afterwards by release-local.sh). With SIGN_ID (gmake app
# LANE=X DEV_SIGN_ID=…, a dev lane only) every helper must carry a valid
# signature that isn't ad-hoc, by that identity when SIGN_ID is its name.
set -euo pipefail
app=$1 inst=${2:-} sign_id=${3:-}
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
# The Go helpers carry the same stamp (-ldflags), shown by their version.
for h in portenv portenvd; do
	v=$("$app/Contents/Helpers/$h" version)
	if [[ -n $inst ]]; then
		[[ $v == *" [$inst]" ]] || fail "$h's version '$v' doesn't carry [$inst]"
	else
		[[ $v != *"["* ]] || fail "$h's version '$v' carries a stamp in a release build"
	fi
done
# The dev-only attached terminal (ADR 0020, decision 12) never reaches a
# release build: neither its Info.plist marker nor its binary's.
if [[ -z $inst ]]; then
	marker=$($pb -c "Print :PortenvAttachedTerminal" "$info" 2>/dev/null || true)
	[[ -z $marker ]] || fail "a release build carries the dev-only attached terminal (PortenvAttachedTerminal)"
	if grep -qa PORTENV_ATTACHED_TERMINAL_BUILD "$app/Contents/MacOS/Portenv"; then
		fail "a release build's binary was built with ATTACHED_TERMINAL"
	fi
fi
if [[ -n $sign_id ]]; then
	[[ -n $inst ]] || fail "a release build is never signed with a dev identity ('$sign_id')"
	for h in portenv portenvd restic; do
		f=$app/Contents/Helpers/$h
		codesign --verify --strict "$f" 2>/dev/null || fail "$h's signature doesn't verify"
		d=$(codesign -d --verbose=2 "$f" 2>&1) || fail "$h has no signature"
		! grep -qx 'Signature=adhoc' <<<"$d" || fail "$h is signed ad-hoc, not with '$sign_id'"
		# A SHA-1 hash names the certificate too; only a name can be compared
		# with the signature's Authority line.
		if ! [[ $sign_id =~ ^[0-9A-Fa-f]{40}$ ]]; then
			grep -qxF "Authority=$sign_id" <<<"$d" || fail "$h isn't signed by '$sign_id'"
		fi
	done
fi
echo "identity ok: $id, $label${inst:+ ($inst)}${sign_id:+, signed by $sign_id}"
