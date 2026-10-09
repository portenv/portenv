#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# portenvd end to end, as the app uses it (milestone 1.0): open a box,
# type into its tmux session through the agent channel, make a save point,
# change a file, revert, close, reopen, restore from storage (whole-home
# checksums, apt packages replayed), quit with a box open, and the agent's
# failures, quitting when a box can't be saved, waking from sleep, and
# quitting while a box opens. portenvd reaches the box only through the agent channel: a
# watcher over the whole run allows no docker exec but the engine's health
# check and this script's own probes (inspection and simulated failures).
#
#   tests/e2e/daemon.sh [IMAGE]
set -euo pipefail
image=${1:-portenv/toolbox-node:dev}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/portenv-d.XXXXXX")
export PORTENV_KEYS=file PORTENV_HOME=$root/mac PORTENV_DOCKER_NAMESPACE=daemon
failures=0
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/d.json"; }
# type sends keystrokes to the box's tmux session and returns what the
# terminal showed; the session lives on after the client detaches.
type_in() { { printf '%s\r' "$1"; sleep "${2:-2}"; } | "$portenv" attach d 2>/dev/null | tr -d '\r' || true; }
# probe is this script's own docker exec (to inspect, or to simulate a
# failure); the exec watcher allows these and nothing else.
probe() { echo "$*" >>"$root/probes.txt"; docker exec "$@"; }
sums() { probe "portenv-daemon-$(box_id)" sh -c 'cd /home && find . -type f -not -path "./work/.cache/*" -print0 | sort -z | xargs -0 sha256sum'; }
start_daemon() {
	"$repo/bin/portenvd" 2>>"$root/portenvd.log" &
	dpid=$!
	for _ in $(seq 50); do [[ -S $root/mac/portenvd.sock ]] && break; sleep 0.1; done
}
cleanup() {
	# Nothing in the cleanup may fail: under set -e that would fail the run.
	if [[ -n ${dpid:-} ]]; then kill "$dpid" 2>/dev/null || true; wait "$dpid" 2>/dev/null || true; fi
	if [[ -n ${epid:-} ]]; then kill "$epid" 2>/dev/null || true; fi
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-daemon-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-daemon-$id" "portenv-cache-daemon-$id" >/dev/null 2>&1 || true
	fi
	# restic in the box wrote the repository as portenv-sync (uid 990); on
	# Linux hosts those files really are uid 990, so remove them as root in
	# a container.
	docker run --rm -v "$root:/scratch" --entrypoint rm "$image" -rf /scratch/mac/Repositories >/dev/null 2>&1 || true
	rm -rf "$root" || true
}
trap cleanup EXIT

mkdir -p "$root/mac"
# Every docker exec into a container shows up as an exec_create event.
docker events --filter type=container --filter event=exec_create --format '{{.Actor.Attributes.name}} {{.Action}}' > "$root/execs.txt" &
epid=$!
touch "$root/probes.txt"
start_daemon
mode() { if [[ $(uname) == Darwin ]]; then stat -f %Lp "$1"; else stat -c %a "$1"; fi; }
expect "portenvd serves its socket, mode 0600 ($(mode "$root/mac/portenvd.sock"))" test "$(mode "$root/mac/portenvd.sock")" = 600

"$portenv" init d --image "$image" >/dev/null
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "open: a new box (rule 1)" grep -q "rule 1" <<<"$out"

out=$(type_in 'echo who=$(id -un) tmux=${TMUX%%,*}')
expect "attach lands in work's tmux session" grep -q "who=work tmux=/tmp/tmux-1000" <<<"$out"
# tmux is plumbing (GUIDELINES.md §4.1): no status bar, no prefix key, even
# with the user's own tmux config asking for them.
tmuxopt() { probe -u work "portenv-daemon-$(box_id)" tmux show-options -gv "$1" 2>/dev/null; }
expect "tmux's status bar is off" test "$(tmuxopt status)" = off
expect "tmux's prefix key never surfaces (None, Ctrl-B reaches the shell)" test "$(tmuxopt prefix)" = None
type_in 'printf "set -g status on\nset -g prefix C-a\n" > ~/.tmux.conf; tmux source-file ~/.tmux.conf' 1 >/dev/null
"$portenv" app restart d >/dev/null 2>&1 || true
type_in 'true' 1 >/dev/null
expect "still off after the user's own tmux config (on the next attach)" test "$(tmuxopt status)" = off -a "$(tmuxopt prefix)" = None
type_in 'rm -f ~/.tmux.conf' 1 >/dev/null
type_in 'echo one > ~/f.txt' 1 >/dev/null
out=$("$portenv" app point d 2>&1) || true; echo "  $out"
expect "save point" grep -q "^save point " <<<"$out"
type_in 'echo two > ~/f.txt; echo added > ~/g.txt' 1 >/dev/null
out=$("$portenv" app revert d 2>&1) || true; echo "  $out"
expect "revert to the last save point" grep -q "^reverted to save point" <<<"$out"
out=$(type_in 'echo f=$(cat ~/f.txt) g=$(cat ~/g.txt 2>/dev/null || echo none)')
expect "the home is back at the save point" grep -q "f=one g=none" <<<"$out"

out=$("$portenv" app close d 2>&1) || true; echo "  $out"
expect "close saves and releases" grep -q "closed and released" <<<"$out"
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "reopen on the same machine (rule 3)" grep -q "rule 3" <<<"$out"
out=$(type_in 'echo f=$(cat ~/f.txt)')
expect "the reverted file is still there after reopening" grep -q "f=one" <<<"$out"

# ADR 0014: one portenvd at a time, and a restarted portenvd takes over a
# running box (re-keys it) instead of restarting it.
out=$("$repo/bin/portenvd" 2>&1) && rc=0 || rc=$?
expect "a second portenvd refuses to start while the first serves" test "$rc" -ne 0
expect "  and says why" grep -q "another portenvd is already running" <<<"$out"
out=$(type_in 'echo still=$((20+1))')
expect "  and the first keeps its box's channel" grep -q "still=21" <<<"$out"
type_in 'sleep 3600 & echo long=started' 1 >/dev/null
started=$(docker inspect -f '{{.State.StartedAt}}' "portenv-daemon-$(box_id)")
kill -9 "$dpid"; wait "$dpid" 2>/dev/null || true
start_daemon
out=$("$portenv" app state d 2>&1) || true; echo "  $out"
expect "after kill -9: the new portenvd reports the box interrupted" grep -q '"interrupted":true' <<<"$out"
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "reopened by the new portenvd" grep -q "open on" <<<"$out"
expect "  by re-keying it, not restarting it" grep -q "took over the running box" "$root/portenvd.log"
expect "  the box never restarted (same start time)" test "$(docker inspect -f '{{.State.StartedAt}}' "portenv-daemon-$(box_id)")" = "$started"
expect "  the long command is still running" probe "portenv-daemon-$(box_id)" pgrep -x sleep
out=$(type_in 'echo back=$((40+2))')
expect "  the terminal reconnects to the same session" grep -q "back=42" <<<"$out"
out=$("$portenv" app state d 2>&1) || true; echo "  $out"
expect "  and the state line recovers" bash -c "! grep -q interrupted <<<'$out' && grep -q SAVE_STATE_ <<<'$out'"
type_in 'pkill -x sleep' 1 >/dev/null
"$portenv" app close d >/dev/null 2>&1 || true

# The box agent dies mid-session. Processes in the box keep editing files;
# nothing can be saved until the box restarts. Restart Box keeps the local
# home (it has unsaved changes, so nothing is restored over it: rule 3),
# and the next save includes the edits made while the agent was gone.
# docker exec here only simulates the failure and the box's own processes.
"$portenv" app open d >/dev/null 2>&1 || true
c=portenv-daemon-$(box_id)
probe "$c" pkill -f '^portenv-agent serve' || true
sleep 1
out=$("$portenv" app check d 2>&1) || true; echo "  $out"
expect "check: the box agent is unavailable" grep -q "the box agent is unavailable" <<<"$out"
probe -u work "$c" sh -c 'echo while-down > /home/work/down.txt'
out=$("$portenv" app restart d 2>&1) || true; echo "  $out"
expect "Restart Box keeps the local home (rule 3)" grep -q "rule 3" <<<"$out"
out=$(type_in 'echo down=$(cat ~/down.txt)')
expect "the edit made while the agent was gone is still there" grep -q "down=while-down" <<<"$out"
out=$("$portenv" app point d 2>&1) || true; echo "  $out"
expect "the next save goes through" grep -q "^save point " <<<"$out"
# A project with a binary file, and a package to replay on restore (tree:
# not in the image, unlike jq).
type_in 'mkdir -p ~/acme-api && echo hello > ~/acme-api/README.md && head -c 2000000 /dev/urandom > ~/acme-api/blob.bin && echo tree >> ~/.portenv/apt-packages.txt' 2 >/dev/null
"$portenv" app close d >/dev/null 2>&1 || true
"$portenv" app open d >/dev/null 2>&1 || true
before=$(sums)
"$portenv" app close d >/dev/null 2>&1 || true
# Prove it is in the save: drop this machine's copy of the box entirely.
docker rm -f "$c" >/dev/null 2>&1 || true
docker volume rm -f "portenv-home-daemon-$(box_id)" >/dev/null 2>&1 || true
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "reopen with no local home restores from storage (rule 5)" grep -q "rule 5" <<<"$out"
out=$(type_in 'echo down=$(cat ~/down.txt)')
expect "the save includes the edit made while the agent was gone" grep -q "down=while-down" <<<"$out"
after=$(sums)
expect "checksums of the whole home match after the restore ($(wc -l <<<"$after") files)" test -n "$before" -a "$before" = "$after"
# The terminal echoes what is typed: look for what only the command prints.
out=$(type_in 'echo tree-at:$(command -v tree)')
expect "apt-packages.txt is replayed on the restored box" grep -q "tree-at:/usr/bin/tree" <<<"$out"

# A package that can't be installed never stops the box (PLAN.md 1.4): it
# starts, the state line stays normal, and the package is listed for the
# inspector (with the error in the log) until a retry installs it.
type_in 'echo portenv-no-such-package >> ~/.portenv/apt-packages.txt' 1 >/dev/null
out=$("$portenv" app restart d 2>&1) || true; echo "  $out"
expect "a package that can't be installed: the box starts all the same" grep -q "rule 3" <<<"$out"
st=$("$portenv" app state d 2>&1) || true; echo "  $st"
expect "the state line stays normal" python3 -c "import json,sys; sys.exit(0 if json.loads(sys.argv[1])['state'] in ('SAVE_STATE_SAVED','SAVE_STATE_NOT_SAVED_YET') else 1)" "$st"
expect "the package is listed for the inspector" python3 -c "import json,sys; sys.exit(0 if json.loads(sys.argv[1]).get('failed_packages') == ['portenv-no-such-package'] else 1)" "$st"
expect "with apt-get's error" grep -q "Unable to locate package portenv-no-such-package" <<<"$st"
expect "and the error is in the box's log" bash -c "docker logs portenv-daemon-$(box_id) 2>&1 | grep -q 'portenv-no-such-package'"
type_in "sed -i '/portenv-no-such-package/d' ~/.portenv/apt-packages.txt" 1 >/dev/null
"$portenv" app retry-packages d >/dev/null 2>&1 || true
for _ in $(seq 60); do "$portenv" app state d 2>/dev/null | grep -q failed_packages || break; sleep 1; done
expect "after the list is fixed, Retry clears it" bash -c "! '$portenv' app state d 2>/dev/null | grep -q failed_packages"

# Quitting the app stops portenvd (SIGTERM) with the box open: it saves,
# releases and stops the box. Proof: drop the local home; the next open
# restores the last edit from storage.
type_in 'echo before-quit > ~/quit.txt' 1 >/dev/null
kill -TERM "$dpid"
for _ in $(seq 240); do kill -0 "$dpid" 2>/dev/null || break; sleep 0.5; done
expect "quitting: portenvd stops within 2 minutes" bash -c "! kill -0 $dpid 2>/dev/null"
wait "$dpid" 2>/dev/null || true
expect "quitting: the box is stopped" test "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" = false
start_daemon
docker rm -f "$c" >/dev/null 2>&1 || true
docker volume rm -f "portenv-home-daemon-$(box_id)" >/dev/null 2>&1 || true
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "after quitting, the box was released and saved (rule 5)" grep -q "rule 5" <<<"$out"
out=$(type_in 'echo quit=$(cat ~/quit.txt)')
expect "the edit made just before quitting is in the save" grep -q "quit=before-quit" <<<"$out"
"$portenv" app close d >/dev/null 2>&1 || true

# Closing the last window: the app closes the box for the window, then
# again when it quits, so two closes meet. The second finds the box closed;
# it once crashed portenvd (a cleared agent connection, dereferenced).
"$portenv" app open d >/dev/null 2>&1 || true
type_in 'echo window > ~/window.txt' 1 >/dev/null
"$portenv" app close d >"$root/close1.txt" 2>&1 & c1=$!
"$portenv" app close d >"$root/close2.txt" 2>&1 & c2=$!
st1=0; wait "$c1" || st1=$?
st2=0; wait "$c2" || st2=$?
echo "  $(cat "$root/close1.txt") / $(cat "$root/close2.txt")"
expect "closing the last window: both closes succeed ($st1, $st2)" test "$st1" = 0 -a "$st2" = 0
expect "and portenvd keeps running" kill -0 "$dpid"
expect "and the box was saved and released" grep -q "closed and released" "$root/close1.txt" "$root/close2.txt"
out=$("$portenv" app open d 2>&1) || true
out=$(type_in 'echo window=$(cat ~/window.txt)')
expect "the window's last edit is in the box" grep -q "window=window" <<<"$out"
"$portenv" app close d >/dev/null 2>&1 || true

# Quit with a box that can't be saved (the box agent is down): the close
# fails, which the app turns into the alert "couldn't be saved before
# quitting". Quit Anyway then stops portenvd: the box is left as it is,
# with the quit marker; the next open says so and saves first thing.
st_json() { "$portenv" app state d 2>/dev/null; }
state_of() { st_json | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])' 2>/dev/null; }
saved_at() { st_json | python3 -c 'import json,sys; print(json.load(sys.stdin).get("saved_at",""))' 2>/dev/null; }
marker() { echo "$root/mac/state/$(box_id)/quit-unsaved"; }
"$portenv" app open d >/dev/null 2>&1 || true
c=portenv-daemon-$(box_id)
type_in 'echo before-quit-anyway > ~/qa.txt' 1 >/dev/null
before_qa=$(saved_at)
probe "$c" pkill -f '^portenv-agent serve' || true
sleep 1
out=$("$portenv" app close d 2>&1) && st=0 || st=$?; echo "  $out"
expect "quit with the agent down: the close fails, so the app asks (the alert)" test "$st" -ne 0
expect "and says why: the box agent is unavailable" grep -q "the box agent is unavailable" <<<"$out"
kill -TERM "$dpid"   # Quit Anyway
for _ in $(seq 240); do kill -0 "$dpid" 2>/dev/null || break; sleep 0.5; done
wait "$dpid" 2>/dev/null || true
expect "Quit Anyway: portenvd stops" bash -c "! kill -0 $dpid 2>/dev/null"
expect "the quit marker is recorded for the box" test -f "$(marker)"
expect "the box is left as it is (still running)" test "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" = true
start_daemon
out=$("$portenv" app open d 2>&1) || true; echo "  $out"
expect "relaunch: the open says Portenv quit before saving" grep -q "Portenv quit before saving" <<<"$out"
seen=""
for _ in $(seq 120); do s1=$(state_of); seen="$seen $s1"; [[ $s1 == SAVE_STATE_SAVED ]] && break; sleep 0.5; done
echo "  states after relaunch:$(tr ' ' '\n' <<<"$seen" | awk 'NF && $0!=p {printf " %s", $0; p=$0}')"
expect "the state line said so until the save" grep -qE "SAVE_STATE_(QUIT_UNSAVED|SAVING)" <<<"$seen"
expect "then the box was saved first thing" test "$(state_of)" = SAVE_STATE_SAVED
expect "with a newer save time than before quitting" test "$(saved_at)" != "$before_qa"
expect "and the quit marker is gone" test ! -f "$(marker)"
"$portenv" app close d >/dev/null 2>&1 || true
docker rm -f "$c" >/dev/null 2>&1 || true
docker volume rm -f "portenv-home-daemon-$(box_id)" >/dev/null 2>&1 || true
"$portenv" app open d >/dev/null 2>&1 || true
out=$(type_in 'echo qa=$(cat ~/qa.txt)')
expect "the edit made before Quit Anyway is in that save (restored from storage)" grep -q "qa=before-quit-anyway" <<<"$out"

# Waking from sleep: the box agent's channel dropped while the Mac slept.
# The app reports the wake; portenvd checks every open box's channel and
# restarts a box whose channel is gone (its home stays: rule 3).
type_in 'echo before-sleep > ~/sleep.txt' 1 >/dev/null
probe "$c" pkill -f '^portenv-agent serve' || true
sleep 1
out=$("$portenv" app woke 2>&1) || true; echo "  $out"
expect "after waking: the dropped channel is noticed and the box restarted" grep -q "restarted" <<<"$out"
out=$("$portenv" app check d 2>&1) || true
expect "the box agent answers again" bash -c "! grep -q unavailable <<<'$out'"
out=$(type_in 'echo sleep=$(cat ~/sleep.txt)')
expect "the edit made before sleeping is still there" grep -q "sleep=before-sleep" <<<"$out"
out=$("$portenv" app point d 2>&1) || true
expect "and the next save goes through" grep -q "^save point" <<<"$out"
"$portenv" app close d >/dev/null 2>&1 || true

# Quit while a box is still opening: portenvd waits for the open, then
# saves, releases and stops it (it once forgot boxes still opening).
"$portenv" app open d >/dev/null 2>&1 &
opener=$!
sleep 1
kill -TERM "$dpid"
for _ in $(seq 360); do kill -0 "$dpid" 2>/dev/null || break; sleep 0.5; done
wait "$dpid" 2>/dev/null || true
wait "$opener" 2>/dev/null || true
expect "quit while opening: portenvd stops within 3 minutes" bash -c "! kill -0 $dpid 2>/dev/null"
expect "and the box it was opening is stopped" test "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" != true
expect "and released (lease released, nothing unsaved)" python3 -c "import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if d.get('lease')=='released' and not d.get('dirty') else 1)" "$root/mac/state/$(box_id)/state.json"
start_daemon

# An impostor on the agent's port (ADR 0010, conditions): root in the box
# kills the agent and serves a self-made certificate there. portenvd
# refuses it and reports the box agent as unavailable; nothing is saved.
"$portenv" app open d >/dev/null 2>&1 || true
c=portenv-daemon-$(box_id)
probe "$c" sh -c "pkill -f '^portenv-agent serve'; sleep 1; openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=portenv-agent -days 1 -keyout /tmp/i.key -out /tmp/i.crt 2>/dev/null; setsid sh -c 'openssl s_server -accept 7700 -cert /tmp/i.crt -key /tmp/i.key -quiet >/tmp/impostor.log 2>&1' >/dev/null 2>&1 & sleep 1"
out=$("$portenv" app point d 2>&1) || true; echo "  $out"
expect "an impostor on the agent's port: portenvd reports the box agent unavailable" grep -q "the box agent is unavailable" <<<"$out"

# Over the whole run: the image's HEALTHCHECK is the engine's own exec of
# "portenv-agent ready", and the probes above are this script's; any other
# exec into the box would be an access path.
sleep 1; kill "$epid" 2>/dev/null || true; epid=""
grep "portenv-daemon-" "$root/execs.txt" | sed 's/^[^ ]* //' | cut -c1-80 | sort | uniq -c | sed 's/^/  exec events: /' || true
others=$(grep "portenv-daemon-" "$root/execs.txt" | grep -vc 'exec_create: /usr/local/bin/portenv-agent ready$' || true)
probes=$(wc -l <"$root/probes.txt" | tr -d ' ')
expect "no docker exec into the box but the health check and this script's $probes probes ($others)" test "$others" = "$probes"

if (( failures )); then echo "$failures check(s) failed"; sed 's/^/  portenvd: /' "$root/portenvd.log" | tail -20; exit 1; fi
echo "all checks passed"
