// SPDX-License-Identifier: Apache-2.0

// Command portenv-runner is the Portenv server daemon, the server build of the core
// that runs boxes on developer servers and cloud hosts.
//
// Milestone 0.1 scaffold: it only reports its version.
package main

import (
	"fmt"

	"github.com/portenv/portenv/core/internal/version"
)

func main() {
	fmt.Println("portenv-runner", version.String())
}
