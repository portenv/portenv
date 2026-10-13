# SPDX-License-Identifier: Apache-2.0
#
# ADR 0020's latency gate, at tmux: keystroke to echo, through an attached
# tmux client (today's path) against a tmux control-mode client (send-keys
# -H, echo read from %output). Both type into bash's prompt in the same
# session, one key at a time, waiting for each echo; a backspace follows
# each key so the line stays short. Run in the toolbox image:
#   docker run --rm -v "$PWD/tests/spike/q3:/q3" --entrypoint python3 \
#     portenv/toolbox-node:<tag> /q3/latency.py [keys] [load]
# With load, another tab prints continuously while typing.
import os, pty, select, statistics, subprocess, sys, time

N = int(sys.argv[1]) if len(sys.argv) > 1 else 500
LOAD = len(sys.argv) > 2 and sys.argv[2] == "load"
ENV = dict(os.environ, LANG="C.UTF-8", TERM="xterm-256color")
SOCK = "lat"


def tmux(*a):
    return subprocess.run(["tmux", "-L", SOCK, *a], env=ENV, capture_output=True, text=True)


def pct(xs, p):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p / 100 * (len(xs) - 1))))]


def report(name, ms):
    print(f"{name:<34} n={len(ms):4d}  p50 {statistics.median(ms):6.2f} ms  p95 {pct(ms, 95):6.2f} ms  "
          f"p99 {pct(ms, 99):6.2f} ms  max {max(ms):6.2f} ms")


tmux("new-session", "-d", "-s", "s", "-x", "80", "-y", "24", "bash --norc --noprofile")
tmux("set-option", "-g", "status", "off")
if LOAD:
    tmux("new-window", "-d", "-t", "s", "while :; do seq 1 200; done")
time.sleep(0.5)
pane = tmux("display", "-p", "-t", "s:0", "#{pane_id}").stdout.strip()

# Attached client on a pty, as `portenv attach` runs it.
pid, fd = pty.fork()
if pid == 0:
    os.execvpe("tmux", ["tmux", "-L", SOCK, "attach", "-t", "s:0"], ENV)
os.set_blocking(fd, False)


def drain(f, t=0.3):
    end = time.time() + t
    while time.time() < end:
        r, _, _ = select.select([f], [], [], 0.02)
        if r:
            try:
                os.read(f, 1 << 16)
            except OSError:
                pass


drain(fd, 1.0)
attached = []
for i in range(N):
    k = b"xyz"[i % 3:i % 3 + 1]
    t0 = time.perf_counter()
    os.write(fd, k)
    buf = b""
    while k not in buf:
        r, _, _ = select.select([fd], [], [], 1.0)
        if not r:
            break
        buf += os.read(fd, 1 << 16)
    attached.append((time.perf_counter() - t0) * 1000)
    os.write(fd, b"\x7f")
    drain(fd, 0.01)
os.kill(pid, 9)

# Control-mode client: input as send-keys -H, echo from %output.
c = subprocess.Popen(["tmux", "-L", SOCK, "-C", "attach", "-t", "s:0"], env=ENV,
                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, bufsize=0)
cfd = c.stdout.fileno()
os.set_blocking(cfd, False)
c.stdin.write(b"refresh-client -C 80x24\n")
drain(cfd, 1.0)
control = []
prefix = f"%output {pane} ".encode()
for i in range(N):
    k = b"xyz"[i % 3:i % 3 + 1]
    t0 = time.perf_counter()
    c.stdin.write(f"send-keys -t {pane} -H {k[0]:02x}\n".encode())
    buf = b""
    while True:
        r, _, _ = select.select([cfd], [], [], 1.0)
        if not r:
            break
        buf += os.read(cfd, 1 << 16)
        if any(l.startswith(prefix) and k in l[len(prefix):] for l in buf.split(b"\n")):
            break
    control.append((time.perf_counter() - t0) * 1000)
    c.stdin.write(f"send-keys -t {pane} -H 7f\n".encode())
    drain(cfd, 0.01)
c.stdin.close()
tmux("kill-server")

print(f"tmux {subprocess.run(['tmux', '-V'], capture_output=True, text=True).stdout.strip()}, "
      f"{'another tab printing continuously' if LOAD else 'idle'}")
report("attached client (today)", attached)
report("control mode (send-keys -H)", control)
print(f"control mode - attached at p95: {pct(control, 95) - pct(attached, 95):+.2f} ms")
