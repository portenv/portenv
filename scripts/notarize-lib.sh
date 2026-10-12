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
# The saved submission, $out/notary-<file>.id, is two or three lines:
#   id=<submission UUID>
#   sha256=<the file's SHA-256>
#   from=history          (only if it was found in notarytool history)
# It's only reused for a file with that same SHA-256.
#
# A slow upload can reach Apple before `notarytool submit` returns, and a
# submit that then dies loses the ID. So before uploading a file with no saved
# ID, and again after a failed upload, notarytool history is searched for a
# submission of the same name, In Progress or Accepted, made since the file
# (its modification time, less 10 minutes for clock differences). One found
# is adopted without uploading; once Accepted, its notary log must show this
# file's SHA-256, or the run fails without uploading and the next run skips
# that submission ($out/notary-<file>.skip).

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

# notary_find FILE: the newest same-name submission in notarytool history,
# In Progress or Accepted, made since FILE and not in the skip list; empty if
# none (or if history can't be read).
notary_find() {
	local file=$1 name hist
	name=$(basename "$file")
	hist=$out/notary-history.json
	xcrun notarytool history --keychain-profile "$profile" --output-format json >"$hist" 2>/dev/null || return 0
	python3 -c '
import datetime, json, os, sys
hist, path, skip = sys.argv[1:4]
try:
    entries = json.load(open(hist)).get("history", [])
except (ValueError, AttributeError):
    sys.exit(0)
skipped = set(open(skip).read().split()) if os.path.exists(skip) else set()
since = os.path.getmtime(path) - 600
best = None
for e in entries:
    if not isinstance(e, dict) or e.get("name") != os.path.basename(path):
        continue
    if e.get("status") not in ("In Progress", "Accepted") or e.get("id") in skipped:
        continue
    try:
        created = datetime.datetime.fromisoformat(e.get("createdDate", "").replace("Z", "+00:00")).timestamp()
    except ValueError:
        continue
    if created >= since and (best is None or created > best[0]):
        best = (created, e["id"])
if best:
    print(best[1])
' "$hist" "$file" "$out/notary-$name.skip" 2>/dev/null || true
}

# save_id IDFILE ID SHA256 [from=history]
save_id() {
	{
		printf 'id=%s\nsha256=%s\n' "$2" "$3"
		[[ -z ${4:-} ]] || printf '%s\n' "$4"
	} >"$1.tmp"
	mv "$1.tmp" "$1"
}

uuid_re='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'

# notarize FILE: notarizes FILE, reusing its saved submission if there is one;
# returns once Apple has accepted it, and fails with the notary log otherwise.
notarize() {
	local file=$1 name idf sum id='' saved_id saved_sum json rc=0 adopted=0
	name=$(basename "$file")
	idf=$out/notary-$name.id
	json=$out/notary-$name.json
	sum=$(sha256_of "$file")
	if [[ -f $idf ]]; then
		saved_id=$(sed -n 's/^id=//p' "$idf")
		saved_sum=$(sed -n 's/^sha256=//p' "$idf")
		if [[ $saved_id =~ $uuid_re && $saved_sum == "$sum" ]]; then
			id=$saved_id
			grep -qx 'from=history' "$idf" && adopted=1
			echo "$name: resuming submission $id (already uploaded; not uploading again)"
		else
			echo "$name: the saved submission was for another copy of this file; submitting this one"
			rm -f "$idf"
		fi
	fi
	if [[ -z $id ]]; then
		id=$(notary_find "$file")
		if [[ $id =~ $uuid_re ]]; then
			adopted=1
			echo "$name: notarytool history has submission $id for this file name, made since the file: waiting on it instead of uploading (its SHA-256 is checked once Apple accepts it)"
		else
			echo "$name: uploading"
			xcrun notarytool submit "$file" --keychain-profile "$profile" --output-format json >"$json" || rc=$?
			id=$(json_field "$json" id)
			if [[ ! $id =~ $uuid_re ]]; then
				id=$(notary_find "$file")
				[[ $id =~ $uuid_re ]] || die "$name: the upload failed (notarytool exit $rc), and notarytool history shows no submission of it. Before running again, check \`xcrun notarytool history --keychain-profile $profile\`: if it lists this upload, save its ID in $idf (see scripts/notarize-lib.sh) and use --resume"
				adopted=1
				echo "$name: the upload reported a failure (notarytool exit $rc), but notarytool history has submission $id: waiting on it"
			fi
		fi
		if [[ $adopted == 1 ]]; then save_id "$idf" "$id" "$sum" from=history; else save_id "$idf" "$id" "$sum"; fi
		echo "$name: submission $id (saved in ${idf#"$out"/})"
	fi
	notary_wait "$id" "$name" "$sum" "$adopted"
}

# notarize_staple FILE [BUNDLE]: notarizes FILE and staples the ticket to
# BUNDLE (default FILE itself). An uploaded zip is deleted only once the
# staple has worked, so a run resumed after a failed staple finds the same
# zip, with the same SHA-256, and doesn't upload it again.
notarize_staple() {
	local file=$1 target=${2:-$1}
	notarize "$file"
	xcrun stapler staple "$target" ||
		die "$(basename "$target"): stapling failed (the submission is accepted and saved); carry on with: scripts/release-local.sh --resume $out"
	[[ $target == "$file" ]] || rm -f "$file"
}

# notary_wait ID NAME SHA256 ADOPTED: waits on submission ID until Apple
# decides. ADOPTED (1: found in history) makes the SHA-256 check mandatory.
notary_wait() {
	local id=$1 name=$2 sum=$3 adopted=${4:-0} json err status rc start delay base log logged
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
					if [[ $logged != "$sum" ]]; then
						# Never this file's: forget it, and skip it from now on.
						echo "$id" >>"$out/notary-$name.skip"
						rm -f "$out/notary-$name.id"
						die "$name: submission $id was accepted for a file with SHA-256 '${logged:-?}', not this one ($sum). Nothing was uploaded; run again to upload this file"
					fi
				elif [[ $adopted == 1 ]]; then
					die "$name: couldn't fetch the notary log of submission $id (found in history) to check it's this file; carry on later with: scripts/release-local.sh --resume $out"
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
