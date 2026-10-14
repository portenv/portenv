// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"testing"

	"github.com/moby/moby/api/types/jsonstream"
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
