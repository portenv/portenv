// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"context"
	"errors"
	"io"
)

// RunRestic is only supported inside a Linux box.
func RunRestic(context.Context, []string, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("portenv-agent restic runs only inside a Linux box")
}
