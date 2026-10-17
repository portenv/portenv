// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/docker"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// TestAPullReachesTheBoxState (R-0216): an engine (a fake one) streams a
// pull; while it's under way, the box's state carries how far it has got,
// as the window's line reads it ("Getting the workspace image… 312 MB of
// 900 MB"); once the open ends, it's gone.
func TestAPullReachesTheBoxState(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such image"}`)
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			_, _ = io.WriteString(w, `{"status":"Downloading","id":"bbb","progressDetail":{"current":312000000,"total":900000000}}`+"\n")
			w.(http.Flusher).Flush()
			<-release // the pull is under way until the test has looked
			_, _ = io.WriteString(w, `{"status":"Pull complete","id":"bbb"}`+"\n")
		default:
			w.WriteHeader(http.StatusInternalServerError) // nothing past the pull
			_, _ = io.WriteString(w, `{"message":"not in this test"}`)
		}
	}))
	defer srv.Close()
	defer close(release)
	d, err := docker.New(docker.Config{StateDir: t.TempDir(), Host: "tcp://" + strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	s := placementServer(t)
	ctx, done := s.followPull(context.Background(), "acme-api")
	created := make(chan error, 1)
	go func() {
		_, err := d.Create(ctx, driver.Box{ID: "box-0123456789ab", Name: "acme-api", ToolboxImage: "ghcr.io/portenv/toolbox-node:main"})
		created <- err
	}()
	var pull *daemonv1.ImagePull
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		st, err := s.GetBoxState(context.Background(), &daemonv1.GetBoxStateRequest{Name: "acme-api"})
		if err != nil {
			t.Fatal(err)
		}
		if pull = st.GetImagePull(); pull != nil {
			break
		}
	}
	if pull.GetDoneBytes() != 312000000 || pull.GetTotalBytes() != 900000000 {
		t.Fatalf("mid-pull state %v", pull)
	}
	release <- struct{}{}
	<-created // the fake engine refuses what comes after the pull
	done()
	if st, _ := s.GetBoxState(context.Background(), &daemonv1.GetBoxStateRequest{Name: "acme-api"}); st.GetImagePull() != nil {
		t.Fatalf("after the open: %v", st.GetImagePull())
	}
}
