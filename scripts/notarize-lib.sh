# shellcheck shell=bash disable=SC2154 # $out and $profile are the caller's
# SPDX-License-Identifier: Apache-2.0
#
# notarize-lib.sh: notarization for release-local.sh, sourced (not run). One
# upload per file: the submission ID is saved next to the file before any
# waiting, and a dropped connection or a slow review only makes it wait
# longer on that same submission, never upload again (Apple queues a second
# submission behind the first).
#
# The caller sets $out (the release folder) and $profile (the notarytool
# keychain profile) and defines die. Tuning, for tests:
#   PORTENV_NOTARY_POLL       one `notarytool wait` call's limit (default 1h)
#   PORTENV_NOTARY_RETRY      seconds before retrying after an error (default
#                             30, doubling to 10 minutes)
#   PORTENV_NOTARY_MAX_WAIT   seconds to keep waiting before stopping, the
#                             submission kept for --resume (default 72 h)
#
# The saved submission, $out/notary-<file>.id, is two lines:
#   id=<submission UUID>
#   sha256=<the file's SHA-256>
# It's only reused for a file with that same SHA-256.

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

sha256_of() {
	local s
	s=$(shasum -a 256 <"$1")
	echo "${s%% *}"
}

uuid_re='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'

# notarize FILE: notarizes FILE, reusing its saved submission if there is one;
# returns once Apple has accepted it, and fails with the notary log otherwise.
notarize() {
	local file=$1 name idf sum id='' saved_id saved_sum json rc=0
	name=$(basename "$file")
	idf=$out/notary-$name.id
	json=$out/notary-$name.json
	sum=$(sha256_of "$file")
	if [[ -f $idf ]]; then
		saved_id=$(sed -n 's/^id=//p' "$idf")
		saved_sum=$(sed -n 's/^sha256=//p' "$idf")
		if [[ $saved_id =~ $uuid_re && $saved_sum == "$sum" ]]; then
			id=$saved_id
			echo "$name: resuming submission $id (already uploaded; not uploading again)"
		else
			echo "$name: the saved submission was for another copy of this file; submitting this one"
			rm -f "$idf"
		fi
	fi
	if [[ -z $id ]]; then
		echo "$name: uploading"
		xcrun notarytool submit "$file" --keychain-profile "$profile" --output-format json >"$json" || rc=$?
		id=$(json_field "$json" id)
		if [[ ! $id =~ $uuid_re ]]; then
			die "$name: the upload failed (notarytool exit $rc). Before running again, check \`xcrun notarytool history --keychain-profile $profile\`: if it lists this upload, save its ID in $idf (see scripts/notarize-lib.sh) and use --resume"
		fi
		printf 'id=%s\nsha256=%s\n' "$id" "$sum" >"$idf.tmp"
		mv "$idf.tmp" "$idf"
		echo "$name: submission $id (saved in ${idf#"$out"/})"
	fi
	notary_wait "$id" "$name" "$sum"
}

# notary_wait ID NAME SHA256: waits on submission ID until Apple decides.
notary_wait() {
	local id=$1 name=$2 sum=$3 json err status rc start delay base log logged
	json=$out/notary-$name.json
	err=$out/notary-$name.err
	log=$out/notary-$name.log.json
	base=${PORTENV_NOTARY_RETRY:-30}
	delay=$base
	start=$(date +%s)
	echo "$name: waiting for Apple (usually minutes; a team's first submissions can take hours)"
	while :; do
		rc=0
		xcrun notarytool wait "$id" --keychain-profile "$profile" --timeout "${PORTENV_NOTARY_POLL:-1h}" --output-format json >"$json" 2>"$err" || rc=$?
		status=$(json_field "$json" status)
		if [[ -z $status ]]; then
			# The wait timed out or the connection dropped: ask once directly.
			xcrun notarytool info "$id" --keychain-profile "$profile" --output-format json >"$json" 2>>"$err" || true
			status=$(json_field "$json" status)
		fi
		case $status in
			Accepted)
				echo "$name: accepted ($id)"
				# The accepted submission must be this file.
				if xcrun notarytool log "$id" --keychain-profile "$profile" "$log" >/dev/null 2>>"$err"; then
					logged=$(json_field "$log" sha256)
					[[ $logged == "$sum" ]] || die "$name: submission $id was accepted for a file with SHA-256 '${logged:-?}', not this one ($sum). Delete ${out}/notary-$name.id and run again"
				else
					echo "$name: warning: couldn't fetch the notary log to match its SHA-256; stapling checks the ticket" >&2
				fi
				return 0
				;;
			Invalid | Rejected)
				xcrun notarytool log "$id" --keychain-profile "$profile" "$log" >/dev/null 2>>"$err" || true
				[[ -s $log ]] && cat "$log" >&2
				die "$name: notarization failed (status $status, submission $id; log in ${log#"$out"/})"
				;;
			'In Progress')
				delay=$base
				echo "$name: still in progress after $(elapsed "$start"); waiting on the same submission"
				;;
			*)
				echo "$name: couldn't get a status for $id (notarytool exit $rc: $(tail -n 1 "$err" 2>/dev/null)); trying again in ${delay}s" >&2
				;;
		esac
		if (($(date +%s) - start >= ${PORTENV_NOTARY_MAX_WAIT:-259200})); then
			die "$name: still '${status:-no answer}' after $(elapsed "$start"). Apple keeps processing submission $id; carry on later with: scripts/release-local.sh --resume $out"
		fi
		sleep "$delay"
		[[ $status == 'In Progress' ]] || delay=$((delay * 2 > 600 ? 600 : delay * 2))
	done
}

elapsed() {
	local s=$(($(date +%s) - $1))
	printf '%dh%02dm' $((s / 3600)) $((s % 3600 / 60))
}
