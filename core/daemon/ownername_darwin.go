// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// accountFullName is the Mac account's full name (`id -F`), or "".
func accountFullName() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/id", "-F").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
