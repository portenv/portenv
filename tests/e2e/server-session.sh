#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# A box on a server through the runner and the agent channel (milestone
# 1.0b), against a disposable server set up by server/setup.sh:
#
#   PORTENV_SSH="ssh -i KEY -o ..." tests/e2e/server-session.sh HOST "ssh-ed25519 AAAA..." "ssh-ed25519 MAC-KEY" [IMAGE]
#
# Uploads this build (portenv, portenv-runner, the setup script and the
# source), builds the image on the server, then as the app does through
# portenvd: Move To the server, the terminal there, edit, save point and
# Revert To there, a runner restart (nothing lost), an impostor on the box's
# agent port, no channel secret on the server's disk, journal or shell
# history, and Move To this Mac with the edits. Admin steps use ubuntu@HOST;
# the Mac's own calls go as portenv@HOST, whose sudo allows only portenv.
set -euo pipefail
host=${1:?usage: server-session.sh HOST HOST-KEY MAC-KEY [IMAGE]}
host_key=${2:?usage: server-session.sh HOST HOST-KEY MAC-KEY [IMAGE]}
mac_key=${3:?usage: server-session.sh HOST HOST-KEY MAC-KEY [IMAGE]}
image=${4:-portenv/toolbox-node:10b}
repo=$(cd "$(dirname "$0")/../.." && pwd)
portenv=$repo/bin/portenv
root=$(mktemp -d "${TMPDIR:-/tmp}/pss.XXXX")
read -r -a ssh_cmd <<<"${PORTENV_SSH:-ssh}"
# A dropped connection (for example this machine's address changing) fails
# within a minute instead of hanging.
ssh_cmd+=(-o ServerAliveInterval=15 -o ServerAliveCountMax=4)
export PORTENV_KEYS=file PORTENV_HOME=$root/mac PORTENV_DOCKER_NAMESPACE=session
box=session-$(date +%Y%m%d%H%M%S)
target=portenv@$host
failures=0
A() { "${ssh_cmd[@]}" -- "ubuntu@$host" "$@" </dev/null; }
Ain() { "${ssh_cmd[@]}" -- "ubuntu@$host" "$@"; }
pass() { printf 'ok    %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name"; fi; }
box_id() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$root/mac/boxes/$box.json"; }
type_in() { { printf '%s\r' "$1"; sleep "${2:-3}"; } | "$portenv" attach "$box" 2>/dev/null | tr -d '\r' || true; }
app() { "$portenv" app "$@" 2>&1; }
cleanup() {
	[[ -n ${dpid:-} ]] && kill "$dpid" 2>/dev/null && wait "$dpid" 2>/dev/null
	local id; id=$(box_id 2>/dev/null || true)
	if [[ -n $id ]]; then
		docker rm -f "portenv-session-$id" >/dev/null 2>&1 || true
		docker volume rm -f "portenv-home-session-$id" "portenv-cache-session-$id" >/dev/null 2>&1 || true
	fi
	rm -rf "$root" || true
}
trap cleanup EXIT

echo "== server: this build, the runner, the SSH user portenv, the image"
arch=$(A dpkg --print-architecture)
for b in portenv portenv-runner; do
	(cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "$root/$b-linux" "./core/cmd/$b")
	gzip -9c "$root/$b-linux" | Ain "gunzip > /tmp/$b && chmod +x /tmp/$b"
done
Ain 'cat > /tmp/portenv-setup.sh' < "$repo/server/setup.sh"
A sudo bash /tmp/portenv-setup.sh --portenv-bin /tmp/portenv --runner-bin /tmp/portenv-runner --mac-key "'$mac_key'" 2>&1 | grep -E "^== |portenv-runner|error|FAIL" | sed 's/^/  /' || true
(cd "$repo" && git ls-files -z | COPYFILE_DISABLE=1 tar --no-mac-metadata --null -T - -czf - ) | Ain 'rm -rf /tmp/portenv-src && mkdir -p /tmp/portenv-src && tar -xzf - -C /tmp/portenv-src'
A "cd /tmp/portenv-src && sudo docker build -q -f images/toolbox-node/Dockerfile -t '$image' . | tail -1" | sed 's/^/  image /'
expect "the runner is running" A systemctl is-active portenv-runner
expect "sudo for portenv allows portenv only (a shell is refused)" bash -c "! ${ssh_cmd[*]} -- $target sudo -n /bin/sh -c true </dev/null"
expect "sudo for portenv runs /usr/local/bin/portenv" "${ssh_cmd[@]}" -- "$target" sudo -n /usr/local/bin/portenv version

echo "== a box on this Mac with storage on the server"
mkdir -p "$root/mac"
printf '{"servers": ["%s"]}\n' "$target" > "$root/mac/machine.json"
"$portenv" init "$box" --image "$image" --storage "sftp:portenv-storage@$host:/storage" --storage-host-key "$host_key" --storage-rest 127.0.0.1:7422 > "$root/init.txt"
A sudo bash /tmp/portenv-setup.sh --only storage-account --storage-key "'$(grep '^ssh-ed25519 ' "$root/init.txt")'" --rest-user "'$(sed -n 's/^rest-user //p' "$root/init.txt")'" >/dev/null
"$repo/bin/portenvd" 2>"$root/portenvd.log" &
dpid=$!
for _ in $(seq 50); do [[ -S $root/mac/portenvd.sock ]] && break; sleep 0.1; done
out=$(app open "$box") || true; echo "  $out"
expect "open on this Mac (rule 1)" grep -q "rule 1" <<<"$out"
type_in 'echo from-the-mac > ~/note.txt' 2 >/dev/null

echo "== Move To ▸ the server"
t0=$(python3 -c 'import time; print(time.time())')
out=$(app move "$box" "$target") || true; echo "  $out"
t1=$(python3 -c 'import time; print(time.time())')
expect "Move To ▸ $target ($(python3 -c "print(round($t1 - $t0, 1))") s)" grep -q "open on $target" <<<"$out"
state=$(app state "$box") || true; echo "  state $state"
expect "the state says where it runs" grep -q "\"location\":\"$target\"" <<<"$state"
server_kernel=$(A uname -r)
out=$(type_in 'echo where=$(uname -r) note=$(cat ~/note.txt)')
expect "the window's terminal runs on the server ($server_kernel)" grep -q "where=$server_kernel note=from-the-mac" <<<"$out"

echo "== edit, save point and Revert To on the server"
type_in 'echo one > ~/f.txt' 2 >/dev/null
out=$(app point "$box") || true; echo "  $out"
expect "save point on the server" grep -q "^save point" <<<"$out"
type_in 'echo two > ~/f.txt; echo extra > ~/g.txt' 2 >/dev/null
out=$(app revert "$box") || true; echo "  $out"
expect "Revert To ▸ Last Save Point on the server" grep -q "^reverted to save point" <<<"$out"
out=$(type_in 'echo f=$(cat ~/f.txt) g=$(cat ~/g.txt 2>/dev/null || echo none)')
expect "the home on the server is back at the save point" grep -q "f=one g=none" <<<"$out"

echo "== the runner restarts: boxes keep running, nothing is lost"
type_in 'echo before-restart > ~/r1.txt' 2 >/dev/null
A sudo systemctl restart portenv-runner
sleep 2
expect "the box keeps running on the server" A "sudo docker ps --format '{{.Names}}' | grep -q portenv-$(box_id)"
out=$(app check "$box") || true; echo "  $out"
expect "check: the box agent is unavailable" grep -q "the box agent is unavailable" <<<"$out"
# Processes in the box keep running while the agent is gone: one edits a file.
A "sudo docker exec -u work portenv-$(box_id) sh -c 'echo while-down > /home/work/r2.txt'"
out=$(app restart "$box") || true; echo "  $out"
expect "Restart Box on the server keeps its home (rule 3)" grep -q "rule 3" <<<"$out"
out=$(type_in 'echo r1=$(cat ~/r1.txt) r2=$(cat ~/r2.txt)')
expect "both edits are still there" grep -q "r1=before-restart r2=while-down" <<<"$out"

echo "== an impostor on the server box's agent port"
A "sudo docker exec portenv-$(box_id) sh -c \"pkill -f '^portenv-agent serve'; sleep 1; openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=portenv-agent -days 1 -keyout /tmp/i.key -out /tmp/i.crt 2>/dev/null; setsid sh -c 'openssl s_server -accept 7700 -cert /tmp/i.crt -key /tmp/i.key -quiet >/tmp/impostor.log 2>&1' >/dev/null 2>&1 & sleep 1\""
out=$(app point "$box") || true; echo "  $out"
expect "a save point is refused: the box agent is unavailable" grep -q "the box agent is unavailable" <<<"$out"
out=$({ sleep 3; } | "$portenv" attach "$box" 2>&1 | tr -d '\r' | tail -1) || true; echo "  $out"
expect "the terminal is refused: the box agent is unavailable" grep -q "the box agent is unavailable" <<<"$out"
token1=$("${ssh_cmd[@]}" -- "$target" sudo /usr/local/bin/portenv app channel "$box" </dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
token=$token1
impostor_log=$(A "sudo docker exec portenv-$(box_id) cat /tmp/impostor.log" || true)
if [[ -n $token ]] && ! grep -qF "$token" <<<"$impostor_log"; then pass "the impostor never received the token"; else fail "the impostor never received the token"; fi
out=$(app restart "$box") || true; echo "  $out"
expect "Restart Box after the impostor (rule 3)" grep -q "rule 3" <<<"$out"

echo "== no channel secret on the server's disk, journal or shell history"
token=$("${ssh_cmd[@]}" -- "$target" sudo /usr/local/bin/portenv app channel "$box" </dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
# This start's token and the one before the impostor: neither anywhere.
# The tokens never go on the server's command line (sudo logs command lines,
# so a search there would plant them in the logs it checks): the disk search
# reads them on stdin, and the journal and histories are searched here.
search_disk() { printf '%s\n' "$1" | "${ssh_cmd[@]}" -- "ubuntu@$host" 'sudo timeout 900 grep -rlsF -f /dev/stdin / --exclude-dir=proc --exclude-dir=sys --exclude-dir=dev; echo "rc=$?"' || echo "rc=ssh-failed"; }
# Control: a canary planted in a file is found, so the search really works.
canary=$(openssl rand -hex 24)
printf '%s' "$canary" | Ain 'sudo tee /var/tmp/portenv-canary >/dev/null'
if search_disk "$canary" | grep -qx /var/tmp/portenv-canary; then pass "control: the disk search finds a planted canary"; else fail "control: the disk search finds a planted canary"; fi
A sudo rm -f /var/tmp/portenv-canary
for tk in "$token" "$token1"; do
	res=$(printf '%s\n' "$tk" | "${ssh_cmd[@]}" -- "ubuntu@$host" 'sudo timeout 900 grep -rlsF -f /dev/stdin / --exclude-dir=proc --exclude-dir=sys --exclude-dir=dev; echo "rc=$?"' || echo "rc=ssh-failed")
	rc=${res##*rc=}
	hits=$(sed '/^rc=/d' <<<"$res")
	# grep: 1 is no match; 2 is no match plus unreadable special files.
	if [[ ($rc == 1 || $rc == 2) && -z $hits ]]; then pass "a channel token is in no file on the server"; else fail "a channel token is in no file on the server (grep $rc): $hits"; fi
	if A 'sudo journalctl --no-pager -o cat' | grep -qF -- "$tk"; then fail "a channel token is not in the journal"; else pass "a channel token is not in the journal"; fi
	if A 'sudo cat /root/.bash_history /home/ubuntu/.bash_history /home/portenv/.bash_history 2>/dev/null' | grep -qF -- "$tk"; then fail "a channel token is in no shell history"; else pass "a channel token is in no shell history"; fi
done

echo "== Move To ▸ This Mac"
out=$(app move "$box" this-mac) || true; echo "  $out"
expect "Move To ▸ This Mac (rule 5)" grep -q "rule 5" <<<"$out"
out=$(type_in 'echo where=$(uname -r) f=$(cat ~/f.txt) r2=$(cat ~/r2.txt)')
expect "back on this Mac with the server's edits" grep -q "f=one r2=while-down" <<<"$out"
expect "and running here again" bash -c "! grep -q 'where=$server_kernel' <<<'$out'"
app close "$box" >/dev/null || true

if (( failures )); then echo "$failures check(s) failed"; tail -20 "$root/portenvd.log" | sed 's/^/  portenvd: /'; exit 1; fi
echo "all checks passed"
