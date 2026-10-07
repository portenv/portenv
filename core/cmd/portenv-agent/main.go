// SPDX-License-Identifier: Apache-2.0

// Command portenv-agent is the in-box agent and the box's init process: terminals,
// lanes, port discovery, command audit and the outbound tunnel.
//
// Milestone 0.1 scaffold: it only reports its version.
package main

import (
	"fmt"

	"github.com/portenv/portenv/core/internal/version"
)

func main() {
	fmt.Println("portenv-agent", version.String())
}
