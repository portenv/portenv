// SPDX-License-Identifier: Apache-2.0

// Command portenvd is the Portenv Mac daemon. It owns the boxes on this Mac:
// driver calls, autosave, restic, leases and port relays.
//
// Milestone 0.1 scaffold: it only reports its version.
package main

import (
	"fmt"

	"github.com/portenv/portenv/core/internal/version"
)

func main() {
	fmt.Println("portenvd", version.String())
}
