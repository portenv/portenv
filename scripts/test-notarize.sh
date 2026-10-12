#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# test-notarize.sh: notarize-lib.sh against a fake xcrun (run by make test).
# A dropped connection or a slow review waits on the same submission, a saved
# submission is resumed without a second upload, and every failure says so.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The fake xcrun: answers `notarytool wait` from the queue in $FAKE/waits, one
# line per call (timeout, progress, accepted, invalid); `info` with $FAKE/info
# (empty: the network is down); `history` with $FAKE/history (default none);
# logs every call in $FAKE/calls.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/xcrun" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ $1 == stapler ]]; then
	echo "staple $3" >>"$FAKE/calls"
	[[ ! -f $FAKE/staple-fails ]] || { echo "The staple and validate action failed! Error 65." >&2; exit 65; }
	exit 0
fi
[[ $1 == notarytool ]] || exit 99
cmd=$2
echo "$cmd" >>"$FAKE/calls"
id=0bc21e4c-4aef-4bca-896a-5ec3b8c841cd
case $cmd in
	submit)
		if [[ -f $FAKE/upload-fails ]]; then
			[[ ! -f $FAKE/upload-hook ]] || bash "$FAKE/upload-hook"
			echo "Error: HTTP status code: 500" >&2
			exit 1
		fi
		shasum -a 256 <"$3" | cut -d' ' -f1 >"$FAKE/submitted-sha"
		echo "{\"id\":\"$id\",\"message\":\"Successfully uploaded file\"}"
		;;
	wait)
		next=$(head -n 1 "$FAKE/waits")
		sed -i.bak 1d "$FAKE/waits"
		case $next in
			timeout) echo 'Error: NSURLErrorDomain Code=-1001 "The request timed out."' >&2; exit 1 ;;
			progress) echo "{\"id\":\"$id\",\"status\":\"In Progress\"}" ;;
			accepted) echo "{\"id\":\"$id\",\"status\":\"Accepted\"}" ;;
			invalid) echo "{\"id\":\"$id\",\"status\":\"Invalid\"}"; exit 1 ;;
			*) exit 98 ;;
		esac
		;;
	history)
		if [[ -f $FAKE/history ]]; then cat "$FAKE/history"; else echo '{"history":[],"message":"No submissions"}'; fi
		;;
	info)
		[[ -s $FAKE/info ]] || { echo "Error: NSURLErrorDomain Code=-1009" >&2; exit 1; }
		echo "{\"id\":\"$id\",\"status\":\"$(cat "$FAKE/info")\"}"
		;;
	log)
		[[ ! -f $FAKE/log-fails ]] || { echo "Error: NSURLErrorDomain Code=-1001" >&2; exit 1; }
		echo "{\"jobId\":\"$id\",\"sha256\":\"$(cat "$FAKE/log-sha" 2>/dev/null || cat "$FAKE/submitted-sha")\",\"issues\":null}" >"${!#}"
		;;
	*) exit 97 ;;
esac
EOF
chmod +x "$tmp/bin/xcrun"

fail=0
# t DESCRIPTION COMMAND...: one check; on a failure, the run's output.
t() {
	local d=$1
	shift
	if "$@"; then
		echo "ok    $d"
	else
		echo "FAIL  $d"
		[[ -f $tmp/$last.out ]] && sed 's/^/      /' "$tmp/$last.out"
		fail=1
	fi
}

# run NAME WAITS...: a release folder with a file to notarize ($file, default
# Portenv-app.zip; made once, kept across runs of the same NAME); runs
# notarize on it (or $step, with $target), leaving its output in $tmp/NAME.out
# and status in $rc.
run() {
	local name=$1
	shift
	last=$name
	export FAKE=$tmp/$name
	mkdir -p "$FAKE/out"
	: >"$FAKE/calls"
	printf '%s\n' "$@" >"$FAKE/waits"
	local f=${file:-Portenv-app.zip}
	[[ -f $FAKE/out/$f ]] || echo "$name" >"$FAKE/out/$f"
	[[ -n ${seed:-} ]] && printf '%s\n' "$seed" >"$FAKE/out/notary-$f.id"
	rc=0
	(
		out=$FAKE/out profile=test-profile
		# shellcheck disable=SC2317 # called by the library
		die() { echo "release-local: $*" >&2; exit 1; }
		# shellcheck source=scripts/notarize-lib.sh
		source "$here/notarize-lib.sh"
		"${step:-notarize}" "$out/$f" ${target:+"$out/$target"}
	) >"$tmp/$name.out" 2>&1 || rc=$?
}
count() { grep -cx "$2" "$tmp/$1/calls" || true; }

export PATH=$tmp/bin:$PATH PORTENV_NOTARY_RETRY=0 PORTENV_NOTARY_MAX_WAIT=3600

run dropped timeout timeout progress accepted
t "a dropped connection and a slow review: accepted" [ "$rc" = 0 ]
t "  uploaded once" [ "$(count dropped submit)" = 1 ]
t "  waited on the same submission 4 times" [ "$(count dropped wait)" = 4 ]
t "  the submission is saved" grep -q '^id=0bc21e4c-' "$tmp/dropped/out/notary-Portenv-app.zip.id"

sum=$(shasum -a 256 <<<"resumed" | cut -d' ' -f1)
mkdir -p "$tmp/resumed" && echo "$sum" >"$tmp/resumed/log-sha"
seed=$(printf 'id=0bc21e4c-4aef-4bca-896a-5ec3b8c841cd\nsha256=%s' "$sum") run resumed accepted
t "a saved submission is resumed" [ "$rc" = 0 ]
t "  without uploading again" [ "$(count resumed submit)" = 0 ]

seed=$(printf 'id=0bc21e4c-4aef-4bca-896a-5ec3b8c841cd\nsha256=%064d' 0) run other accepted
t "a submission saved for another copy of the file isn't reused" [ "$rc" = 0 ]
t "  this copy is uploaded" [ "$(count other submit)" = 1 ]

run invalid timeout invalid
t "Invalid fails" [ "$rc" = 1 ]
t "  and says so" grep -q 'notarization failed (status Invalid' "$tmp/invalid.out"
t "  with the notary log kept" [ -s "$tmp/invalid/out/notary-Portenv-app.zip.log.json" ]

mkdir -p "$tmp/mismatch" && printf '%064d\n' 1 >"$tmp/mismatch/log-sha"
run mismatch accepted
t "accepted for another file: fails" [ "$rc" = 1 ]
t "  and says so" grep -q 'accepted for a file with SHA-256' "$tmp/mismatch.out"

PORTENV_NOTARY_MAX_WAIT=0 run slow progress
t "past the time limit: stops" [ "$rc" = 1 ]
t "  pointing to --resume" grep -q 'carry on later with: scripts/release-local.sh --resume' "$tmp/slow.out"
t "  keeping the saved submission" [ -f "$tmp/slow/out/notary-Portenv-app.zip.id" ]

mkdir -p "$tmp/upload" && touch "$tmp/upload/upload-fails"
run upload accepted
t "a failed upload fails" [ "$rc" = 1 ]
t "  pointing to notarytool history" grep -q 'the upload failed.*notarytool history' "$tmp/upload.out"
t "  and saves no submission" [ ! -f "$tmp/upload/out/notary-Portenv-app.zip.id" ]

# A failed staple, then --resume: the same upload is reused (R-0114).
mkdir -p "$tmp/appstaple" && touch "$tmp/appstaple/staple-fails"
step=notarize_staple target=Portenv.app run appstaple accepted
t "a failed staple of the app fails" [ "$rc" = 1 ]
t "  pointing to --resume" grep -q 'stapling failed.*--resume' "$tmp/appstaple.out"
t "  keeping the uploaded zip" [ -f "$tmp/appstaple/out/Portenv-app.zip" ]
rm "$tmp/appstaple/staple-fails"
step=notarize_staple target=Portenv.app run appstaple accepted
t "  resumed: stapled" [ "$rc" = 0 ]
t "  without uploading again" [ "$(count appstaple submit)" = 0 ]
t "  the app is stapled" [ "$(count appstaple "staple $tmp/appstaple/out/Portenv.app")" = 1 ]
t "  and the zip deleted only then" [ ! -e "$tmp/appstaple/out/Portenv-app.zip" ]

mkdir -p "$tmp/dmgstaple" && touch "$tmp/dmgstaple/staple-fails"
file=Portenv.dmg step=notarize_staple run dmgstaple accepted
t "a failed staple of the DMG fails" [ "$rc" = 1 ]
t "  keeping notary-Portenv.dmg.id" [ -f "$tmp/dmgstaple/out/notary-Portenv.dmg.id" ]
t "  and the DMG" [ -f "$tmp/dmgstaple/out/Portenv.dmg" ]
rm "$tmp/dmgstaple/staple-fails"
file=Portenv.dmg step=notarize_staple run dmgstaple accepted
t "  resumed: stapled" [ "$rc" = 0 ]
t "  without uploading again" [ "$(count dmgstaple submit)" = 0 ]

# notarytool history (R-0124). hist DIR NAME STATUS CREATED ID: one submission.
hist() {
	mkdir -p "$tmp/$1"
	printf '{"history":[{"createdDate":"%s","id":"%s","name":"%s","status":"%s"}],"message":"ok"}\n' "$4" "$5" "$2" "$3" >"$tmp/$1/history"
}
now=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
hid=0bc21e4c-4aef-4bca-896a-5ec3b8c841cd

hist adopt Portenv.dmg 'In Progress' "$now" "$hid"
mkdir -p "$tmp/adopt/out" && echo adopt >"$tmp/adopt/out/Portenv.dmg"
shasum -a 256 <"$tmp/adopt/out/Portenv.dmg" | cut -d' ' -f1 >"$tmp/adopt/log-sha"
file=Portenv.dmg run adopt progress accepted
t "a same-name submission in history, made since the file, is adopted" [ "$rc" = 0 ]
t "  without uploading" [ "$(count adopt submit)" = 0 ]
t "  saved, marked as from history" grep -qx 'from=history' "$tmp/adopt/out/notary-Portenv.dmg.id"
t "  and its SHA-256 checked in the log" [ "$(count adopt log)" = 1 ]

hist wrong Portenv.dmg Accepted "$now" "$hid"
mkdir -p "$tmp/wrong" && printf '%064d\n' 2 >"$tmp/wrong/log-sha"
file=Portenv.dmg run wrong accepted
t "an adopted submission with another SHA-256 fails" [ "$rc" = 1 ]
t "  without uploading" [ "$(count wrong submit)" = 0 ]
t "  and says so" grep -q 'accepted for a file with SHA-256.*Nothing was uploaded' "$tmp/wrong.out"
t "  forgetting it" [ ! -f "$tmp/wrong/out/notary-Portenv.dmg.id" ]
rm "$tmp/wrong/log-sha"
file=Portenv.dmg run wrong accepted
t "  the next run skips it and uploads" [ "$rc" = 0 ]
t "  once" [ "$(count wrong submit)" = 1 ]

hist nologadopt Portenv.dmg Accepted "$now" "$hid"
mkdir -p "$tmp/nologadopt/out" && echo x >"$tmp/nologadopt/out/Portenv.dmg"
touch "$tmp/nologadopt/log-fails"
file=Portenv.dmg run nologadopt accepted
t "an adopted submission whose log can't be read fails" [ "$rc" = 1 ]
t "  pointing to --resume, its ID kept" grep -q -- '--resume' "$tmp/nologadopt.out"
t "  without uploading" [ "$(count nologadopt submit)" = 0 ]

hist older Portenv.dmg Accepted 2020-01-01T00:00:00.000Z "$hid"
file=Portenv.dmg run older accepted
t "a same-name submission older than the file: uploads" [ "$(count older submit)" = 1 ]
hist othername Portenv-app.zip 'In Progress' "$now" "$hid"
file=Portenv.dmg run othername accepted
t "another file's submission: uploads" [ "$(count othername submit)" = 1 ]
hist invalidh Portenv.dmg Invalid "$now" "$hid"
file=Portenv.dmg run invalidh accepted
t "an Invalid one in history: uploads" [ "$(count invalidh submit)" = 1 ]

hist lostid Portenv.dmg 'In Progress' "$now" "$hid"
touch "$tmp/lostid/upload-fails"
mkdir -p "$tmp/lostid/out" && echo lost >"$tmp/lostid/out/Portenv.dmg"
shasum -a 256 <"$tmp/lostid/out/Portenv.dmg" | cut -d' ' -f1 >"$tmp/lostid/log-sha"
rm "$tmp/lostid/history"
# history is empty before the upload and has it after the failed submit
cat >"$tmp/lostid/upload-hook" <<EOF
printf '{"history":[{"createdDate":"$now","id":"$hid","name":"Portenv.dmg","status":"In Progress"}]}\n' >"$tmp/lostid/history"
EOF
file=Portenv.dmg run lostid accepted
t "a submit that fails after Apple got the file: adopted from history" [ "$rc" = 0 ]
t "  one upload, not two" [ "$(count lostid submit)" = 1 ]

# The script's arguments, checked before anything else runs.
last=none
rc=0
"$here/release-local.sh" --resume >/dev/null 2>&1 || rc=$?
t "--resume without a folder is refused" [ "$rc" = 1 ]
rc=0
"$here/release-local.sh" --resume "$tmp" --check >/dev/null 2>&1 || rc=$?
t "--resume with --check is refused" [ "$rc" = 1 ]

if [[ $fail == 0 ]]; then echo "all checks passed"; else exit 1; fi
