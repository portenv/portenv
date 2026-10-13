// SPDX-License-Identifier: Apache-2.0

package daemon

import "testing"

// TestHelperUnavailableEveryServerWording: a server's portenv is installed
// separately and may be older, so each wording it has used for an
// unavailable helper is recognised (R-0132): with only the newest, an older
// server's message would miss dropRemoteAgent.
func TestHelperUnavailableEveryServerWording(t *testing.T) {
	for _, tc := range []struct {
		name, msg string
		want      bool
	}{
		{"before the rename", "acme-api: the box agent is unavailable (no channel); restart the box", true},
		{"the first rename", "the workspace agent is unavailable (its channel is down or failed verification); restart the workspace", true},
		{"now", "portenv app point: the workspace helper is unavailable (no channel)", true},
		{"another failure", "the lease is held by studio", false},
		{"an AI agent", "Grok Bot is unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := helperUnavailable(tc.msg); got != tc.want {
				t.Fatalf("helperUnavailable(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}
