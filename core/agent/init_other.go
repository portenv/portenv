// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"context"
	"errors"
	"log/slog"
)

// Init is only supported inside a Linux box.
func Init(context.Context, Config, *slog.Logger) error {
	return errors.New("portenv-agent init runs only inside a Linux box")
}
