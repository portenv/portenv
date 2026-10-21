#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# A dev lane's stable signing (R-0328, R-0401): gmake app signs ad-hoc
# (codesign --sign -) unless a dev lane's build names DEV_SIGN_ID, when the
# helpers and the app are signed with that identity instead. A release build
# (no LANE, as release-local.sh makes it) ignores DEV_SIGN_ID, and SwiftPM
# never sees it. Checked with gmake -n (nothing is built or signed), and
# check-app-identity.sh's half on a planted, ad-hoc signed app (macOS only;
# ad-hoc signing reads no keychain). Nothing here signs with a real
# certificate: that would read its key from the keychain.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
make=${MAKE:-gmake}
fail() { echo "FAIL: $*" >&2; exit 1; }
id='Portenv Dev (test-host)'

# dry ARGS...: gmake -n app's commands, with DEV_SIGN_ID from the caller's
# environment only (never this shell's).
dry() { "$make" -n app "$@" 2>&1; }
signs() { grep -c -- "$1" <<<"$2" || true; }

# Unset, or empty: ad-hoc exactly as before, for a lane and a release.
for lane in Z ""; do
	out=$(env -u DEV_SIGN_ID "$make" -n app LANE="$lane" 2>&1) || fail "gmake -n app LANE=$lane: $out"
	[[ $(signs "codesign --force --sign - " "$out") == 2 ]] || fail "LANE=$lane without DEV_SIGN_ID: the helpers and the app aren't both signed ad-hoc"
	[[ $(signs "codesign --force --sign '" "$out") == 0 ]] || fail "LANE=$lane without DEV_SIGN_ID signs with an identity"
	[[ $out != *"DEV_SIGN_ID"* ]] || fail "LANE=$lane without DEV_SIGN_ID mentions it"
	out=$(DEV_SIGN_ID= dry LANE="$lane") || fail "gmake -n app LANE=$lane DEV_SIGN_ID=: $out"
	[[ $(signs "codesign --force --sign - " "$out") == 2 ]] || fail "LANE=$lane with an empty DEV_SIGN_ID doesn't sign ad-hoc"
done

# Set, for a lane: from the environment and from the command line alike.
for how in env arg; do
	if [[ $how == env ]]; then out=$(DEV_SIGN_ID=$id dry LANE=Z) || fail "gmake -n app LANE=Z (DEV_SIGN_ID in the environment): $out"
	else out=$(env -u DEV_SIGN_ID "$make" -n app LANE=Z "DEV_SIGN_ID=$id" 2>&1) || fail "gmake -n app LANE=Z DEV_SIGN_ID=…: $out"; fi
	[[ $(signs "codesign --force --sign '$id' " "$out") == 2 ]] || fail "DEV_SIGN_ID ($how): the helpers and the app aren't both signed with '$id'"
	[[ $(signs "codesign --force --sign - " "$out") == 0 ]] || fail "DEV_SIGN_ID ($how): something is still signed ad-hoc"
	[[ $out == *"check-app-identity.sh"*"'dev-Z' '$id'"* ]] || fail "DEV_SIGN_ID ($how): check-app-identity.sh isn't asked to check the identity"
	[[ $out == *"Signing with $id (DEV_SIGN_ID): macOS may ask"* ]] || fail "DEV_SIGN_ID ($how): the build doesn't say a keychain prompt may come"
	# SwiftPM's builds run with env -i and never carry the identity.
	while IFS= read -r line; do
		[[ $line == *"env -i "* ]] || fail "a Swift build runs without env -i: $line"
		[[ $line != *"$id"* && $line != *DEV_SIGN_ID* ]] || fail "a Swift build carries DEV_SIGN_ID: $line"
	done < <(grep -E '(^|[ ;&])swift( |$)' <<<"$out" | grep -E 'swift (build|test|run)' || true)
done

# A release build ignores it, and says so.
out=$(DEV_SIGN_ID=$id dry LANE=) || fail "gmake -n app LANE= DEV_SIGN_ID=…: $out"
[[ $(signs "codesign --force --sign - " "$out") == 2 && $(signs "codesign --force --sign '" "$out") == 0 ]] ||
	fail "a release build used DEV_SIGN_ID"
[[ $out == *"DEV_SIGN_ID is for a dev lane build (LANE=X) only: signing ad-hoc."* ]] || fail "a release build ignored DEV_SIGN_ID without saying so"
# release-local.sh builds with LANE= (so DEV_SIGN_ID never reaches it).
grep -qF '"$make_cmd" -C "$root" app LANE=' scripts/release-local.sh || fail "release-local.sh doesn't build with LANE="

# A quote can't break out of the codesign line.
if out=$(DEV_SIGN_ID="it's" dry LANE=Z); then fail "a DEV_SIGN_ID with a single quote was taken"; fi
[[ $out == *"can't contain a single quote"* ]] || fail "the refusal of a quoted DEV_SIGN_ID didn't say why: $out"

if [[ $(uname) != Darwin ]]; then
	echo "dev signing: ok (the Makefile; check-app-identity.sh's half runs on macOS only)"
	exit 0
fi

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
pb=/usr/libexec/PlistBuddy
plant() { # plant INSTANCE: a minimal app with that identity, its helpers signed ad-hoc
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
	for h in portenv portenvd restic; do
		printf '#!/bin/sh\necho "%s 1.0%s"\n' "$h" "${inst:+ [$inst]}" >"$app/Contents/Helpers/$h"
		chmod +x "$app/Contents/Helpers/$h"
		codesign --force --sign - "$app/Contents/Helpers/$h" 2>/dev/null
	done
	printf 'a binary\n' >"$app/Contents/MacOS/Portenv"
	echo "$app"
}
check() { scripts/check-app-identity.sh "$@" >/dev/null 2>&1; }

app=$(plant dev-Z)
check "$app" dev-Z || fail "a lane's ad-hoc signed app was refused without DEV_SIGN_ID"
check "$app" dev-Z "" || fail "a lane's ad-hoc signed app was refused with an empty DEV_SIGN_ID"
check "$app" dev-Z "$id" && fail "a lane's ad-hoc signed app passed as signed by '$id'"
check "$app" dev-Z 0123456789abcdef0123456789abcdef01234567 && fail "a lane's ad-hoc signed app passed as signed by a hash"
app=$(plant "")
check "$app" || fail "a release app was refused"
check "$app" "" "$id" && fail "a release app passed with a dev signing identity"
echo "dev signing: ok"
