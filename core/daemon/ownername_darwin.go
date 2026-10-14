// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/portenv/portenv/core/internal/bounded"
)

// accountFullName is the Mac account's full name (`id -F`), or "".
func accountFullName() string {
	cmd, err := bounded.Command(context.Background(), 2*time.Second, "/usr/bin/id", "-F")
	if err != nil {
		return ""
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
