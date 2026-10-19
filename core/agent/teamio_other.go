// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import (
	"bytes"
	"context"
	"errors"
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

func runTeamIOAsAgent(context.Context, int, string, []string, io.Reader) ([]byte, error) {
	return nil, errors.New("an agent's files are reached only in the workspace")
}
