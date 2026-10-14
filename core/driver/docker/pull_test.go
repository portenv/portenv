// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/driver"
)

// TestPullTallyCountsEveryLayer (R-0211): the bytes a pull has fetched,
// over every layer it fetches, as Docker reports them layer by layer; a
// layer already here counts for nothing, a finished one counts in full.
func TestPullTallyCountsEveryLayer(t *testing.T) {
	var p pullTally
	steps := []struct {
		msg               jsonstream.Message
		wantDone, wantAll int64
	}{
		{jsonstream.Message{Status: "Pulling from portenv/toolbox-node", ID: "main"}, 0, 0},
		{jsonstream.Message{Status: "Already exists", ID: "aaa"}, 0, 0},
		{jsonstream.Message{Status: "Pulling fs layer", ID: "bbb"}, 0, 0},
		{jsonstream.Message{Status: "Pulling fs layer", ID: "ccc"}, 0, 0},
		{jsonstream.Message{Status: "Downloading", ID: "bbb", Progress: &jsonstream.Progress{Current: 100, Total: 600}}, 100, 600},
		{jsonstream.Message{Status: "Downloading", ID: "ccc", Progress: &jsonstream.Progress{Current: 50, Total: 300}}, 150, 900},
		{jsonstream.Message{Status: "Downloading", ID: "bbb", Progress: &jsonstream.Progress{Current: 262, Total: 600}}, 312, 900},
		{jsonstream.Message{Status: "Download complete", ID: "ccc"}, 562, 900},
		// Extracting reports its own progress: never counted twice.
		{jsonstream.Message{Status: "Extracting", ID: "ccc", Progress: &jsonstream.Progress{Current: 10, Total: 300}}, 562, 900},
		{jsonstream.Message{Status: "Pull complete", ID: "bbb"}, 900, 900},
		{jsonstream.Message{Status: "Digest: sha256:abc"}, 900, 900},
	}
	for i, s := range steps {
		done, all := p.update(s.msg)
		if done != s.wantDone || all != s.wantAll {
			t.Fatalf("step %d (%s %s): %d of %d, want %d of %d", i, s.msg.Status, s.msg.ID, done, all, s.wantDone, s.wantAll)
		}
	}
}

// TestAPullReportsItsProgress: a pull against an engine (a fake one here)
// reports its bytes to whoever opens the box, layer by layer, and an
// engine's error during the pull ends it.
func TestAPullReportsItsProgress(t *testing.T) {
	stream := strings.Join([]string{
		`{"status":"Pulling fs layer","id":"bbb"}`,
		`{"status":"Downloading","id":"bbb","progressDetail":{"current":312,"total":900}}`,
		`{"status":"Download complete","id":"bbb"}`,
		`{"status":"Pull complete","id":"bbb"}`,
	}, "\n") + "\n"
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		switch {
		case strings.HasSuffix(r.URL.Path, "/json") && strings.Contains(r.URL.Path, "/images/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such image"}`)
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			w.Header().Set("Content-Type", "application/json")
			if fail {
				_, _ = io.WriteString(w, `{"errorDetail":{"message":"no space left on device"},"error":"no space left on device"}`+"\n")
				return
			}
			_, _ = io.WriteString(w, stream)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{StateDir: t.TempDir(), Client: cli})
	if err != nil {
		t.Fatal(err)
	}
	var seen [][2]int64
	ctx := driver.WithPullProgress(context.Background(), func(done, total int64) { seen = append(seen, [2]int64{done, total}) })
	if err := d.ensureImage(ctx, "ghcr.io/portenv/toolbox-node:main"); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 || seen[len(seen)-1] != [2]int64{900, 900} || !slices.Contains(seen, [2]int64{312, 900}) {
		t.Fatalf("progress %v", seen)
	}
	fail = true
	if err := d.ensureImage(context.Background(), "ghcr.io/portenv/toolbox-node:main"); err == nil || !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("an engine's error during the pull: %v", err)
	}
}
