#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Tests scripts/archive-downloads.sh against a fake `gh`: what it writes,
# that a second run merges traffic days instead of duplicating them, that a
# rerun on the same day replaces that day's rows, and that a failing gh
# fails the run. Never calls GitHub.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/fixtures"

cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ -n ${FAKE_GH_FAIL:-} ]] && { echo "HTTP 403" >&2; exit 1; }
path=${!#}
case $path in
	*/releases*) cat "$FAKE_GH/releases.json" ;;
	*/traffic/views*) cat "$FAKE_GH/views.json" ;;
	*/traffic/clones*) cat "$FAKE_GH/clones.json" ;;
	*/popular/referrers) cat "$FAKE_GH/referrers.json" ;;
	*/popular/paths) cat "$FAKE_GH/paths.json" ;;
	repos/*) cat "$FAKE_GH/repo.json" ;;
	*) echo "unexpected: $path" >&2; exit 1 ;;
esac
EOF
chmod +x "$tmp/bin/gh"

fixtures() { # fixtures <dmg downloads> <views json days> <stars>
	cat >"$tmp/fixtures/releases.json" <<EOF
[{"tag_name":"v0.2.0","assets":[{"name":"Portenv.dmg","download_count":$1},{"name":"portenv-cli_darwin_arm64.tar.gz","download_count":3}]}]
EOF
	echo "{\"count\":0,\"uniques\":0,\"views\":[$2]}" >"$tmp/fixtures/views.json"
	echo '{"count":2,"uniques":1,"clones":[{"timestamp":"2026-10-18T00:00:00Z","count":2,"uniques":1}]}' >"$tmp/fixtures/clones.json"
	echo '[{"referrer":"news.ycombinator.com","count":30,"uniques":20}]' >"$tmp/fixtures/referrers.json"
	echo '[{"path":"/portenv/portenv","title":"x","count":50,"uniques":25}]' >"$tmp/fixtures/paths.json"
	echo "{\"stargazers_count\":$3}" >"$tmp/fixtures/repo.json"
}

run() {
	PATH="$tmp/bin:$PATH" FAKE_GH="$tmp/fixtures" PORTENV_METRICS_DIR="$tmp/metrics" PORTENV_TODAY="$1" \
		scripts/archive-downloads.sh >/dev/null
}

fail() { echo "test-archive-downloads: $*" >&2; exit 1; }
expect() { # expect <file> <exact content>
	local got
	got=$(cat "$tmp/metrics/$1")
	[[ $got == "$2" ]] || fail "$1: got
$got
want
$2"
}

fixtures 10 '{"timestamp":"2026-10-17T00:00:00Z","count":5,"uniques":3},{"timestamp":"2026-10-18T00:00:00Z","count":7,"uniques":4}' 12
run 2026-10-18
expect downloads.csv "date,tag,asset,downloads
2026-10-18,v0.2.0,Portenv.dmg,10
2026-10-18,v0.2.0,portenv-cli_darwin_arm64.tar.gz,3"
expect views.csv "date,count,uniques
2026-10-17,5,3
2026-10-18,7,4"
expect stars.csv "date,stars
2026-10-18,12"
expect referrers.csv "fetched,referrer,count,uniques
2026-10-18,news.ycombinator.com,30,20"
[[ -f $tmp/metrics/raw/2026-10-18/releases.json ]] || fail "raw answer not kept"

# The next day: 18 Oct's views were incomplete, 19 Oct is new; downloads
# and stars get a new day.
fixtures 14 '{"timestamp":"2026-10-18T00:00:00Z","count":9,"uniques":5},{"timestamp":"2026-10-19T00:00:00Z","count":1,"uniques":1}' 13
run 2026-10-19
expect views.csv "date,count,uniques
2026-10-17,5,3
2026-10-18,9,5
2026-10-19,1,1"
expect downloads.csv "date,tag,asset,downloads
2026-10-18,v0.2.0,Portenv.dmg,10
2026-10-18,v0.2.0,portenv-cli_darwin_arm64.tar.gz,3
2026-10-19,v0.2.0,Portenv.dmg,14
2026-10-19,v0.2.0,portenv-cli_darwin_arm64.tar.gz,3"

# Run again the same day: that day's rows are replaced, not added.
fixtures 15 '{"timestamp":"2026-10-19T00:00:00Z","count":2,"uniques":2}' 13
run 2026-10-19
expect stars.csv "date,stars
2026-10-18,12
2026-10-19,13"
[[ $(grep -c '^2026-10-19,v0.2.0,Portenv.dmg,15$' "$tmp/metrics/downloads.csv") == 1 ]] || fail "same-day rerun didn't replace"
[[ $(grep -c '^2026-10-19,' "$tmp/metrics/downloads.csv") == 2 ]] || fail "same-day rerun duplicated rows"

# A failing gh fails the run, and keeps what was archived.
if PATH="$tmp/bin:$PATH" FAKE_GH="$tmp/fixtures" FAKE_GH_FAIL=1 PORTENV_METRICS_DIR="$tmp/metrics" PORTENV_TODAY=2026-10-20 \
	scripts/archive-downloads.sh >/dev/null 2>&1; then
	fail "a failing gh didn't fail the run"
fi
[[ $(wc -l <"$tmp/metrics/stars.csv") -eq 3 ]] || fail "a failed run changed the archive"

# A day GitHub has no traffic or releases for keeps the history.
before=$(cat "$tmp/metrics/views.csv")
fixtures 15 '' 13
echo '[]' >"$tmp/fixtures/releases.json"
run 2026-10-21
[[ $(cat "$tmp/metrics/views.csv") == "$before" ]] || fail "an empty answer dropped the views history"
[[ $(grep -c '^2026-10-19,' "$tmp/metrics/downloads.csv") == 2 ]] || fail "an empty answer dropped downloads"

echo "test-archive-downloads: ok"
