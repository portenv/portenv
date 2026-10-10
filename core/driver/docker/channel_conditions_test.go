// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/agent"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// TestChannelConditions checks ADR 0010's conditions against a real box:
// the channel's files never reach a save and are gone once the API process
// has loaded them; root in the box cannot read the key or token; and an
// impostor on the agent's port never receives the token or a password.
func TestChannelConditions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	d, id, ch, sh := startChannelBox(t, ctx, "conditions")
	// Condition 2: the secrets came on stdin and were never written: the
	// token is in none of the places it could land (files where anything
	// writes, channel files, processes' environments and command lines, the
	// box's log), init holds no stdin any more, and the API process runs
	// as portenv-agent. The whole root file system is scanned nightly
	// (TestChannelTokenNotOnDisk); the control proves each targeted search
	// finds what it looks for.
	checkTokenNotWritten(ctx, t, d, id, ch.Token)
	checkTheSearchFindsIt(ctx, t, d, id, sh)
	// Root in the box gets nothing from init's stdin: it is /dev/null, or
	// not even readable (the container's init is not dumpable), and reading
	// it never yields the token.
	if got := sh("", "readlink /proc/1/fd/0"); got != "" && got != "/dev/null" {
		t.Errorf("init's fd 0 is %q, want /dev/null or unreadable", got)
	}
	if got := sh("", "timeout 2 cat /proc/1/fd/0 2>/dev/null | head -c 65536"); strings.Contains(got, ch.Token) {
		t.Error("root read the token from init's stdin")
	}
	servePid := sh("", "pgrep -f '^portenv-agent serve'")
	if uid := sh("", "ps -o uid= -p "+servePid); uid != "991" {
		t.Errorf("API process uid %q, want 991 (portenv-agent)", uid)
	}

	// ADR 0014: only the engine's attach can re-key. Root in the box writing
	// a valid secrets line to the API process's stdin through /proc is
	// refused (the process is non-dumpable and no box has CAP_SYS_PTRACE),
	// and the injected secrets never work. The control: the same write to
	// an ordinary root process's stdin pipe gets through.
	{
		forged, err := agent.NewChannelSecrets()
		if err != nil {
			t.Fatal(err)
		}
		line, _ := forged.Line()
		write := func(pid string) string {
			return sh("", "printf '%s' '"+strings.TrimSpace(string(line))+"\n' > /proc/"+pid+"/fd/0 2>/dev/null && echo WROTE || echo REFUSED")
		}
		ctl := sh("", "rm -f /tmp/ctl /tmp/ctlfifo; mkfifo /tmp/ctlfifo; sh -c 'read l; echo \"$l\" > /tmp/ctl' <> /tmp/ctlfifo >/dev/null 2>&1 & echo $!")
		if got := write(ctl); got != "WROTE" {
			t.Fatalf("control: writing to an ordinary process's stdin: %s (the probe proves nothing)", got)
		}
		for range 20 {
			if strings.Contains(sh("", "cat /tmp/ctl 2>/dev/null"), forged.Token) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !strings.Contains(sh("", "cat /tmp/ctl 2>/dev/null"), forged.Token) {
			t.Fatal("control: the ordinary process never received the line")
		}
		if got := write(servePid); got != "REFUSED" {
			t.Errorf("root in the box wrote to the API process's stdin: %s", got)
		}
		time.Sleep(500 * time.Millisecond)
		works := func(cert []byte, token string) bool {
			conn, err := agent.DialChannel(ch.Dial, cert, token)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_, err = agentv1.NewAgentServiceClient(conn).GetVersion(cctx, &agentv1.GetVersionRequest{})
			return err == nil
		}
		if works(forged.CertPEM, forged.Token) {
			t.Error("secrets injected from inside the box took over the channel")
		}
		if !works(ch.CertPEM, ch.Token) {
			t.Error("the real secrets stopped working after the injection attempt")
		}
	}

	// Processes started for users carry no capabilities: CapPrm, CapEff and
	// CapAmb are zero for the terminal's shell, the tmux server and every
	// other process of work; ambient sets are zero for the API process too.
	{
		conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
		if err != nil {
			t.Fatal(err)
		}
		term, err := agentv1.NewAgentServiceClient(conn).Terminal(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = term.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Open{Open: &agentv1.TerminalOpen{Session: "main", Size: &agentv1.TerminalSize{Cols: 200, Rows: 50}}}})
		_ = term.Send(&agentv1.TerminalRequest{Msg: &agentv1.TerminalRequest_Input{Input: []byte(
			"for p in $$ $(pgrep -u work); do grep -E '^Cap(Prm|Eff|Amb)' /proc/$p/status; done | sort | uniq -c | sed 's/^/capcheck /'; echo capcheck-$((40+2))-done\r")}})
		var out strings.Builder
		for !strings.Contains(out.String(), "capcheck-42-done") {
			r, err := term.Recv()
			if err != nil {
				t.Fatalf("terminal: %v; output %q", err, out.String())
			}
			out.Write(r.GetOutput())
		}
		_ = term.CloseSend()
		_ = conn.Close()
		lines := 0
		for _, line := range strings.Split(out.String(), "\n") {
			if !strings.Contains(line, "capcheck ") || strings.Contains(line, "sed") {
				continue
			}
			lines++
			if !strings.HasSuffix(strings.TrimSpace(line), "0000000000000000") {
				t.Errorf("a process started for work has capabilities: %s", strings.TrimSpace(line))
			}
		}
		if lines < 3 {
			t.Fatalf("read %d capability lines through the terminal, want CapPrm, CapEff and CapAmb: %q", lines, out.String())
		}
	}
	if got := sh("", "grep '^CapAmb' /proc/"+servePid+"/status | awk '{print $2}'"); got != "0000000000000000" {
		t.Errorf("the API process has ambient capabilities %s", got)
	}

	// Anything restic starts (its ssh for SFTP storage) carries no
	// capabilities either: restic's own file capabilities stay with restic.
	// The agent refuses restic's -o (so nothing can swap ssh for another
	// command through it), so restic is started here as the agent starts it:
	// as portenv-sync, with empty inheritable and ambient sets (the channel
	// run in TestChannelPasswordIsUnreadable checks restic starts that way).
	// sftp.command stands in for ssh and records its capability sets.
	{
		sh("", "rm -f /tmp/restic-child-caps; RESTIC_PASSWORD=unused setpriv --reuid=990 --regid=990 --clear-groups --inh-caps=-all "+
			agent.ResticBinary+" --no-cache --repo sftp:nowhere:/repo -o sftp.command=\"sh -c 'grep -E ^Cap /proc/self/status > /tmp/restic-child-caps; exit 1'\" snapshots >/dev/null 2>&1 || true")
		caps := sh("", "cat /tmp/restic-child-caps 2>/dev/null")
		if !strings.Contains(caps, "CapEff") {
			t.Fatalf("restic did not start its child (no capability record): %q", caps)
		}
		for _, line := range strings.Split(caps, "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && (f[0] == "CapPrm:" || f[0] == "CapEff:" || f[0] == "CapAmb:") && f[1] != "0000000000000000" {
				t.Errorf("a process restic started has %s %s", f[0], f[1])
			}
		}
	}

	// Condition 1: a save right after start contains none of them.
	conn, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	c := agentv1.NewAgentServiceClient(conn)
	password := "portenv-" + string(id)
	input, _ := json.Marshal(map[string]string{"password": password})
	sh("", "install -d -o 990 -g 990 -m 0700 /var/tmp/repo; id -u lane >/dev/null 2>&1 || useradd --uid 2000 --no-create-home lane")
	for _, args := range [][]string{{"init"}, {"backup", "/home"}} {
		if res, err := c.RunRestic(ctx, &agentv1.RunResticRequest{Args: append([]string{"--repo", "/var/tmp/repo"}, args...), Input: input}); err != nil || res.GetExitCode() != 0 {
			t.Fatalf("restic %v: %v %s", args, err, res.GetStderr())
		}
	}
	// Restore the save into a scratch directory and look for the files and
	// their contents there (restic's allow-list has no ls).
	sh("", "install -d -o 990 -g 990 -m 0700 /var/tmp/restored")
	if res, err := c.RunRestic(ctx, &agentv1.RunResticRequest{Args: []string{"--repo", "/var/tmp/repo", "restore", "latest", "--target", "/var/tmp/restored"}, Input: input}); err != nil || res.GetExitCode() != 0 {
		t.Fatalf("restic restore: %v %s", err, res.GetStderr())
	}
	if got := sh("", "find /var/tmp/restored \\( -name token -o -name cert.pem -o -name key.pem -o -path '*portenv/agent*' \\) | head -3"); got != "" {
		t.Errorf("the save contains channel files: %s", got)
	}
	if got := sh("", "grep -rlF "+ch.Token+" /var/tmp/restored 2>/dev/null | head -1"); got != "" {
		t.Errorf("the save contains the token: %s", got)
	}
	if got := sh("", "ls /var/tmp/restored/home | tr '\\n' ' '"); !strings.Contains(got, "work") {
		t.Fatalf("the restored save does not hold the home (%q): the check would prove nothing", got)
	}
	if got := sh("", "grep -rlF "+ch.Token+" /home 2>/dev/null | head -1"); got != "" {
		t.Errorf("the token is in a file under /home: %s", got)
	}

	// Condition 3: root in the box, and the API process's own uid, cannot
	// read the token from files, /proc or the environment.
	sh("", "cat > /tmp/memprobe.py <<'PY'\n"+memProbe+"\nPY")
	for _, attacker := range []string{"root", "root-as-agent", "lane"} {
		for _, what := range []string{"environ", "fd3", "cmdline", "mem", "ptrace"} {
			cmd := "python3 /tmp/memprobe.py " + servePid + " " + ch.Token + " " + what
			switch attacker {
			case "root-as-agent":
				cmd = "setpriv --reuid=991 --regid=991 --clear-groups " + cmd
			case "lane":
				cmd = "setpriv --reuid=2000 --regid=2000 --clear-groups " + cmd
			}
			if got := sh("", cmd); got != "DENIED" {
				t.Errorf("%s reading the token through %s: %s", attacker, what, got)
			}
		}
	}
	if got := sh("", "ls -l /proc/"+servePid+"/fd 2>/dev/null | grep -c 'portenv/agent' || true"); got != "0" {
		t.Errorf("the API process still has channel files open (%s)", got)
	}

	// Condition 3: root kills the agent and starts an impostor with a
	// self-made certificate on the same port. The client refuses it: the
	// impostor never sees the token or a password.
	sh("", "pkill -f '^portenv-agent serve'; sleep 1")
	if got := sh("", "pgrep -f '^portenv-agent serve' || echo none"); got != "none" {
		t.Fatalf("the agent was restarted without its secrets: %s", got)
	}
	sh("", "openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=portenv-agent -days 1 -keyout /tmp/i.key -out /tmp/i.crt 2>/dev/null; "+
		"setsid sh -c 'openssl s_server -accept 7700 -cert /tmp/i.crt -key /tmp/i.key -quiet > /tmp/impostor.log 2>&1' >/dev/null 2>&1 & sleep 1")
	if raw, err := ch.Dial(ctx); err != nil {
		t.Fatalf("the impostor is not listening: %v", err)
	} else {
		_ = raw.Close()
	}
	conn2, err := agent.DialChannel(ch.Dial, ch.CertPEM, ch.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn2.Close() }()
	_, err = agentv1.NewAgentServiceClient(conn2).RunRestic(ctx, &agentv1.RunResticRequest{Args: []string{"--repo", "/var/tmp/repo", "snapshots"}, Input: input})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "the certificate is not this box's") {
		t.Fatalf("against the impostor: %v, want the pinned certificate to refuse it", err)
	}
	log := sh("", "cat /tmp/impostor.log")
	if strings.Contains(log, ch.Token) || strings.Contains(log, password) {
		t.Fatal("the impostor received the token or the password")
	}
}
