// SPDX-License-Identifier: Apache-2.0

package version

import "testing"

func TestString(t *testing.T) {
	if got, want := String(), Version+" ("+Commit+")"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
