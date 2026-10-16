// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"bytes"
	"context"
	"io"
)

func runAsMainUser(_ context.Context, _ Config, _ []string, _ io.Reader, local func(io.Writer) error) ([]byte, error) {
	var out bytes.Buffer
	err := local(&out)
	return out.Bytes(), err
}
