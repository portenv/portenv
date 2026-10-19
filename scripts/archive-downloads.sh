#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# archive-downloads.sh: archives the repository's download counts and
# traffic once a day (PLAN.md 1.8). GitHub keeps traffic for only 14 days,
# so a missed day is recovered by the next run within two weeks.
#
# Uses the owner's own `gh` sign-in (traffic needs push access); no token is
# read from or written to the repository. Writes outside the repository, to
# $PORTENV_METRICS_DIR (default ~/Portenv-metrics):
#   raw/<date>/*.json   what GitHub answered, as is
#   downloads.csv       date,tag,asset,downloads   (each release asset's total that day)
#   views.csv           date,count,uniques         (per day, merged across runs)
#   clones.csv          date,count,uniques
#   referrers.csv       fetched,referrer,count,uniques   (GitHub's 14-day window)
#   paths.csv           fetched,path,count,uniques
#   stars.csv           date,stars
# Running it twice a day replaces that day's rows. It schedules nothing;
# tools/counting/README.md says how to run it daily.
set -euo pipefail

repo=${PORTENV_REPO:-portenv/portenv}
dir=${PORTENV_METRICS_DIR:-$HOME/Portenv-metrics}
today=${PORTENV_TODAY:-$(date -u +%F)}

command -v gh >/dev/null || { echo "archive-downloads: gh is not installed (brew install gh)" >&2; exit 1; }
command -v jq >/dev/null || { echo "archive-downloads: jq is not installed (brew install jq)" >&2; exit 1; }

raw=$dir/raw/$today
mkdir -p "$raw"

fetch() { # fetch <name> <api path> [--paginate]
	local name=$1 path=$2; shift 2
	if ! gh api "$@" -H 'Accept: application/vnd.github+json' "$path" >"$raw/$name.json.tmp"; then
		rm -f "$raw/$name.json.tmp"
		echo "archive-downloads: gh api $path failed (signed in with access to $repo? gh auth status)" >&2
		exit 1
	fi
	mv "$raw/$name.json.tmp" "$raw/$name.json"
}

fetch releases "repos/$repo/releases?per_page=100" --paginate
fetch views "repos/$repo/traffic/views?per=day"
fetch clones "repos/$repo/traffic/clones?per=day"
fetch referrers "repos/$repo/traffic/popular/referrers"
fetch paths "repos/$repo/traffic/popular/paths"
fetch repo "repos/$repo"

# merge <csv> <header> <new rows file>: keeps the old rows whose first
# column isn't in the new rows, adds the new ones, sorted.
merge() {
	local csv=$1 header=$2 new=$3 tmp
	# Nothing new (no releases yet, no traffic): keep what's there.
	if [[ ! -s $new ]]; then
		[[ -f $csv ]] || echo "$header" >"$csv"
		return 0
	fi
	tmp=$(mktemp "$dir/.merge.XXXXXX")
	{
		echo "$header"
		{
			if [[ -f $csv ]]; then
				tail -n +2 "$csv" | awk -F, 'NR==FNR { seen[$1]=1; next } !($1 in seen)' "$new" -
			fi
			cat "$new"
		} | LC_ALL=C sort
	} >"$tmp"
	mv "$tmp" "$csv"
}

rows=$(mktemp "$dir/.rows.XXXXXX")
trap 'rm -f "$rows"' EXIT

# --paginate may print one array per page: slurp them into one.
jq -rs --arg d "$today" '
	add // [] | .[] | .tag_name as $t | .assets[]
	| [$d, $t, (.name | gsub(","; "_")), .download_count] | @csv' "$raw/releases.json" | tr -d '"' >"$rows"
merge "$dir/downloads.csv" "date,tag,asset,downloads" "$rows"

for kind in views clones; do
	jq -r --arg k "$kind" '.[$k][] | [(.timestamp[0:10]), .count, .uniques] | @csv' "$raw/$kind.json" | tr -d '"' >"$rows"
	merge "$dir/$kind.csv" "date,count,uniques" "$rows"
done

jq -r --arg d "$today" '.[] | [$d, (.referrer | gsub(","; "_")), .count, .uniques] | @csv' "$raw/referrers.json" | tr -d '"' >"$rows"
merge "$dir/referrers.csv" "fetched,referrer,count,uniques" "$rows"

jq -r --arg d "$today" '.[] | [$d, (.path | gsub(","; "_")), .count, .uniques] | @csv' "$raw/paths.json" | tr -d '"' >"$rows"
merge "$dir/paths.csv" "fetched,path,count,uniques" "$rows"

jq -r --arg d "$today" '[$d, .stargazers_count] | @csv' "$raw/repo.json" | tr -d '"' >"$rows"
merge "$dir/stars.csv" "date,stars" "$rows"

echo "archive-downloads: $repo archived for $today in $dir"
