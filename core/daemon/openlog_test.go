// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/driver/docker"
	"github.com/portenv/portenv/core/local"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// A path in an error never reaches the log: a home folder names the person.
func TestNoPathsInLogLines(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"open /Users/retnan/Library/Application Support/Portenv/boxes/a.json: no such file", "open …: no such file"},
		{`stat "/private/var/folders/x/y": denied`, `stat "…": denied`},
		{"read ~/Library/Portenv/servers.json failed", "read … failed"},
		{"dialing 192.168.65.7:2376: context deadline exceeded", "dialing 192.168.65.7:2376: context deadline exceeded"},
		{"pull ghcr.io/portenv/toolbox-node:main: stalled", "pull ghcr.io/portenv/toolbox-node:main: stalled"},
		{"sftp:portenv-storage@host:/storage unreachable", "sftp:portenv-storage@host:/storage unreachable"},
		{"Get https://ghcr.io/v2/portenv/token: timeout", "Get https://ghcr.io/v2/portenv/token: timeout"},
		{"/single is kept", "/single is kept"},
	} {
		if got := noPaths(c.in); got != c.want {
			t.Errorf("noPaths(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// openWithEngine opens a workspace against a fake container engine whose
// image inspect and pull behave as given, and returns the error and the log.
func openWithEngine(t *testing.T, inspect, pull http.HandlerFunc) (string, error) {
	t.Helper()
	old := docker.EngineWait
	docker.EngineWait = 300 * time.Millisecond
	t.Cleanup(func() { docker.EngineWait = old })
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/_ping"):
			w.Header().Set("API-Version", "1.47")
			_, _ = w.Write([]byte("OK"))
		case strings.Contains(p, "/images/create"):
			pull(w, r)
		case strings.Contains(p, "/images/"):
			inspect(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(engine.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(engine.URL, "http://"))
	t.Setenv("DOCKER_API_VERSION", "1.47")
	s, logs := loggingServer(t)
	c := local.BoxConfig{ID: "box-0123abcd", Name: "acme-api", Image: "ghcr.io/portenv/toolbox-node:main"}
	if err := s.env.SaveBox(c); err != nil {
		t.Fatal(err)
	}
	_ = s.env.Provided.Put(c.ID, testRepoKey(c.ID))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.OpenBox(ctx, &daemonv1.OpenBoxRequest{Name: c.Name})
	out := logs.String()
	if strings.Contains(out, s.env.Dir) || strings.Contains(out, "/Users/") {
		t.Errorf("the log has a path: %s", out)
	}
	return out, err
}

func hangHandler(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// An open the engine never answers fails within its deadline with a plain
// line, and the log says so by workspace ID: on entry, and the failure.
func TestAnOpenTheEngineDoesntAnswerFailsPlainlyAndIsLogged(t *testing.T) {
	start := time.Now()
	out, err := openWithEngine(t, hangHandler, hangHandler)
	if st, _ := status.FromError(err); st.Code() != codes.Unavailable || st.Message() != EngineUnresponsive {
		t.Fatalf("got %v, want %q", err, EngineUnresponsive)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("it took %s", time.Since(start))
	}
	for _, want := range []string{`msg=open box_id=box-0123abcd`, `msg="open failed" box_id=box-0123abcd`, "didn't answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
}

// A download that stops is its own plain line; the log says the pull began.
func TestAStalledDownloadFailsPlainlyAndIsLogged(t *testing.T) {
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such image"}`))
	}
	stall := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"status":"Downloading","id":"layer"}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
	out, err := openWithEngine(t, notFound, stall)
	if st, _ := status.FromError(err); st.Code() != codes.Unavailable || st.Message() != PullStalled {
		t.Fatalf("got %v, want %q", err, PullStalled)
	}
	for _, want := range []string{`msg="pulling the toolbox image" box_id=box-0123abcd`, `msg="open failed" box_id=box-0123abcd`} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
}

// An engine nothing answers for (Docker Desktop not running) is its own
// plain line.
func TestAnOpenWithNoEngineSaysStartIt(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	t.Setenv("DOCKER_HOST", "tcp://"+addr)
	t.Setenv("DOCKER_API_VERSION", "1.47")
	s, logs := loggingServer(t)
	c := local.BoxConfig{ID: "box-0123abcd", Name: "acme-api", Image: "ghcr.io/portenv/toolbox-node:main"}
	if err := s.env.SaveBox(c); err != nil {
		t.Fatal(err)
	}
	_ = s.env.Provided.Put(c.ID, testRepoKey(c.ID))
	_, err = s.OpenBox(context.Background(), &daemonv1.OpenBoxRequest{Name: c.Name})
	if st, _ := status.FromError(err); st.Code() != codes.Unavailable || st.Message() != EngineNotRunning {
		t.Fatalf("got %v, want %q", err, EngineNotRunning)
	}
	if !strings.Contains(logs.String(), `msg="open failed" box_id=box-0123abcd`) {
		t.Errorf("no failure in the log:\n%s", logs.String())
	}
}

// CloseBox says what it did by workspace ID (ADR 0023): on entry, and a
// failure with its reason, never a path.
func TestCloseBoxIsLogged(t *testing.T) {
	s, logs := loggingServer(t)
	c := local.BoxConfig{ID: "box-0123abcd", Name: "acme-api", Image: "img"}
	if err := s.env.SaveBox(c); err != nil {
		t.Fatal(err)
	}
	// Not open here: the close fails, and says so.
	if _, err := s.CloseBox(context.Background(), &daemonv1.CloseBoxRequest{Name: c.Name}); err == nil {
		t.Fatal("closing a workspace that isn't open succeeded")
	}
	out := logs.String()
	for _, want := range []string{`msg=close box_id=box-0123abcd`, `msg="close failed" box_id=box-0123abcd`} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, s.env.Dir) {
		t.Errorf("the log has a path:\n%s", out)
	}
	// Already closed while this close waited: entry only, no failure.
	before := strings.Count(logs.String(), "close failed")
	s.open[c.Name] = &openBox{closed: true}
	if _, err := s.CloseBox(context.Background(), &daemonv1.CloseBoxRequest{Name: c.Name}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "close failed") != before {
		t.Errorf("a close of an already-closed workspace logged a failure:\n%s", logs.String())
	}
}

// While a Move To runs, CloseBox refuses plainly, with the refusal's
// trailer, and closes nothing (R-0249): a close or a quit in the middle of
// a move would race the move's own close and open.
func TestCloseBoxRefusesDuringAMove(t *testing.T) {
	s, logs := loggingServer(t)
	c := local.BoxConfig{ID: "box-0123abcd", Name: "acme-api", Image: "img"}
	if err := s.env.SaveBox(c); err != nil {
		t.Fatal(err)
	}
	ob := &openBox{}
	s.open[c.Name] = ob
	done := s.moving(c.Name, "dev@test-server")
	_, err := s.CloseBox(context.Background(), &daemonv1.CloseBoxRequest{Name: c.Name})
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition || st.Message() != "acme-api is moving to test-server. Close it once the move has finished." {
		t.Fatalf("got %v", err)
	}
	if ob.closed || s.open[c.Name] != ob {
		t.Error("the refused close closed the workspace")
	}
	if !strings.Contains(logs.String(), `msg="close refused" box_id=box-0123abcd`) {
		t.Errorf("the refusal isn't in the log:\n%s", logs.String())
	}
	// The move over, a close goes ahead (here: already closed, so nothing to do).
	done()
	ob.closed = true
	if _, err := s.CloseBox(context.Background(), &daemonv1.CloseBoxRequest{Name: c.Name}); err != nil {
		t.Fatalf("after the move: %v", err)
	}
}
