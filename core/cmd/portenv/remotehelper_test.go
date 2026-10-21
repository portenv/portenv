// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// git-remote-portenv (R-0391): git's remote helper for pushes from the
// workspace. It asks work's socket for the remote's refs, bundles what's
// pushed, and hands the bundle and the updates to the socket.

// fakeWorkSocket plays the helper: list answers with refs; push records
// the call and its bundle and answers with statuses.
type fakeWorkSocket struct {
	mu       sync.Mutex
	refs     []*agentv1.RemoteRef
	pushes   []*agentv1.PushRefs
	bundles  [][]byte
	statuses func(*agentv1.PushRefs) []*agentv1.PushRefStatus
	refuse   string
}

func (f *fakeWorkSocket) serve(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return sock
}

func (f *fakeWorkSocket) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	line, _ := br.ReadBytes('\n')
	var call agentv1.TeamCallsResponse
	_ = protojson.Unmarshal(line, &call)
	reply := &agentv1.TeamCallsRequest{Ok: true}
	f.mu.Lock()
	switch {
	case f.refuse != "":
		reply = &agentv1.TeamCallsRequest{Line: f.refuse}
	case call.GetPushList() != nil:
		reply.Result = &agentv1.TeamCallsRequest_PushList{PushList: &agentv1.PushListResult{Refs: f.refs}}
	case call.GetPush() != nil:
		p := call.GetPush()
		b, _ := io.ReadAll(io.LimitReader(br, p.GetBundleBytes()))
		f.pushes = append(f.pushes, p)
		f.bundles = append(f.bundles, b)
		var st []*agentv1.PushRefStatus
		if f.statuses != nil {
			st = f.statuses(p)
		} else {
			for _, u := range p.GetUpdates() {
				st = append(st, &agentv1.PushRefStatus{Dst: u.GetDst(), Ok: true})
			}
		}
		reply.Result = &agentv1.TeamCallsRequest_Push{Push: &agentv1.PushResult{Statuses: st}}
	}
	f.mu.Unlock()
	out, _ := protojson.Marshal(reply)
	_, _ = c.Write(append(out, '\n'))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- the test's own setup
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// talk runs the remote helper over a scripted conversation, as git would.
func talk(t *testing.T, repo, sock, script string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runRemoteHelper([]string{"origin", "portenv::github"}, strings.NewReader(script), &out, &errOut, remoteHelperEnv{gitDir: repo, socket: sock})
	return out.String() + errOut.String(), code
}

func localRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := filepath.Join(t.TempDir(), "p")
	git(t, "", "init", "-q", "-b", "main", dir)
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "one")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

func TestRemoteHelperSaysWhatItCanDo(t *testing.T) {
	repo, _ := localRepo(t)
	out, code := talk(t, repo, (&fakeWorkSocket{}).serve(t), "capabilities\n\n")
	if code != 0 || out != "fetch\npush\noption\n\n" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestRemoteHelperListsTheRemote(t *testing.T) {
	repo, head := localRepo(t)
	f := &fakeWorkSocket{refs: []*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: head}}}
	out, code := talk(t, repo, f.serve(t), "list for-push\n\n")
	if code != 0 || out != head+" refs/heads/main\n\n" {
		t.Fatalf("%d %q", code, out)
	}
}

// A push: the updates, and a bundle of the new objects, thin against what
// the remote has.
func TestRemoteHelperPushes(t *testing.T) {
	repo, old := localRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "b"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "two")
	n := git(t, repo, "rev-parse", "HEAD")
	f := &fakeWorkSocket{refs: []*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: old}}}
	out, code := talk(t, repo, f.serve(t), "list for-push\npush refs/heads/main:refs/heads/main\npush +refs/heads/main:refs/heads/other\npush :refs/heads/gone\n\n\n")
	if code != 0 || !strings.HasSuffix(out, "ok refs/heads/main\nok refs/heads/other\nok refs/heads/gone\n\n") {
		t.Fatalf("%d %q", code, out)
	}
	if len(f.pushes) != 1 {
		t.Fatalf("pushes %v", f.pushes)
	}
	u := f.pushes[0].GetUpdates()
	if len(u) != 3 || u[0].GetNewSha() != n || u[0].GetOldSha() != old || u[0].GetForce() ||
		u[1].GetDst() != "refs/heads/other" || !u[1].GetForce() || u[1].GetOldSha() != "" ||
		u[2].GetDst() != "refs/heads/gone" || u[2].GetNewSha() != "" {
		t.Fatalf("updates %v", u)
	}
	b := f.bundles[0]
	if int64(len(b)) != f.pushes[0].GetBundleBytes() || !bytes.HasPrefix(b, []byte("# v2 git bundle\n-"+old)) || !bytes.Contains(b, []byte(n+" refs/portenv-push/0\n")) {
		t.Fatalf("bundle header: %q", b[:min(200, len(b))])
	}
	// The temporary refs are gone.
	if refs := git(t, repo, "for-each-ref", "refs/portenv-push"); refs != "" {
		t.Fatalf("left %q", refs)
	}
}

// Refusals come back as git's own words.
func TestRemoteHelperSaysWhatWasRefused(t *testing.T) {
	repo, old := localRepo(t)
	f := &fakeWorkSocket{refs: []*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: old}},
		statuses: func(p *agentv1.PushRefs) []*agentv1.PushRefStatus {
			return []*agentv1.PushRefStatus{{Dst: "refs/heads/x", Reason: "non-fast-forward"}}
		}}
	out, code := talk(t, repo, f.serve(t), "list for-push\npush refs/heads/main:refs/heads/x\n\n\n")
	if code != 0 || !strings.Contains(out, "error refs/heads/x non-fast-forward\n") {
		t.Fatalf("%d %q", code, out)
	}
	// The socket's refusal (an agent, or no sign-in) is the plain line, and
	// the push fails.
	f.refuse = "Only you can push from this workspace. Agents can’t push."
	out, code = talk(t, repo, f.serve(t), "list for-push\n\n")
	if code == 0 || !strings.Contains(out, "Only you can push from this workspace.") {
		t.Fatalf("%d %q", code, out)
	}
}

// With git itself: git push through portenv::github.
func TestGitPushThroughThePortenvRemote(t *testing.T) {
	repo, old := localRepo(t)
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "git-remote-portenv"), ".") // #nosec G204 -- this package
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	f := &fakeWorkSocket{refs: []*agentv1.RemoteRef{{Name: "refs/heads/main", Sha: old}}}
	sock := f.serve(t)
	git(t, repo, "remote", "add", "origin", "https://github.com/acme/api")
	git(t, repo, "config", "remote.origin.pushurl", "portenv::github")
	if err := os.WriteFile(filepath.Join(repo, "c"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "three")
	cmd := exec.Command("git", "push", "origin", "main") // #nosec G204 -- the test's own push
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "PORTENV_PUSH_SOCKET="+sock, "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "main -> main") {
		t.Fatalf("%v %s", err, out)
	}
	j, _ := json.Marshal(f.pushes)
	if len(f.pushes) != 1 || !strings.Contains(string(j), "refs/heads/main") {
		t.Fatalf("%s", j)
	}
}
