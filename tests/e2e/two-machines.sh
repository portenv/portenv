#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Two simulated machines (A and B), each with its own portenvd, share one
# storage directory and drive one box through portenvd's API (portenv app,
# 1.1: never docker exec):
#
#   tests/e2e/two-machines.sh [IMAGE]      (default portenv/toolbox-node:dev)
#
# Checks: new box, the lease, an A → B → A round trip with checksums of
# /home, take over, and resume rule 4 keeping unsaved work. The script's own
# docker exec edits files in the boxes (a test probe, never portenvd's path).
set -euo pipefail
image=${1:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/portenv-e2e.XXXXXX")
export PORTENV_KEYS=file
failures=0

A() { PORTENV_HOME=$root/a PORTENV_DOCKER_NAMESPACE=e2ea "$portenv" "$@"; }
B() { PORTENV_HOME=$root/b PORTENV_DOCKER_NAMESPACE=e2eb "$portenv" "$@"; }
# daemon starts machine m's portenvd and waits until it answers.
daemon() {
	local m=$1
	PORTENV_HOME=$root/$m PORTENV_DOCKER_NAMESPACE=e2e$m "$repo/bin/portenvd" 2>>"$root/portenvd-$m.log" &
	eval "dpid_$m=$!"
	for _ in $(seq 100); do PORTENV_HOME=$root/$m "$portenv" app ping >/dev/null 2>&1 && break; sleep 0.1; done
}
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/a/boxes/demo.json"; }
in_box() { local m=$1; shift; echo "$m" >>"$root/probes.txt"; docker exec -u work -w /home/work "portenv-e2e$m-$(box_id)" bash -lc "$*"; }
sums() { in_box "$1" 'cd /home && find . -type f -not -path "./work/.cache/*" -print0 | sort -z | xargs -0 sha256sum'; }

cleanup() {
	if [[ -n ${epid:-} ]]; then kill "$epid" 2>/dev/null || true; fi
	for p in ${dpid_a:-} ${dpid_b:-}; do kill "$p" 2>/dev/null || true; wait "$p" 2>/dev/null || true; done
	local id; id=$(box_id 2>/dev/null || true)
	for m in a b; do
		if [[ -n $id ]]; then
			docker rm -f "portenv-e2e$m-$id" >/dev/null 2>&1 || true
			docker volume rm -f "portenv-home-e2e$m-$id" >/dev/null 2>&1 || true
		fi
	done
	# restic in the box wrote the repository as portenv-sync (uid 990, mode
	# 0700). On Linux hosts those files really are uid 990, so remove them as
	# root from a container; on a Mac the shared folder maps them to the user.
	docker run --rm -v "$root:/scratch" --entrypoint rm "$image" -rf /scratch/storage >/dev/null 2>&1 || true
	rm -rf "$root" || true
}
trap cleanup EXIT

echo "== setup"
# Every docker exec into a container shows up as an exec_create event.
docker events --filter type=container --filter event=exec_create --format '{{.Actor.Attributes.name}} {{.Action}}' > "$root/execs.txt" &
epid=$!
touch "$root/probes.txt"
A init demo --image "$image" --storage "$root/storage" >/dev/null
mkdir -p "$root/b/boxes" && chmod 700 "$root/b"
cp "$root/a/boxes/demo.json" "$root/b/boxes/"
install -d -m 0700 "$root/b/keys" && install -m 0600 "$root/a/keys/$(box_id).key" "$root/b/keys/"
daemon a
daemon b

echo "== new box on A"
out=$(A app open demo 2>&1); echo "  $out"
expect "rule 1 creates a fresh home" grep -q "rule 1" <<<"$out"
in_box a 'mkdir -p acme-api && echo "hello" > acme-api/README.md && head -c 2000000 /dev/urandom > acme-api/blob.bin && echo tree >> .portenv/apt-packages.txt'
expect "save" A app save demo
expect "status shows A holds the lease (local state, no box started)" bash -c "PORTENV_HOME=$root/a PORTENV_DOCKER_NAMESPACE=e2ea PORTENV_KEYS=file $portenv status demo | grep -q 'lease held here'"
expect "history shows the box open on A" bash -c "PORTENV_HOME=$root/a $portenv app history demo | grep -q 'open on'"

echo "== B while A has it open"
expect "B is refused while A holds the lease" bash -c "! PORTENV_HOME=$root/b $portenv app open demo"

echo "== A → B"
before=$(sums a)
expect "A closes" A app close demo
out=$(B app open demo 2>&1); echo "  $out"
expect "B restores (rule 5)" grep -q "rule 5" <<<"$out"
after=$(sums b)
if [[ "$before" == "$after" ]]; then pass "checksums of /home match on B"; else fail "checksums of /home match on B"; fi
expect "apt-packages.txt replayed on B" in_box b 'command -v tree'
in_box b 'echo "from B" >> acme-api/README.md'
before=$(sums b)
expect "B closes" B app close demo

echo "== B → A"
out=$(A app open demo 2>&1); echo "  $out"
expect "A restores B's change (rule 5)" grep -q "rule 5" <<<"$out"
after=$(sums a)
if [[ "$before" == "$after" ]]; then pass "checksums of /home match back on A"; else fail "checksums of /home match back on A"; fi

echo "== take over and rule 4"
expect "A autosaves" A app save demo
in_box a 'echo "unsaved on A" > acme-api/offline.txt'
out=$(B app open demo --take-over 2>&1); echo "  $out"
expect "B takes over" grep -q "open on" <<<"$out"
in_box b 'echo "from B again" >> acme-api/README.md'
expect "B closes" B app close demo
expect "A's autosave is refused after the take over" bash -c "! PORTENV_HOME=$root/a $portenv app save demo"
# A's portenvd still holds the box open; a new one (an update relaunch)
# takes the running box over and resumes it.
A app relaunch >/dev/null
wait "$dpid_a" 2>/dev/null || true
daemon a
out=$(A app open demo 2>&1); echo "  $out"
expect "A applies rule 4" grep -q "rule 4" <<<"$out"
expect "A has B's latest change" in_box a 'grep -q "from B again" acme-api/README.md'
expect "A's unsaved file is gone from the home" in_box a '! test -e acme-api/offline.txt'
hist=$(A app history demo)
expect "A's unsaved work is in history as orphaned" grep -q orphaned <<<"$hist"
expect "history says the work was kept" grep -q "unsaved work from .*, kept as a separate save" <<<"$hist"
expect "the resume message says the work was kept, with its time" grep -qE "unsaved work from .* was kept as a separate save, [0-9a-f]{8} \(20[0-9-]{8} [0-9:]{8}\)" <<<"$out"
expect "status says A's home is open again" bash -c "PORTENV_HOME=$root/a PORTENV_DOCKER_NAMESPACE=e2ea PORTENV_KEYS=file $portenv status demo | grep -q 'local    open'"
echo "$hist" | sed 's/^/  /'
expect "A closes" A app close demo
# portenvd reached the boxes only through the agent channel: no exec but
# this script's own (in_box) and the engine's health check.
sleep 1
others=$(grep -E "portenv-e2e[ab]-" "$root/execs.txt" | grep -vc 'exec_create: /usr/local/bin/portenv-agent ready$' || true)
probes=$(wc -l <"$root/probes.txt" | tr -d ' ')
expect "no docker exec into the boxes but the health check and this script's $probes probes ($others)" test "$others" = "$probes"

echo
if (( failures )); then echo "$failures check(s) failed"; exit 1; fi
echo "all checks passed"
