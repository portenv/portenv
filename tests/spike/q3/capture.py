# SPDX-License-Identifier: Apache-2.0
#
# 1.2 spike, question 3 (ADR 0017): records what a terminal in the app would
# receive from a box's tmux session, two ways, for the same commands:
#
#   A  an attached tmux client (today's `portenv attach`), with tmux's
#      allow-passthrough on and every OSC 133 mark also sent wrapped for
#      passthrough, since tmux drops a bare one;
#   C  each pane's own output, from a tmux control-mode client (%output), as
#      the box agent already reads it, one stream per tab.
#
# Run inside the toolbox image (real tmux, the real bash integration):
#   docker run --rm -v "$PWD/tests/spike/q3:/q3" --entrypoint python3 \
#     portenv/toolbox-node:<tag> /q3/capture.py /q3/fixtures
# Each scenario is written as JSON lines: {"t":"data","path":"A"|"C",
# "pane":"%N","b64":…} and {"t":"resize","cols":…,"rows":…}, in order.
import base64, fcntl, json, os, pty, select, struct, subprocess, sys, termios, time

OUT = sys.argv[1]
ENV = dict(os.environ, LANG="C.UTF-8", TERM="xterm-256color", HOME="/tmp/q3home")
INTEGRATION = "/usr/share/portenv/shell/osc133.bash"
RC = "/tmp/q3home/.bashrc"


def write_rc():
    os.makedirs("/tmp/q3home", exist_ok=True)
    src = open(INTEGRATION).read()
    # Path A: each mark is also sent wrapped for tmux's passthrough. The bare
    # one stays, so control mode (path C, and the agent) still sees it.
    wrapped = src.replace("printf '\\033]133;D;%s\\007' \"$ec\"",
                          "printf '\\033]133;D;%s\\007\\033Ptmux;\\033\\033]133;D;%s\\007\\033\\\\' \"$ec\" \"$ec\"")
    wrapped = wrapped.replace("printf '\\033]133;A\\007'",
                              "printf '\\033]133;A\\007\\033Ptmux;\\033\\033]133;A\\007\\033\\\\'")
    wrapped = wrapped.replace("'\\[\\033]133;B\\007\\]'",
                              "'\\[\\033]133;B\\007\\033Ptmux;\\033\\033]133;B\\007\\033\\\\\\]'")
    wrapped = wrapped.replace("'\\[\\033]133;C\\007\\]'",
                              "'\\[\\033]133;C\\007\\033Ptmux;\\033\\033]133;C\\007\\033\\\\\\]'")
    assert wrapped.count("Ptmux") == 4, "the integration changed; update the wrapping"
    with open(RC, "w") as f:
        f.write("PS1='P\\#\\$ '\n")
        f.write(wrapped.replace("[[ $- == *i* ]]", "true"))


class Capture:
    def __init__(self, name):
        self.sock = "q3-" + name
        self.records = []
        self.size = (80, 24)
        self.tmux("new-session", "-d", "-s", "s", "-x", "80", "-y", "24", "bash --rcfile " + RC + " -i")
        for opt in [("status", "off"), ("allow-passthrough", "on"), ("history-limit", "2000"),
                    ("window-size", "latest")]:
            self.tmux("set-option", "-g", *opt)
        # A: an attached client on a pty, like the app's terminal.
        self.apid, self.afd = pty.fork()
        if self.apid == 0:
            os.execvpe("tmux", ["tmux", "-L", self.sock, "attach", "-t", "s"], ENV)
        self.winsize(80, 24)
        # C: a control-mode client, as the agent runs one.
        self.c = subprocess.Popen(["tmux", "-L", self.sock, "-C", "attach", "-t", "s"], env=ENV,
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        os.set_blocking(self.c.stdout.fileno(), False)
        self.cbuf = b""
        self.ctl("refresh-client -C 80x24")
        self.drain(1.0)

    def tmux(self, *args):
        return subprocess.run(["tmux", "-L", self.sock, *args], env=ENV, capture_output=True, text=True)

    def ctl(self, line):
        self.c.stdin.write((line + "\n").encode())
        self.c.stdin.flush()

    def winsize(self, cols, rows):
        fcntl.ioctl(self.afd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def drain(self, t):
        end = time.time() + t
        while time.time() < end:
            r, _, _ = select.select([self.afd, self.c.stdout], [], [], 0.05)
            if self.afd in r:
                try:
                    data = os.read(self.afd, 65536)
                    self.records.append({"t": "data", "path": "A", "pane": "", "b64": base64.b64encode(data).decode()})
                except OSError:
                    pass
            if self.c.stdout in r:
                chunk = self.c.stdout.read() or b""
                self.cbuf += chunk
                while b"\n" in self.cbuf:
                    line, self.cbuf = self.cbuf.split(b"\n", 1)
                    if line.startswith(b"%output "):
                        _, pane, data = line.split(b" ", 2)
                        self.records.append({"t": "data", "path": "C", "pane": pane.decode(),
                                             "b64": base64.b64encode(decode(data)).decode()})

    def run(self, cmd, wait=0.35):
        self.tmux("send-keys", "-t", "s", "-l", cmd)
        self.tmux("send-keys", "-t", "s", "Enter")
        self.drain(wait)

    def keys(self, *keys, wait=0.35):
        self.tmux("send-keys", "-t", "s", *keys)
        self.drain(wait)

    def resize(self, cols, rows):
        self.records.append({"t": "resize", "cols": cols, "rows": rows})
        self.winsize(cols, rows)
        os.kill(self.apid, 28)  # SIGWINCH to the attached client
        self.ctl(f"refresh-client -C {cols}x{rows}")
        self.drain(0.8)

    def save(self, name):
        self.tmux("kill-server")
        self.drain(0.3)
        with open(os.path.join(OUT, name + ".jsonl"), "w") as f:
            for r in self.records:
                f.write(json.dumps(r) + "\n")


def decode(b):
    out, i = bytearray(), 0
    while i < len(b):
        if b[i] == 0x5C and i + 3 < len(b) + 0 and b[i + 1:i + 4].isdigit():
            out.append(int(b[i + 1:i + 4], 8))
            i += 4
        else:
            out.append(b[i])
            i += 1
    return bytes(out)


def commands(c, first, n):
    for i in range(first, first + n):
        c.run(f"seq 1 3 | sed s/^/out-{i}-/")


def main():
    os.makedirs(OUT, exist_ok=True)
    write_rc()

    c = Capture("scroll")              # output scrolls well past the screen
    commands(c, 1, 30)
    c.save("scroll")

    c = Capture("long")                # one long output between short ones
    commands(c, 1, 5)
    c.run("seq 1 300 | sed s/^/long-/", wait=1.0)
    commands(c, 6, 5)
    c.save("long")

    c = Capture("fullscreen")          # a pager on the alternate screen
    commands(c, 1, 5)
    c.run("seq 1 200 | less", wait=0.8)
    c.keys("q")
    commands(c, 6, 5)
    c.save("fullscreen")

    c = Capture("clear")               # clear between commands
    commands(c, 1, 5)
    c.run("clear")
    commands(c, 6, 5)
    c.save("clear")

    c = Capture("tabs")                # a second tab, then back
    commands(c, 1, 5)
    c.tmux("new-window", "-t", "s", "bash --rcfile " + RC + " -i")
    c.drain(0.8)
    commands(c, 101, 5)
    c.tmux("select-window", "-t", "s:0")
    c.drain(0.8)
    commands(c, 6, 5)
    c.save("tabs")

    c = Capture("resize")              # the window narrows, then widens
    commands(c, 1, 10)
    c.resize(60, 20)
    commands(c, 11, 5)
    c.resize(100, 30)
    commands(c, 16, 5)
    c.save("resize")


main()
