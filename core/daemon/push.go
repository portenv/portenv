// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/githubrepo"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// Push from the workspace (R-0391). git-remote-portenv's list and push
// arrive as the owner's calls from work's own socket (the helper checked
// SO_PEERCRED). portenvd asks the app on the workspace's window for the
// owner's GitHub sign-in, and for the owner's yes on the workspace's first
// push, a force or a delete (WatchPushes, AnswerPush); then it pushes the
// bundle's pack and the ref updates from the Mac with go-git, to the
// workspace's own repository only. The sign-in is used for that call
// alone: never kept, logged or sent into the workspace.

const (
	githubRepoFile    = "github-repo"
	pushesAllowedFile = "pushes-allowed"
)

// pushAnswerWait is how long a push waits for the app.
var pushAnswerWait = 80 * time.Second

// keepGitHubRepo records the workspace's own repository, at its clone.
func keepGitHubRepo(stateDir, url string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return writePrivateAtomic(filepath.Join(stateDir, githubRepoFile), []byte(url+"\n"))
}

func loadGitHubRepo(stateDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, githubRepoFile)) // #nosec G304 -- portenvd's own folder for the workspace
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// pushQuestion is one push waiting for the app.
type pushQuestion struct {
	q      *daemonv1.PushQuestion
	answer chan *daemonv1.AnswerPushRequest
}

// pushQuestions are each workspace's pending questions, and their
// watchers' wake-ups.
type pushQuestions struct {
	mu       sync.Mutex
	pending  map[string]map[string]*pushQuestion
	watchers map[string]map[chan struct{}]bool
}

func (s *Server) pushQs() *pushQuestions {
	s.pushOnce.Do(func() {
		s.pushes = &pushQuestions{pending: map[string]map[string]*pushQuestion{}, watchers: map[string]map[chan struct{}]bool{}}
	})
	return s.pushes
}

func (p *pushQuestions) wakeLocked(name string) {
	for ch := range p.watchers[name] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// pendingPushes is a workspace's questions now, oldest first.
func (s *Server) pendingPushes(name string) []*daemonv1.PushQuestion {
	p := s.pushQs()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*daemonv1.PushQuestion
	for _, q := range p.pending[name] {
		out = append(out, q.q)
	}
	return out
}

// ask puts a question to the app and waits for its answer.
func (s *Server) ask(ctx context.Context, name string, q *daemonv1.PushQuestion) (*daemonv1.AnswerPushRequest, error) {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	q.Id = hex.EncodeToString(raw[:])
	pq := &pushQuestion{q: q, answer: make(chan *daemonv1.AnswerPushRequest, 1)}
	p := s.pushQs()
	p.mu.Lock()
	if p.pending[name] == nil {
		p.pending[name] = map[string]*pushQuestion{}
	}
	p.pending[name][q.Id] = pq
	p.wakeLocked(name)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending[name], q.Id)
		p.wakeLocked(name)
		p.mu.Unlock()
	}()
	select {
	case a := <-pq.answer:
		return a, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(pushAnswerWait):
		return nil, errNoWindow
	}
}

var errNoWindow = errors.New("no answer")

// WatchPushes is the window's app's list of pushes waiting for it.
func (s *Server) WatchPushes(req *daemonv1.WatchPushesRequest, stream daemonv1.DaemonService_WatchPushesServer) error {
	c, err := callerFrom(stream.Context())
	if err != nil {
		return err
	}
	if c.kind != callerApp {
		return status.Error(codes.PermissionDenied, "only the owner's Portenv on this Mac answers pushes")
	}
	name := req.GetName()
	p := s.pushQs()
	wake := make(chan struct{}, 1)
	p.mu.Lock()
	if p.watchers[name] == nil {
		p.watchers[name] = map[chan struct{}]bool{}
	}
	p.watchers[name][wake] = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.watchers[name], wake)
		p.mu.Unlock()
	}()
	sent := map[string]bool{}
	for {
		now := map[string]bool{}
		for _, q := range s.pendingPushes(name) {
			now[q.GetId()] = true
			if !sent[q.GetId()] {
				if err := stream.Send(&daemonv1.WatchPushesResponse{Question: q}); err != nil {
					return err
				}
				sent[q.GetId()] = true
			}
		}
		for id := range sent {
			if !now[id] {
				if err := stream.Send(&daemonv1.WatchPushesResponse{Question: &daemonv1.PushQuestion{Id: id, Gone: true}}); err != nil {
					return err
				}
				delete(sent, id)
			}
		}
		select {
		case <-stream.Context().Done():
			return nil
		case <-wake:
		}
	}
}

// AnswerPush is the app's answer: the sign-in, and yes or no.
func (s *Server) AnswerPush(ctx context.Context, req *daemonv1.AnswerPushRequest) (*daemonv1.AnswerPushResponse, error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if c.kind != callerApp {
		return nil, status.Error(codes.PermissionDenied, "only the owner's Portenv on this Mac answers pushes")
	}
	p := s.pushQs()
	p.mu.Lock()
	q := p.pending[req.GetName()][req.GetId()]
	p.mu.Unlock()
	if q == nil {
		return nil, status.Error(codes.NotFound, "That push isn’t waiting anymore.")
	}
	select {
	case q.answer <- req:
	default:
	}
	return &daemonv1.AnswerPushResponse{}, nil
}

func pushRefuse(id, line string) *agentv1.TeamCallsRequest {
	return &agentv1.TeamCallsRequest{CallId: id, Line: line}
}

// handlePush answers the owner's list or push from work's socket.
func (s *Server) handlePush(ctx context.Context, name string, c *agentv1.TeamCallsResponse) *agentv1.TeamCallsRequest {
	id := c.GetCallId()
	if !c.GetOwner() || c.GetAgentId() != "" {
		return pushRefuse(id, "Only you can push from this workspace. Agents can’t push.")
	}
	box, err := s.env.LoadBox(name)
	if err != nil {
		return pushRefuse(id, "Portenv couldn’t find this workspace.")
	}
	state := filepath.Join(s.env.Dir, "state", box.ID)
	url, err := loadGitHubRepo(state)
	if err != nil || url == "" {
		return pushRefuse(id, "This workspace has no GitHub repository to push to.")
	}
	repo, err := githubrepo.Parse(url)
	if err != nil {
		return pushRefuse(id, "This workspace has no GitHub repository to push to.")
	}
	gitBase := s.githubGit
	if gitBase == "" {
		gitBase = "https://github.com"
	}
	remote := gitBase + "/" + repo.Owner + "/" + repo.Name
	client := &http.Client{Timeout: 10 * time.Minute}
	signInLine := "Your GitHub sign-in in Portenv has ended, or can’t push to " + repo.String() + ". Sign in again in Portenv, then push again."

	answer := func(q *daemonv1.PushQuestion) (string, string) {
		q.Repo = repo.String()
		a, err := s.ask(ctx, name, q)
		switch {
		case errors.Is(err, errNoWindow):
			return "", "Open " + name + "’s window in Portenv to push: Portenv asks you there."
		case err != nil:
			return "", "The push was stopped."
		case a.GetSignedOut():
			return "", "Sign in with GitHub in Portenv, then push again."
		case !a.GetAllow():
			return "", "You didn’t allow the push in Portenv."
		case a.GetGithubToken() == "":
			return "", "Sign in with GitHub in Portenv, then push again."
		}
		return a.GetGithubToken(), ""
	}

	// A fetch, pull or ls-remote (R-0392): only the sign-in is asked for,
	// and without one a public repository is read anonymously.
	fetchAnswer := func() (string, string) {
		q := &daemonv1.PushQuestion{Repo: repo.String(), List: true, Fetch: true}
		a, err := s.ask(ctx, name, q)
		switch {
		case errors.Is(err, errNoWindow):
			return "", "Open " + name + "\u2019s window in Portenv to fetch: Portenv asks you there."
		case err != nil:
			return "", "The fetch was stopped."
		case a.GetSignedOut():
			return "", ""
		case !a.GetAllow():
			return "", "You didn\u2019t allow the fetch in Portenv."
		}
		return a.GetGithubToken(), ""
	}
	fetchSignIn := func(token string) string {
		if token == "" {
			return "This repository needs a sign-in. Sign in with GitHub in Portenv, then fetch again."
		}
		return "Your GitHub sign-in in Portenv has ended, or can\u2019t read " + repo.String() + ". Sign in again in Portenv, then fetch again."
	}

	if c.GetPushList() != nil && c.GetPushList().GetFetch() {
		token, line := fetchAnswer()
		if line != "" {
			return pushRefuse(id, line)
		}
		refs, head, err := githubrepo.ListForFetch(ctx, client, remote, token)
		if errors.Is(err, githubrepo.ErrNeedsSignIn) {
			return pushRefuse(id, fetchSignIn(token))
		}
		if err != nil {
			return pushRefuse(id, "Portenv couldn\u2019t reach "+repo.String()+": "+err.Error()+".")
		}
		out := &agentv1.PushListResult{Head: head}
		for _, r := range refs {
			out.Refs = append(out.Refs, &agentv1.RemoteRef{Name: r.Name, Sha: r.SHA})
		}
		return &agentv1.TeamCallsRequest{CallId: id, Ok: true, Result: &agentv1.TeamCallsRequest_PushList{PushList: out}}
	}

	if f := c.GetFetch(); f != nil {
		var wants []githubrepo.Ref
		for _, w := range f.GetWants() {
			if !githubrepo.CheckRefName(w.GetName()) {
				return pushRefuse(id, "Portenv fetches only branches and tags.")
			}
			wants = append(wants, githubrepo.Ref{Name: w.GetName(), SHA: w.GetSha()})
		}
		if len(wants) == 0 {
			return pushRefuse(id, "The fetch names nothing.")
		}
		token, line := fetchAnswer()
		if line != "" {
			return pushRefuse(id, line)
		}
		return s.fetchInto(ctx, name, id, remote, token, wants, f.GetHaves(), fetchSignIn)
	}

	if c.GetPushList() != nil {
		token, line := answer(&daemonv1.PushQuestion{List: true})
		if line != "" {
			return pushRefuse(id, line)
		}
		refs, err := githubrepo.ListRemote(ctx, client, remote, token)
		if errors.Is(err, githubrepo.ErrNeedsSignIn) {
			return pushRefuse(id, signInLine)
		}
		if err != nil {
			return pushRefuse(id, "Portenv couldn’t reach "+repo.String()+": "+err.Error()+".")
		}
		out := &agentv1.PushListResult{}
		for _, r := range refs {
			out.Refs = append(out.Refs, &agentv1.RemoteRef{Name: r.Name, Sha: r.SHA})
		}
		return &agentv1.TeamCallsRequest{CallId: id, Ok: true, Result: &agentv1.TeamCallsRequest_PushList{PushList: out}}
	}

	p := c.GetPush()
	var updates []githubrepo.Update
	for _, u := range p.GetUpdates() {
		updates = append(updates, githubrepo.Update{Dst: u.GetDst(), New: u.GetNewSha(), Old: u.GetOldSha(), Force: u.GetForce()})
	}
	if err := githubrepo.CheckUpdates(updates); err != nil {
		return pushRefuse(id, err.Error()+".")
	}
	// The bundle first: a fast-forward is proven from its pack, never
	// taken from what the workspace says (R-0393). An update that isn't
	// proven is a force: the owner is asked.
	var bundle io.ReadSeeker
	if bid := p.GetBundleId(); bid != "" {
		f, cleanup, err := s.readPushBundle(ctx, name, bid)
		if err != nil {
			return pushRefuse(id, err.Error())
		}
		defer cleanup()
		bundle = f
	}
	ff := githubrepo.FastForwards(bundle, updates)
	q := &daemonv1.PushQuestion{}
	allowedFile := filepath.Join(state, pushesAllowedFile)
	_, aerr := os.Stat(allowedFile)
	q.First = errors.Is(aerr, fs.ErrNotExist)
	q.NeedsYes = q.First
	for i := range updates {
		u := &updates[i]
		del := u.New == ""
		if !u.Force && u.Old != "" && !del && !ff[u.Dst] {
			u.Force = true
		}
		q.Updates = append(q.Updates, &daemonv1.PushUpdateInfo{Dst: u.Dst, NewSha: u.New, Force: u.Force, Delete: del})
		if u.Force || del {
			q.NeedsYes = true
		}
	}
	token, line := answer(q)
	if line != "" {
		return pushRefuse(id, line)
	}
	if q.First {
		_ = writePrivateAtomic(allowedFile, []byte("yes\n"))
	}
	statuses, err := githubrepo.Push(ctx, client, remote, token, updates, bundle)
	token = ""
	_ = token
	switch {
	case errors.Is(err, githubrepo.ErrNeedsSignIn):
		return pushRefuse(id, signInLine)
	case errors.Is(err, githubrepo.ErrTooBig):
		return pushRefuse(id, "This push is too big for GitHub (over 2 GB).")
	case err != nil:
		return pushRefuse(id, "Portenv couldn’t push to "+repo.String()+": "+err.Error())
	}
	s.log.Info("push", "box_id", box.ID, "refs", len(statuses))
	out := &agentv1.PushResult{}
	for _, st := range statuses {
		out.Statuses = append(out.Statuses, &agentv1.PushRefStatus{Dst: st.Dst, Ok: st.OK, Reason: st.Reason})
	}
	return &agentv1.TeamCallsRequest{CallId: id, Ok: true, Result: &agentv1.TeamCallsRequest_Push{Push: out}}
}

// readPushBundle copies the helper's bundle to a 0600 file in portenvd's
// own folder, at most githubrepo.MaxPushBytes, and opens it.
func (s *Server) readPushBundle(ctx context.Context, name, id string) (*os.File, func(), error) {
	h, err := s.agentFor(ctx, name)
	if err != nil {
		return nil, nil, errors.New("Portenv lost the connection to this workspace. Push again.") //nolint:staticcheck // ST1005: a plain line
	}
	st, err := h.ReadPushBundle(ctx, &agentv1.ReadPushBundleRequest{BundleId: id})
	if err != nil {
		return nil, nil, errors.New("Portenv couldn’t read the push. Push again.") //nolint:staticcheck // ST1005: a plain line
	}
	dir := filepath.Join(s.env.Dir, "push")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.CreateTemp(dir, "bundle-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = f.Close(); _ = os.Remove(f.Name()) }
	var n int64
	for {
		r, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			return nil, nil, errors.New("Portenv couldn’t read the push. Push again.") //nolint:staticcheck // ST1005: a plain line
		}
		n += int64(len(r.GetChunk()))
		if n > githubrepo.MaxPushBytes {
			cleanup()
			return nil, nil, fmt.Errorf("This push is too big for GitHub (over 2 GB).") //nolint:staticcheck // ST1005: a plain line
		}
		if _, err := f.Write(r.GetChunk()); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	if _, err := f.Seek(0, 0); err != nil {
		cleanup()
		return nil, nil, err
	}
	return f, cleanup, nil
}

// fetchInto fetches the wanted refs from GitHub on the Mac into a 0600
// bundle in portenvd's own folder, hands it to the helper and removes it.
func (s *Server) fetchInto(ctx context.Context, name, id, remote, token string, wants []githubrepo.Ref, haves []string, signIn func(string) string) *agentv1.TeamCallsRequest {
	dir := filepath.Join(s.env.Dir, "fetch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return pushRefuse(id, "Portenv couldn\u2019t fetch: "+err.Error())
	}
	f, err := os.CreateTemp(dir, "bundle-*")
	if err != nil {
		return pushRefuse(id, "Portenv couldn\u2019t fetch: "+err.Error())
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	client := &http.Client{Timeout: 30 * time.Minute}
	err = githubrepo.FetchRefs(ctx, client, remote, token, wants, haves, f, githubrepo.MaxFetchBytes)
	switch {
	case errors.Is(err, githubrepo.ErrNeedsSignIn):
		return pushRefuse(id, signIn(token))
	case errors.Is(err, githubrepo.ErrTooBig):
		return pushRefuse(id, "This fetch is over 2 GB.")
	case err != nil:
		return pushRefuse(id, err.Error()+".")
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return pushRefuse(id, err.Error())
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return pushRefuse(id, err.Error())
	}
	h, err := s.agentFor(ctx, name)
	if err != nil {
		return pushRefuse(id, "Portenv lost the connection to this workspace. Fetch again.")
	}
	st, err := h.WriteFetchBundle(ctx)
	if err != nil {
		return pushRefuse(id, "Portenv couldn\u2019t hand the fetch in. Fetch again.")
	}
	buf := make([]byte, 1<<20)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if err := st.Send(&agentv1.WriteFetchBundleRequest{Chunk: buf[:n]}); err != nil {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	resp, err := st.CloseAndRecv()
	if err != nil {
		return pushRefuse(id, "Portenv couldn\u2019t hand the fetch in. Fetch again.")
	}
	s.log.Info("fetch", "refs", len(wants), "bytes", size)
	return &agentv1.TeamCallsRequest{CallId: id, Ok: true, Result: &agentv1.TeamCallsRequest_Fetch{Fetch: &agentv1.FetchResult{BundleId: resp.GetBundleId(), BundleBytes: size}}}
}
