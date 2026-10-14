// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/agent"
	"github.com/portenv/portenv/core/driver"
)

// fakeEngine answers the Docker API calls the open path makes, as each test
// says: an image inspect, a pull's stream, a container inspect.
type fakeEngine struct {
	inspectImage func(w http.ResponseWriter, r *http.Request)
	pull         func(w http.ResponseWriter, r *http.Request)
	inspectBox   func(w http.ResponseWriter, r *http.Request)
}

// hang answers nothing until the caller gives up.
func hang(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

func notFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"No such image"}`))
}

// streamPull writes n progress messages, every, then ends (or hangs).
func streamPull(n int, every time.Duration, thenHang bool) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for i := range n {
			_, _ = fmt.Fprintf(w, `{"status":"Downloading","id":"layer","progressDetail":{"current":%d,"total":%d}}`+"\n", i, n)
			w.(http.Flusher).Flush()
			select {
			case <-time.After(every):
			case <-r.Context().Done():
				return
			}
		}
		if thenHang {
			<-r.Context().Done()
		}
	}
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.Contains(p, "/images/create"):
		f.pull(w, r)
	case strings.Contains(p, "/images/") && strings.HasSuffix(p, "/json"):
		f.inspectImage(w, r)
	case strings.Contains(p, "/containers/") && strings.HasSuffix(p, "/json"):
		f.inspectBox(w, r)
	default:
		http.NotFound(w, r)
	}
}

func fakeDriver(t *testing.T, f *fakeEngine) *Driver {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return driverAt(t, "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
}

func driverAt(t *testing.T, host string) *Driver {
	t.Helper()
	cli, err := client.New(client.WithHost(host), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{StateDir: t.TempDir(), Client: cli})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func shortEngineWait(t *testing.T, d time.Duration) {
	old := EngineWait
	EngineWait = d
	t.Cleanup(func() { EngineWait = old })
}

// An engine call that isn't answered fails within EngineWait as
// ErrEngineUnresponsive, never hanging the open (R-0211).
func TestAnUnansweredEngineCallFailsInEngineWait(t *testing.T) {
	shortEngineWait(t, 300*time.Millisecond)
	d := fakeDriver(t, &fakeEngine{inspectImage: hang, inspectBox: hang})
	start := time.Now()
	err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: "ghcr.io/portenv/toolbox-node:main"})
	if !errors.Is(err, driver.ErrEngineUnresponsive) || time.Since(start) > 2*time.Second {
		t.Fatalf("image inspect: %v after %s", err, time.Since(start))
	}
	if _, err := d.running(context.Background(), "box-1"); !errors.Is(err, driver.ErrEngineUnresponsive) {
		t.Fatalf("container inspect: %v", err)
	}
	sec, err := agent.NewChannelSecrets()
	if err != nil {
		t.Fatal(err)
	}
	d.channels = map[driver.BoxID]agent.ChannelSecrets{"box-1": sec}
	if _, err := d.AgentChannel(context.Background(), "box-1"); !errors.Is(err, driver.ErrEngineUnresponsive) {
		t.Fatalf("the agent channel's inspect: %v", err)
	}
	// The caller's own end isn't the engine's fault.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.running(ctx, "box-1"); errors.Is(err, driver.ErrEngineUnresponsive) {
		t.Errorf("a cancelled call: %v", err)
	}
}

// An engine that can't be reached at all (not running, or starting) is
// ErrEngineUnresponsive too.
func TestAnUnreachableEngineIsUnresponsive(t *testing.T) {
	shortEngineWait(t, 300*time.Millisecond)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	d := driverAt(t, "tcp://"+addr)
	if err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: "img:1"}); !errors.Is(err, driver.ErrEngineUnresponsive) {
		t.Fatalf("got %v", err)
	}
}

// A pull takes as long as it keeps progressing, past EngineWait, and says
// it's starting.
func TestAProgressingPullTakesAsLongAsItNeeds(t *testing.T) {
	shortEngineWait(t, 300*time.Millisecond)
	d := fakeDriver(t, &fakeEngine{inspectImage: notFound, pull: streamPull(12, 100*time.Millisecond, false)})
	var steps []string
	start := time.Now()
	err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: "img:1", Progress: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if time.Since(start) < time.Second {
		t.Errorf("the pull took %s, want its whole stream", time.Since(start))
	}
	if len(steps) != 1 || steps[0] != "pulling the toolbox image" {
		t.Errorf("progress %q", steps)
	}
}

// A pull that stops progressing fails EngineWait after its last message.
func TestAStalledPullFails(t *testing.T) {
	shortEngineWait(t, 300*time.Millisecond)
	d := fakeDriver(t, &fakeEngine{inspectImage: notFound, pull: streamPull(3, 50*time.Millisecond, true)})
	start := time.Now()
	err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: "img:1"})
	if !errors.Is(err, driver.ErrPullStalled) {
		t.Fatalf("got %v", err)
	}
	if took := time.Since(start); took < 400*time.Millisecond || took > 2*time.Second {
		t.Errorf("failed after %s, want about 150 ms of progress and 300 ms of silence", took)
	}
	// One that never starts (no answer to the request) stalls the same way.
	d = fakeDriver(t, &fakeEngine{inspectImage: notFound, pull: hang})
	if err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: "img:1"}); !errors.Is(err, driver.ErrPullStalled) {
		t.Fatalf("a pull with no answer: %v", err)
	}
}

// The engine's own error in a pull's stream is the pull's error.
func TestAPullsErrorIsReturned(t *testing.T) {
	shortEngineWait(t, time.Second)
	d := fakeDriver(t, &fakeEngine{inspectImage: notFound, pull: func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errorDetail":{"message":"no space left on device"},"error":"no space left on device"}` + "\n"))
	}})
	err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: "img:1"})
	if err == nil || !strings.Contains(err.Error(), "no space left on device") || errors.Is(err, driver.ErrPullStalled) {
		t.Fatalf("got %v", err)
	}
}
