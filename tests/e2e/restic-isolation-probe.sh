#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Runs inside a toolbox box as root (copied in by restic-isolation.sh). Starts
# a slow restic backup holding a secret password and checks whether other
# processes in the box can read it. Prints one line per probe:
#   <mode> <attacker> <probe> LEAKED|DENIED
set -u
mode=$1   # sync: restic as portenv-sync with file caps; root: restic as plain root (control)
secret="portenv-probe-$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
restic=/usr/local/libexec/portenv/restic
repo=/var/tmp/probe-repo-$mode
id -u lane >/dev/null 2>&1 || useradd --uid 2000 --no-create-home lane

as_sync() { setpriv --reuid=990 --regid=990 --clear-groups "$@"; }
runner=()
[[ $mode == sync ]] && runner=(setpriv --reuid=990 --regid=990 --clear-groups)

rm -rf "$repo" && install -d -o 990 -g 990 -m 0700 "$repo"
[[ $mode == root ]] && chown root:root "$repo"
printf %s "$secret" | RESTIC_PASSWORD_FILE=/dev/stdin "${runner[@]}" "$restic" -q -r "$repo" --no-cache init >/dev/null

mkdir -p /home/work/probe && head -c 40M /dev/urandom > /home/work/probe/data.bin

# The canary environment variable checks whether the environment is readable;
# the password itself only travels through the pipe on fd 3.
PORTENV_CANARY="$secret" RESTIC_PASSWORD_FILE=/dev/fd/3 \
	"${runner[@]}" "$restic" -q -r "$repo" --no-cache backup --limit-upload 1024 /home/work/probe \
	3< <(printf %s "$secret") >/dev/null 2>&1 &
sleep 4
pid=$(pgrep -f "$restic -q -r $repo" | head -1)
[[ -n $pid ]] || { echo "$mode setup restic-not-running FAILED"; exit 1; }

cat > /tmp/probe.py <<'PY'
import ctypes, os, re, sys
pid, secret, what = int(sys.argv[1]), sys.argv[2].encode(), sys.argv[3]
def out(ok): print("LEAKED" if ok else "DENIED")
try:
    if what == "environ":
        out(secret in open(f"/proc/{pid}/environ", "rb").read())
    elif what == "fd3":
        os.open(f"/proc/{pid}/fd/3", os.O_RDONLY | os.O_NONBLOCK); out(True)
    elif what == "cmdline":
        out(secret in open(f"/proc/{pid}/cmdline", "rb").read())
    elif what == "mem":
        found = False
        with open(f"/proc/{pid}/mem", "rb", 0) as mem:
            for line in open(f"/proc/{pid}/maps"):
                m = re.match(r"([0-9a-f]+)-([0-9a-f]+) rw", line)
                if not m: continue
                lo, hi = int(m[1], 16), int(m[2], 16)
                try:
                    mem.seek(lo); found |= secret in mem.read(hi - lo)
                except OSError: pass
        out(found)
    elif what == "ptrace":
        libc = ctypes.CDLL(None, use_errno=True)
        ok = libc.ptrace(16, pid, 0, 0) == 0
        if ok: libc.ptrace(17, pid, 0, 0)
        out(ok)
except OSError:
    out(False)
PY

for attacker in root root-as-sync lane; do
	for what in environ fd3 cmdline mem ptrace; do
		case $attacker in
			root) r=$(python3 /tmp/probe.py "$pid" "$secret" "$what") ;;
			root-as-sync) r=$(as_sync python3 /tmp/probe.py "$pid" "$secret" "$what") ;;
			lane) r=$(setpriv --reuid=2000 --regid=2000 --clear-groups python3 /tmp/probe.py "$pid" "$secret" "$what") ;;
		esac
		echo "$mode $attacker $what $r"
	done
done

# A successful ptrace probe leaves restic stopped; SIGKILL works regardless.
kill -KILL "$pid" 2>/dev/null; wait 2>/dev/null
# Never on disk: search every file outside /proc and /sys for the secret.
if grep -rIl --exclude-dir={proc,sys,dev} -F "$secret" / 2>/dev/null | grep -v '^/tmp/probe' | head -1 | grep -q .; then
	echo "$mode disk any-file LEAKED"
else
	echo "$mode disk any-file DENIED"
fi
rm -rf /home/work/probe "$repo"
