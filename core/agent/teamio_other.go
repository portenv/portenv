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

func runAsAgent(_ context.Context, _ int, _ []string, _ io.Reader, local func(io.Writer) error) ([]byte, error) {
	var out bytes.Buffer
	err := local(&out)
	return out.Bytes(), err
}

func runAsAgentInTeam(ctx context.Context, uid int, args []string, stdin io.Reader, local func(io.Writer) error) ([]byte, error) {
	return runAsAgent(ctx, uid, args, stdin, local)
}
