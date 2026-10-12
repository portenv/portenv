#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# release-local.sh: builds a signed, notarized and stapled Portenv.dmg on the
# owner's Mac, for design testers (R-0089; docs/PLAN.md, 1.8). CI signing waits
# for the public release; the credentials live only in the owner's login
# keychain, and this script never prints or writes them.
#
#   scripts/release-local.sh            build, sign, notarize, staple, verify
#   scripts/release-local.sh --check    only the preflight (identity, notary profile)
#   scripts/release-local.sh --skip-notarize
#       TESTING ONLY: no notarization. The result is not distributable, and the
#       script says so and exits 2. With PORTENV_SIGN_IDENTITY=- it signs
#       ad-hoc, to test the signing, packaging and verification path on a Mac
#       without a Developer ID.
#
# Environment:
#   PORTENV_SIGN_IDENTITY   the signing identity (SHA-1 hash or full common
#                           name); default: the one "Developer ID Application:"
#                           identity in the login keychain
#   PORTENV_NOTARY_PROFILE  notarytool keychain profile (default portenv-notary)
#   MAKE                    GNU Make 4 or later (default gmake)
#
# The build is the Makefile's own (gmake app, no LANE). The result goes to
# ~/Library/Caches/Portenv/release/<version>-<build>/ (build: the commit
# count, stamped with the commit into the built copy only), outside the repository.
# Exit status: 0 distributable; 1 failed; 2 built for testing, not distributable.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
make_cmd=${MAKE:-gmake}
profile=${PORTENV_NOTARY_PROFILE:-portenv-notary}
ent=$root/apps/mac/Portenv.entitlements

check_only=0 skip_notarize=0
for arg in "$@"; do
	case $arg in
		--check) check_only=1 ;;
		--skip-notarize) skip_notarize=1 ;;
		-h | --help) sed -n '3,27p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "release-local: unknown argument $arg (see --help)" >&2; exit 1 ;;
	esac
done

die() { echo "release-local: $*" >&2; exit 1; }
# json_field FILE KEY: a top-level string field of notarytool's JSON output
# (the whole file, or else its last JSON line); empty if absent.
json_field() {
	python3 -c '
import json, sys
text = open(sys.argv[1]).read()
try:
    doc = json.loads(text)
except ValueError:
    lines = [l for l in text.splitlines() if l.strip().startswith("{")]
    doc = json.loads(lines[-1]) if lines else {}
v = doc.get(sys.argv[2], "") if isinstance(doc, dict) else ""
print(v if isinstance(v, str) else "")
' "$1" "$2" 2>/dev/null || true
}
# Any failure exits 1, never 2: 2 means only "built for testing".
test_build_done=0
trap 'rc=$?; if [[ $rc != 0 && $test_build_done == 0 ]]; then [[ $rc == 1 ]] || echo "release-local: failed (exit $rc)" >&2; exit 1; fi' EXIT
step() { printf '\n==> %s\n' "$*"; }

# --- Preflight -------------------------------------------------------------

step "Preflight"
[[ $(uname -s) == Darwin ]] || die "this runs on a Mac"

# The signing identity: its SHA-1 hash (unambiguous for codesign) and name.
adhoc=0 sign_id='' sign_name=''
idents=$(security find-identity -v -p codesigning)
if [[ ${PORTENV_SIGN_IDENTITY+set} == set && $PORTENV_SIGN_IDENTITY == - ]]; then
	[[ $skip_notarize == 1 ]] || die "PORTENV_SIGN_IDENTITY=- (ad-hoc) is for testing only and needs --skip-notarize"
	adhoc=1 sign_id=- sign_name="ad-hoc (testing only)"
else
	if [[ -n ${PORTENV_SIGN_IDENTITY:-} ]]; then
		want=$PORTENV_SIGN_IDENTITY
		match=$(awk -v w="$want" '$1 ~ /^[0-9]+\)$/ { n = $0; sub(/^[^"]*"/, "", n); sub(/"$/, "", n); if ($2 == w || n == w) print }' <<<"$idents")
		[[ -n $match ]] || die "PORTENV_SIGN_IDENTITY: no valid signing identity '$want' in your keychains (security find-identity -v -p codesigning)"
	else
		match=$(grep '"Developer ID Application: ' <<<"$idents" || true)
		[[ -n $match ]] || die "No Developer ID Application certificate in your login keychain. Owner step: Xcode › Settings › Accounts › your team › Manage Certificates › + › Developer ID Application."
	fi
	if [[ $(wc -l <<<"$match") -gt 1 ]]; then
		echo "release-local: more than one matching identity:" >&2
		awk '{ sub(/^ *[0-9]+\) /, "  "); print }' <<<"$match" >&2
		die "set PORTENV_SIGN_IDENTITY to one of them (its hash or full name)"
	fi
	sign_id=$(awk '{ print $2 }' <<<"$match")
	sign_name=$(sed -E 's/^[^"]*"(.*)"$/\1/' <<<"$match")
	[[ $sign_name == "Developer ID Application: "* ]] || die "'$sign_name' isn't a Developer ID Application identity: only that one is valid outside the App Store"
fi
echo "signing identity: $sign_name"

if [[ $skip_notarize == 1 ]]; then
	echo "notarization: SKIPPED (--skip-notarize, testing only)"
else
	if ! err=$(xcrun notarytool history --keychain-profile "$profile" --output-format json 2>&1 >/dev/null); then
		[[ -z $err ]] || awk '{ print "  notarytool: " $0 }' <<<"$err" >&2
		die "the notary profile '$profile' doesn't work. Owner step: xcrun notarytool store-credentials $profile --key <AuthKey_….p8> --key-id <Key ID> --issuer <Issuer ID>, in your own Terminal"
	fi
	echo "notary profile: $profile works"
fi

info=$root/apps/mac/Info.plist
pb=/usr/libexec/PlistBuddy
version=$($pb -c "Print :CFBundleShortVersionString" "$info")
# Tester builds are told apart by their commit: the built copy's
# CFBundleVersion is the commit count and PortenvCommit the short SHA (the
# source Info.plist is never changed).
build=$(git -C "$root" rev-list --count HEAD)
commit=$(git -C "$root" rev-parse --short HEAD)
echo "version: $version ($build, commit $commit)"

testing=$((adhoc || skip_notarize))
if [[ -n $(git -C "$root" status --porcelain) ]]; then
	[[ $testing == 1 ]] || die "the working tree isn't clean (git status --porcelain, untracked files included): a tester build comes from a commit and nothing else"
	echo "warning: the working tree isn't clean (allowed for a test build only)"
fi
command -v "$make_cmd" >/dev/null || die "$make_cmd not found: GNU Make 4 or later (brew install make)"

if [[ $check_only == 1 ]]; then
	if [[ $testing == 1 ]]; then echo "preflight ok (testing only: the result wouldn't be distributable)"; else echo "preflight ok"; fi
	exit 0
fi

# --- Build -----------------------------------------------------------------

step "Building Portenv.app (gmake app, release identity)"
"$make_cmd" -C "$root" app LANE=

out=$HOME/Library/Caches/Portenv/release/$version-$build
[[ $testing == 1 ]] && out=$out-test
rm -rf "$out"
mkdir -p "$out"
app=$out/Portenv.app
ditto "$root/bin/Portenv.app" "$app"
$pb -c "Set :CFBundleVersion $build" "$app/Contents/Info.plist"
$pb -c "Add :PortenvCommit string $commit" "$app/Contents/Info.plist"
"$root/scripts/check-app-identity.sh" "$app"
# Third-party notices (R-0096): every Go module in the helpers and every
# Swift pin has its licence in the app (gmake app writes them).
(cd "$root" && go run ./core/tools/notices check --app "$app" --resolved apps/mac/Package.resolved) ||
	die "the app's third-party notices don't cover its helpers and Swift packages: gmake app writes them"

# --- Sign, inside out ------------------------------------------------------

if [[ $adhoc == 1 ]]; then ts=(--timestamp=none); else ts=(--timestamp); fi
sign_code() {
	codesign --force --sign "$sign_id" --options runtime "${ts[@]}" --entitlements "$ent" "$1"
	echo "signed: ${1#"$out"/}"
}
is_macho() { [[ $(file -b "$1") == Mach-O* ]]; }
depth_first() { awk -F/ '{ print NF "\t" $0 }' | sort -rn | cut -f2-; }

step "Signing ($sign_name)"
signed=()
# The helpers, exactly the expected three.
helpers=$(cd "$app/Contents/Helpers" && ls)
[[ $helpers == $'portenv\nportenvd\nrestic' ]] || die "Contents/Helpers holds '$(tr '\n' ' ' <<<"$helpers")'; expected portenv, portenvd and restic: update the signing order"
for h in portenv portenvd restic; do
	sign_code "$app/Contents/Helpers/$h"
	signed+=("$app/Contents/Helpers/$h")
done
# Any other code (in resource bundles or frameworks), deepest first, then the
# bundles that hold it.
while IFS= read -r f; do
	[[ -n $f ]] || continue
	is_macho "$f" || continue
	sign_code "$f"
	signed+=("$f")
done < <(find "$app/Contents" -type f ! -path "$app/Contents/MacOS/Portenv" ! -path "$app/Contents/Helpers/*" | depth_first)
while IFS= read -r b; do
	[[ -n $b ]] || continue
	code=0
	while IFS= read -r f; do if is_macho "$f"; then code=1; fi; done < <(find "$b" -type f)
	[[ $code == 1 ]] || continue
	sign_code "$b"
	signed+=("$b")
done < <(find "$app/Contents" -mindepth 2 -type d \( -name '*.bundle' -o -name '*.framework' -o -name '*.app' -o -name '*.xpc' \) | depth_first)
sign_code "$app"
signed+=("$app")

# Every signature carries the hardened runtime, and (Developer ID) a secure
# timestamp and the identity asked for.
for s in "${signed[@]}"; do
	d=$(codesign -d --verbose=2 "$s" 2>&1)
	grep -q '^CodeDirectory.*flags=.*runtime' <<<"$d" || die "${s#"$out"/}: no hardened runtime"
	if [[ $adhoc == 0 ]]; then
		grep -q '^Timestamp=' <<<"$d" || die "${s#"$out"/}: no secure timestamp"
		grep -qxF "Authority=$sign_name" <<<"$d" || die "${s#"$out"/}: not signed by $sign_name"
	fi
done
echo "hardened runtime on all ${#signed[@]} signatures"

# --- Notarize --------------------------------------------------------------

# notarize FILE: submits FILE and waits; on anything but Accepted, prints the
# notary log for the submission and fails.
notarize() {
	local file=$1 json rc=0 id status
	json=$out/notary-$(basename "$file").json
	echo "submitting $(basename "$file") and waiting (usually a few minutes)"
	xcrun notarytool submit "$file" --keychain-profile "$profile" --wait --output-format json >"$json" || rc=$?
	id=$(json_field "$json" id)
	status=$(json_field "$json" status)
	echo "submission ${id:-?}: ${status:-no status}"
	if [[ $rc != 0 || $status != Accepted ]]; then
		if [[ -n $id ]]; then
			echo "notary log for $id:" >&2
			xcrun notarytool log "$id" --keychain-profile "$profile" --output-format json >&2 || true
		fi
		die "notarization of $(basename "$file") failed (status '${status:-unknown}', notarytool exit $rc)"
	fi
}

if [[ $skip_notarize == 0 ]]; then
	# The app is notarized and stapled on its own too, so a tester who copies
	# it out of the DMG can open it offline.
	step "Notarizing Portenv.app"
	ditto -c -k --keepParent "$app" "$out/Portenv-app.zip"
	notarize "$out/Portenv-app.zip"
	rm -f "$out/Portenv-app.zip"
	xcrun stapler staple "$app"
fi

# --- Package ---------------------------------------------------------------

step "Packaging Portenv.dmg"
dmg=$out/Portenv.dmg
stage=$out/dmg-root
mkdir -p "$stage"
ditto "$app" "$stage/Portenv.app"
ln -s /Applications "$stage/Applications"
hdiutil create -volname Portenv -srcfolder "$stage" -format UDZO -ov "$dmg"
rm -rf "$stage"
codesign --force --sign "$sign_id" "${ts[@]}" "$dmg"
echo "signed: Portenv.dmg"

if [[ $skip_notarize == 0 ]]; then
	step "Notarizing Portenv.dmg"
	notarize "$dmg"
	xcrun stapler staple "$dmg"
fi

# --- Verify ----------------------------------------------------------------

step "Verifying"
failed=()
check() {
	local name=$1
	shift
	echo "\$ $*"
	if "$@" 2>&1 | sed 's/^/  /'; then
		echo "  -> ok"
	else
		echo "  -> FAILED"
		failed+=("$name")
	fi
}
check "codesign (app)" codesign --verify --deep --strict --verbose=2 "$app"
check "Gatekeeper (app)" spctl -a -vvv -t exec "$app"
check "Gatekeeper (DMG, install)" spctl -a -vvv -t install "$dmg"
check "Gatekeeper (DMG, open)" spctl -a -vvv -t open --context context:primary-signature "$dmg"
check "stapled ticket (app)" xcrun stapler validate "$app"
check "stapled ticket (DMG)" xcrun stapler validate "$dmg"

sum=$(shasum -a 256 "$dmg")
sum=${sum%% *}
printf '%s  Portenv.dmg\n' "$sum" >"$dmg.sha256"

echo
echo "output: $out"
echo "SHA-256 (Portenv.dmg): $sum"
if [[ $testing == 1 ]]; then
	echo
	why="not notarized"
	[[ $adhoc == 1 ]] && why="ad-hoc signature, $why"
	echo "NOT DISTRIBUTABLE: test build ($why). Don't give it to anyone."
	[[ ${#failed[@]} == 0 ]] || echo "failed checks (expected for a test build): ${failed[*]}"
	test_build_done=1
	exit 2
fi
[[ ${#failed[@]} == 0 ]] || die "verification failed: ${failed[*]}. Don't distribute this DMG."
echo "Portenv $version ($build, commit $commit) is signed, notarized and stapled: ready for design testers."
