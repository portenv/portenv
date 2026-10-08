// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"context"
	"errors"
)

// ServeChannelProcess runs only inside a box.
func ServeChannelProcess(context.Context, Config) error {
	return errors.New("the agent channel runs only inside a box (Linux)")
}
