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
# (empty: the network is down); logs every call in $FAKE/calls.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/xcrun" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == notarytool ]] || exit 99
cmd=$2
echo "$cmd" >>"$FAKE/calls"
id=0bc21e4c-4aef-4bca-896a-5ec3b8c841cd
case $cmd in
	submit)
		[[ -f $FAKE/upload-fails ]] && { echo "Error: HTTP status code: 500" >&2; exit 1; }
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
	info)
		[[ -s $FAKE/info ]] || { echo "Error: NSURLErrorDomain Code=-1009" >&2; exit 1; }
		echo "{\"id\":\"$id\",\"status\":\"$(cat "$FAKE/info")\"}"
		;;
	log)
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

# run NAME WAITS...: a fresh release folder with a file to notarize; runs
# notarize on it, leaving its output in $tmp/NAME.out and status in $rc.
run() {
	local name=$1
	shift
	last=$name
	export FAKE=$tmp/$name
	mkdir -p "$FAKE/out"
	: >"$FAKE/calls"
	printf '%s\n' "$@" >"$FAKE/waits"
	echo "$name" >"$FAKE/out/Portenv-app.zip"
	[[ -n ${seed:-} ]] && printf '%s\n' "$seed" >"$FAKE/out/notary-Portenv-app.zip.id"
	rc=0
	(
		out=$FAKE/out profile=test-profile
		die() { echo "release-local: $*" >&2; exit 1; }
		# shellcheck source=scripts/notarize-lib.sh
		source "$here/notarize-lib.sh"
		notarize "$out/Portenv-app.zip"
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

# The script's arguments, checked before anything else runs.
last=none
rc=0
"$here/release-local.sh" --resume >/dev/null 2>&1 || rc=$?
t "--resume without a folder is refused" [ "$rc" = 1 ]
rc=0
"$here/release-local.sh" --resume "$tmp" --check >/dev/null 2>&1 || rc=$?
t "--resume with --check is refused" [ "$rc" = 1 ]

if [[ $fail == 0 ]]; then echo "all checks passed"; else exit 1; fi
