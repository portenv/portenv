// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"testing"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The helper reports the box the driver said it runs in (R-0172, R-0173),
// and nothing when it wasn't told or the value isn't a box ID.
func TestGetVersionReportsTheBoxID(t *testing.T) {
	for env, want := range map[string]string{
		"box-0123456789ab": "box-0123456789ab", "tabs-1a2b3c4d": "tabs-1a2b3c4d",
		"": "", "Box-X": "", "box id": "", "box-\n": "", "../etc": "",
	} {
		t.Setenv(BoxIDEnv, env)
		r, err := (&server{}).GetVersion(context.Background(), &agentv1.GetVersionRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if r.GetBoxId() != want {
			t.Errorf("%q: box_id %q, want %q", env, r.GetBoxId(), want)
		}
	}
}

// Why the box ID is an environment variable and not part of the secrets
// line: a helper refuses a secrets line with a field it doesn't know, so a
// new field there would stop boxes running an older image from starting.
func TestASecretsLineWithAnUnknownFieldIsRefused(t *testing.T) {
	sec, err := NewChannelSecrets()
	if err != nil {
		t.Fatal(err)
	}
	line, err := sec.Line()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := parseSecretsLine(line); !ok {
		t.Fatal("a good line was refused")
	}
	extra := append([]byte(`{"BoxID":"box-0123456789ab",`), line[1:]...)
	if _, ok, _ := parseSecretsLine(extra); ok {
		t.Fatal("a line with an unknown field was accepted; the box ID could go in the secrets line after all")
	}
}
