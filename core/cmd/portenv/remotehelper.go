// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// git-remote-portenv (R-0391): git's remote helper for pushes from the
// workspace, the portenv CLI under a second name. The clone sets
// remote.origin.pushurl to portenv::github, so a plain git push runs it.
// It speaks git's remote-helper protocol (capabilities push and option):
// list for-push asks work's own socket for the remote's branches and tags;
// push bundles the pushed objects (temporary refs refs/portenv-push/N,
// --not what the remote has) and streams the bundle, after one line of
// updates, to the socket. The helper takes it only from work (SO_PEERCRED);
// portenvd pushes it from the Mac with the owner's sign-in, which never
// enters the workspace.

// remoteHelperName is the name git runs it by.
const remoteHelperName = "git-remote-portenv"

// defaultPushSocket is work's own socket (the helper's, group work).
const defaultPushSocket = "/run/portenv-team/work/portenv.sock"

// pushWait is how long a push may wait for the owner's answer in Portenv
// and for GitHub.
const pushWait = 10 * time.Minute

type remoteHelperEnv struct {
	// gitDir is the folder git runs in; empty: this one (tests name theirs).
	gitDir string
	socket string
}

func remoteHelperFromEnv() remoteHelperEnv {
	// git runs it in the repository, with GIT_DIR set: git's own commands
	// here find the repository the same way.
	e := remoteHelperEnv{socket: os.Getenv("PORTENV_PUSH_SOCKET")}
	if e.socket == "" {
		e.socket = defaultPushSocket
	}
	return e
}

type pushSpec struct {
	src, dst string
	force    bool
}

// runRemoteHelper is git-remote-portenv REMOTE URL, talking with git on
// stdin and stdout. The URL is ignored: portenvd pushes only to the
// workspace's own repository.
func runRemoteHelper(args []string, stdin io.Reader, stdout, stderr io.Writer, env remoteHelperEnv) int {
	if len(args) < 1 {
		_, _ = fmt.Fprintln(stderr, "usage: git-remote-portenv REMOTE [URL] (run by git)")
		return 2
	}
	fail := func(line string) int {
		_, _ = fmt.Fprintln(stderr, "portenv:", line)
		return 1
	}
	in := bufio.NewReader(stdin)
	remote := map[string]string{}
	var batch []pushSpec
	for {
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return 0
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "capabilities":
			_, _ = io.WriteString(stdout, "push\noption\n\n")
		case strings.HasPrefix(line, "option "):
			_, _ = io.WriteString(stdout, "unsupported\n")
		case line == "list" || line == "list for-push":
			reply, err := pushCall(env.socket, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_PushList{PushList: &agentv1.PushList{}}}, "", 0)
			if err != nil {
				return fail(err.Error())
			}
			var out strings.Builder
			for _, r := range reply.GetPushList().GetRefs() {
				remote[r.GetName()] = r.GetSha()
				fmt.Fprintf(&out, "%s %s\n", r.GetSha(), r.GetName())
			}
			out.WriteString("\n")
			_, _ = io.WriteString(stdout, out.String())
		case strings.HasPrefix(line, "push "):
			spec := strings.TrimPrefix(line, "push ")
			p := pushSpec{}
			if p.force = strings.HasPrefix(spec, "+"); p.force {
				spec = spec[1:]
			}
			var ok bool
			if p.src, p.dst, ok = strings.Cut(spec, ":"); !ok || p.dst == "" {
				return fail("git asked for a push Portenv can’t read: " + line)
			}
			batch = append(batch, p)
		case line == "":
			if len(batch) == 0 {
				return 0
			}
			out, code := pushBatch(batch, remote, env)
			if code != 0 {
				return fail(out)
			}
			_, _ = io.WriteString(stdout, out)
			batch = nil
		default:
			return fail("git asked for something Portenv doesn’t do: " + line)
		}
	}
}

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...) // #nosec G204 -- git, with arguments made here
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(errb.String()), err
	}
	return strings.TrimSpace(out.String()), nil
}

// pushBatch makes the bundle and hands the push to the socket; it returns
// git's status lines, or a plain line and a non-zero code.
func pushBatch(batch []pushSpec, remote map[string]string, env remoteHelperEnv) (string, int) {
	updates := make([]*agentv1.PushUpdate, 0, len(batch))
	var tempRefs []string
	defer func() {
		for _, r := range tempRefs {
			_, _ = gitIn(env.gitDir, "update-ref", "-d", r)
		}
	}()
	for i, p := range batch {
		u := &agentv1.PushUpdate{Dst: p.dst, OldSha: remote[p.dst], Force: p.force}
		if p.src != "" {
			sha, err := gitIn(env.gitDir, "rev-parse", "--verify", "--quiet", p.src)
			if err != nil || sha == "" {
				return fmt.Sprintf("%s isn’t something git can push", p.src), 1
			}
			u.NewSha = sha
			ref := "refs/portenv-push/" + strconv.Itoa(i)
			if out, err := gitIn(env.gitDir, "update-ref", ref, sha); err != nil {
				return "Portenv couldn’t prepare the push: " + out, 1
			}
			tempRefs = append(tempRefs, ref)
		}
		updates = append(updates, u)
	}
	var bundle string
	var size int64
	if len(tempRefs) > 0 {
		f, err := os.CreateTemp("", "portenv-push-*.bundle")
		if err != nil {
			return "Portenv couldn’t prepare the push: " + err.Error(), 1
		}
		bundle = f.Name()
		_ = f.Close()
		defer func() { _ = os.Remove(bundle) }()
		args := append([]string{"bundle", "create", "--quiet", bundle}, tempRefs...)
		var nots []string
		for _, sha := range remote {
			if _, err := gitIn(env.gitDir, "cat-file", "-e", sha); err == nil {
				nots = append(nots, sha)
			}
		}
		if len(nots) > 0 {
			args = append(append(args, "--not"), nots...)
		}
		out, err := gitIn(env.gitDir, args...)
		switch {
		case err != nil && strings.Contains(out, "empty bundle"):
			// Everything is on the remote already (a new branch at a
			// commit it has): no objects to send.
			bundle = ""
		case err != nil:
			return "Portenv couldn’t bundle the push: " + out, 1
		default:
			fi, err := os.Stat(bundle)
			if err != nil {
				return "Portenv couldn’t bundle the push: " + err.Error(), 1
			}
			size = fi.Size()
		}
	}
	reply, err := pushCall(env.socket, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Push{Push: &agentv1.PushRefs{Updates: updates, BundleBytes: size}}}, bundle, size)
	if err != nil {
		return err.Error(), 1
	}
	got := map[string]*agentv1.PushRefStatus{}
	for _, s := range reply.GetPush().GetStatuses() {
		got[s.GetDst()] = s
	}
	var out strings.Builder
	for _, u := range updates {
		switch s := got[u.GetDst()]; {
		case s == nil:
			fmt.Fprintf(&out, "error %s no answer from Portenv\n", u.GetDst())
		case s.GetOk():
			fmt.Fprintf(&out, "ok %s\n", u.GetDst())
		default:
			reason := strings.Join(strings.Fields(s.GetReason()), " ")
			if reason == "" {
				reason = "failed"
			}
			fmt.Fprintf(&out, "error %s %s\n", u.GetDst(), reason)
		}
	}
	out.WriteString("\n")
	return out.String(), 0
}

// pushCall sends one call to work's socket, then the bundle's bytes, and
// reads the answer.
func pushCall(sock string, call *agentv1.TeamCallsResponse, bundle string, size int64) (*agentv1.TeamCallsRequest, error) {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, errors.New("Portenv isn’t reachable from here, so this push can’t go. Push from your own terminal in Portenv’s window") //nolint:staticcheck // ST1005: a plain line
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(pushWait))
	b, err := protojson.Marshal(call)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return nil, errors.New("Portenv refused the push") //nolint:staticcheck // ST1005: a plain line
	}
	if bundle != "" {
		f, err := os.Open(filepath.Clean(bundle))
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(conn, io.LimitReader(f, size))
		_ = f.Close()
		if err != nil {
			return nil, errors.New("the push’s bundle couldn’t be handed to Portenv")
		}
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	line, err := bufio.NewReaderSize(io.LimitReader(conn, maxTeamReply+1), 64<<10).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("no answer from Portenv")
	}
	var r agentv1.TeamCallsRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(line, &r); err != nil {
		return nil, errors.New("Portenv’s answer couldn’t be read") //nolint:staticcheck // ST1005: a plain line
	}
	if !r.GetOk() {
		if r.GetLine() == "" {
			return nil, errors.New("Portenv refused the push, without saying why") //nolint:staticcheck // ST1005: a plain line
		}
		return nil, errors.New(r.GetLine())
	}
	return &r, nil
}
