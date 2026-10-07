#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Two simulated machines (A and B) share one storage directory and drive one
# box with the portenv CLI and the docker driver:
#
#   tests/e2e/two-machines.sh [IMAGE]      (default portenv/toolbox-node:dev)
#
# Checks: new box, the lease, an A → B → A round trip with checksums of
# /home, take over, and resume rule 4 keeping unsaved work. Uses docker exec
# to edit files in the boxes (Phase 0 tests only).
set -euo pipefail
image=${1:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/portenv-e2e.XXXXXX")
export PORTENV_KEYS=file
failures=0

A() { PORTENV_HOME=$root/a PORTENV_DOCKER_NAMESPACE=e2ea "$portenv" "$@"; }
B() { PORTENV_HOME=$root/b PORTENV_DOCKER_NAMESPACE=e2eb "$portenv" "$@"; }
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/a/boxes/demo.json"; }
in_box() { local m=$1; shift; docker exec -u work -w /home/work "portenv-e2e$m-$(box_id)" bash -lc "$*"; }
sums() { in_box "$1" 'cd /home && find . -type f -not -path "./work/.cache/*" -print0 | sort -z | xargs -0 sha256sum'; }

cleanup() {
	local id; id=$(box_id 2>/dev/null || true)
	for m in a b; do
		[[ -n $id ]] && docker rm -f "portenv-e2e$m-$id" >/dev/null 2>&1
		[[ -n $id ]] && docker volume rm -f "portenv-home-e2e$m-$id" >/dev/null 2>&1
	done
	rm -rf "$root"
}
trap cleanup EXIT

echo "== setup"
A init demo --image "$image" --storage "$root/storage" >/dev/null
mkdir -p "$root/b/boxes" && chmod 700 "$root/b"
cp "$root/a/boxes/demo.json" "$root/b/boxes/"
install -d -m 0700 "$root/b/keys" && install -m 0600 "$root/a/keys/$(box_id).key" "$root/b/keys/"

echo "== new box on A"
out=$(A resume demo 2>/dev/null); echo "  $out"
expect "rule 1 creates a fresh home" grep -q "rule 1" <<<"$out"
in_box a 'mkdir -p acme-api && echo "hello" > acme-api/README.md && head -c 2000000 /dev/urandom > acme-api/blob.bin && echo jq >> .portenv/apt-packages.txt'
expect "save" A save demo
expect "status shows the lease on A" bash -c "PORTENV_HOME=$root/a PORTENV_DOCKER_NAMESPACE=e2ea PORTENV_KEYS=file $portenv status demo | grep -q 'lease    .*'"

echo "== B while A has it open"
expect "B is refused while A holds the lease" bash -c "! PORTENV_HOME=$root/b PORTENV_DOCKER_NAMESPACE=e2eb PORTENV_KEYS=file $portenv resume demo"

echo "== A → B"
before=$(sums a)
expect "A closes" A close demo
out=$(B resume demo 2>/dev/null); echo "  $out"
expect "B restores (rule 5)" grep -q "rule 5" <<<"$out"
after=$(sums b)
if [[ "$before" == "$after" ]]; then pass "checksums of /home match on B"; else fail "checksums of /home match on B"; fi
expect "apt-packages.txt replayed on B" in_box b 'command -v jq'
in_box b 'echo "from B" >> acme-api/README.md'
before=$(sums b)
expect "B closes" B close demo

echo "== B → A"
out=$(A resume demo 2>/dev/null); echo "  $out"
expect "A restores B's change (rule 5)" grep -q "rule 5" <<<"$out"
after=$(sums a)
if [[ "$before" == "$after" ]]; then pass "checksums of /home match back on A"; else fail "checksums of /home match back on A"; fi

echo "== take over and rule 4"
expect "A autosaves" A save demo
in_box a 'echo "unsaved on A" > acme-api/offline.txt'
out=$(B resume demo --take-over 2>/dev/null); echo "  $out"
expect "B takes over" grep -q "open on" <<<"$out"
in_box b 'echo "from B again" >> acme-api/README.md'
expect "B closes" B close demo
expect "A's autosave is refused after the take over" bash -c "! PORTENV_HOME=$root/a PORTENV_DOCKER_NAMESPACE=e2ea PORTENV_KEYS=file $portenv save demo"
out=$(A resume demo 2>/dev/null); echo "  $out"
expect "A applies rule 4" grep -q "rule 4" <<<"$out"
expect "A has B's latest change" in_box a 'grep -q "from B again" acme-api/README.md'
expect "A's unsaved file is gone from the home" in_box a '! test -e acme-api/offline.txt'
hist=$(A history demo)
expect "A's unsaved work is in history as orphaned" grep -q orphaned <<<"$hist"
echo "$hist" | sed 's/^/  /'
expect "A closes" A close demo

echo
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
