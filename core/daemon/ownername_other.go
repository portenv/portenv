// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package daemon

import (
	"os/user"
	"strings"
)

// accountFullName is the account's full name (GECOS, up to its first
// comma), or "".
func accountFullName() string {
	if u, err := user.Current(); err == nil {
		name, _, _ := strings.Cut(u.Name, ",")
		return name
	}
	return ""
}
