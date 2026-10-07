// SPDX-License-Identifier: Apache-2.0

// Command portenv is the Portenv CLI for agents and power users, a thin client
// of the same APIs.
//
// Milestone 0.1 scaffold: it only reports its version.
package main

import (
	"fmt"

	"github.com/portenv/portenv/core/internal/version"
)

func main() {
	fmt.Println("portenv", version.String())
}
