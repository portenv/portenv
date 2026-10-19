// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portenv/portenv/core/driver"
)

const (
	toolboxMain = "ghcr.io/portenv/toolbox-node:main"
	oldDigest   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newDigest   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// localImage answers an image inspect with these RepoDigests.
func localImage(repoDigests ...string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		list := "[]"
		if len(repoDigests) > 0 {
			list = `["` + repoDigests[0] + `"`
			for _, d := range repoDigests[1:] {
				list += `,"` + d + `"`
			}
			list += "]"
		}
		_, _ = fmt.Fprintf(w, `{"Id":"sha256:abc","RepoTags":[%q],"RepoDigests":%s}`, toolboxMain, list)
	}
}

// registry answers the distribution check with digest, counting calls.
func registry(digest string, calls *atomic.Int32) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"Descriptor":{"mediaType":"application/vnd.oci.image.index.v1+json","digest":%q,"size":1234},"Platforms":[]}`, digest)
	}
}

// countPull records that a pull happened, then ends it.
func countPull(n *atomic.Int32) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"Pull complete","id":"layer"}` + "\n"))
	}
}

// A new workspace never starts from an old toolbox this Mac pulled before:
// a tag whose registry image changed is pulled again first (R-0302).
func TestAStaleToolboxIsPulledBeforeACreate(t *testing.T) {
	shortEngineWait(t, 2*time.Second)
	var asked, pulled atomic.Int32
	d := fakeDriver(t, &fakeEngine{
		inspectImage: localImage("ghcr.io/portenv/toolbox-node@" + oldDigest),
		distribution: registry(newDigest, &asked),
		pull:         countPull(&pulled),
	})
	var steps []string
	if err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: toolboxMain, Progress: func(s string) { steps = append(steps, s) }}); err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 1 || pulled.Load() != 1 {
		t.Fatalf("asked the registry %d times, pulled %d; want 1 and 1", asked.Load(), pulled.Load())
	}
	if len(steps) != 1 || steps[0] != "updating the toolbox image" {
		t.Errorf("progress %q", steps)
	}
}

func TestACurrentOrUncheckableToolboxIsUsedAsItIs(t *testing.T) {
	shortEngineWait(t, 2*time.Second)
	for _, c := range []struct {
		name      string
		ref       string
		local     []string
		registry  func(*atomic.Int32) func(http.ResponseWriter, *http.Request)
		wantAsked int32
	}{
		{"current", toolboxMain, []string{"ghcr.io/portenv/toolbox-node@" + newDigest},
			func(n *atomic.Int32) func(http.ResponseWriter, *http.Request) { return registry(newDigest, n) }, 1},
		{"current among several", toolboxMain, []string{"other.io/x@" + oldDigest, "ghcr.io/portenv/toolbox-node@" + newDigest},
			func(n *atomic.Int32) func(http.ResponseWriter, *http.Request) { return registry(newDigest, n) }, 1},
		{"registry error (offline)", toolboxMain, []string{"ghcr.io/portenv/toolbox-node@" + oldDigest},
			func(n *atomic.Int32) func(http.ResponseWriter, *http.Request) {
				return func(w http.ResponseWriter, _ *http.Request) {
					n.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"dial tcp: lookup ghcr.io: no such host"}`))
				}
			}, 1},
		{"registry doesn't answer", toolboxMain, []string{"ghcr.io/portenv/toolbox-node@" + oldDigest},
			func(n *atomic.Int32) func(http.ResponseWriter, *http.Request) {
				return func(w http.ResponseWriter, r *http.Request) { n.Add(1); hang(w, r) }
			}, 1},
		{"pinned", toolboxMain + "@" + oldDigest, []string{"ghcr.io/portenv/toolbox-node@" + oldDigest},
			func(n *atomic.Int32) func(http.ResponseWriter, *http.Request) { return registry(newDigest, n) }, 0},
		{"built here, no registry digest", toolboxMain, nil,
			func(n *atomic.Int32) func(http.ResponseWriter, *http.Request) { return registry(newDigest, n) }, 0},
		{"not a registry image", "toolbox-node:dev", []string{"toolbox-node@" + oldDigest},
			func(n *atomic.Int32) func(http.ResponseWriter, *http.Request) { return registry(newDigest, n) }, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			old := RegistryWait
			RegistryWait = 300 * time.Millisecond
			t.Cleanup(func() { RegistryWait = old })
			var asked, pulled atomic.Int32
			d := fakeDriver(t, &fakeEngine{inspectImage: localImage(c.local...), distribution: c.registry(&asked), pull: countPull(&pulled)})
			start := time.Now()
			if err := d.ensureImage(context.Background(), driver.Box{ToolboxImage: c.ref}); err != nil {
				t.Fatal(err)
			}
			if asked.Load() != c.wantAsked || pulled.Load() != 0 {
				t.Errorf("asked the registry %d times (want %d), pulled %d (want 0)", asked.Load(), c.wantAsked, pulled.Load())
			}
			if took := time.Since(start); took > time.Second {
				t.Errorf("took %s; the registry check is bounded by RegistryWait", took)
			}
		})
	}
}
