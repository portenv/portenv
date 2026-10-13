// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The %output decoder and the line reader read bytes a workspace program
// wrote, so they're fuzzed (R-0162). The seeds are the spike's real tmux 3.4
// streams (tests/spike/q3/fixtures), re-encoded as control-mode lines.

// spikeOutputs returns each recorded pane-output chunk from the fixtures.
func spikeOutputs(f *testing.F) [][]byte {
	f.Helper()
	paths, err := filepath.Glob("../../tests/spike/q3/fixtures/*.jsonl")
	if err != nil || len(paths) == 0 {
		f.Fatalf("no spike fixtures: %v", err)
	}
	var out [][]byte
	for _, p := range paths {
		file, err := os.Open(p) // #nosec G304 -- test fixtures
		if err != nil {
			f.Fatal(err)
		}
		sc := bufio.NewScanner(file)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var r struct{ T, Path, B64 string }
			if json.Unmarshal(sc.Bytes(), &r) != nil || r.Path != "C" {
				continue
			}
			if b, err := base64.StdEncoding.DecodeString(r.B64); err == nil {
				out = append(out, b)
			}
		}
		_ = file.Close()
	}
	return out
}

// FuzzDecodeControlOutput: decoding never panics, never grows the data,
// and undoes tmux's escaping exactly.
func FuzzDecodeControlOutput(f *testing.F) {
	for _, b := range spikeOutputs(f) {
		f.Add(b)
	}
	f.Add([]byte("\\\\\\0\\00\\777\\0123"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if got := decodeControlOutput(string(b)); len(got) > len(b) {
			t.Fatalf("decoded %d bytes from %d", len(got), len(b))
		}
		if got := decodeControlOutput(encodeControlOutput(b)); !bytes.Equal(got, b) {
			t.Fatalf("round trip: %q -> %q", b, got)
		}
		e := parseControlLine("%output %1 " + encodeControlOutput(b))
		if e.kind != ctlOutput || e.pane != "%1" || !bytes.Equal(e.data, b) {
			t.Fatalf("parse: %+v", e)
		}
	})
}

// FuzzReadControlLines: any stream is read without panicking; every line
// handed on is under the cap and holds no newline; the lines handed on are
// the input's own lines, in order.
func FuzzReadControlLines(f *testing.F) {
	for i, b := range spikeOutputs(f) {
		if i%5 == 0 {
			f.Add([]byte("%begin 1 2 0\n%end 1 2 0\n%output %1 "+encodeControlOutput(b)+"\n%window-add @1\n"), 64)
		}
	}
	f.Add([]byte(strings.Repeat("x", 300)+"\n\r\n\n%exit"), 16)
	f.Fuzz(func(t *testing.T, in []byte, max int) {
		if max < 1 || max > 1<<16 {
			return
		}
		var lines [][]byte
		err := readControlLines(bytes.NewReader(in), max, func(l []byte, err error) {
			if err != nil {
				return
			}
			if len(l) > max || bytes.IndexByte(l, '\n') >= 0 {
				t.Fatalf("line of %d bytes (cap %d): %q", len(l), max, l)
			}
			_ = parseControlLine(string(l))
			lines = append(lines, append([]byte(nil), l...))
		})
		if err != nil {
			t.Fatal(err)
		}
		// Each delivered line is one of the input's lines.
		var want [][]byte
		for _, l := range bytes.Split(in, []byte("\n")) {
			want = append(want, bytes.TrimSuffix(l, []byte("\r")))
		}
		j := 0
		for _, l := range lines {
			for j < len(want) && !bytes.Equal(want[j], l) {
				j++
			}
			if j == len(want) {
				t.Fatalf("line %q isn't in the input, in order", l)
			}
			j++
		}
	})
}
