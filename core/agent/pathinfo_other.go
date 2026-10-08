// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import "context"

func pathInfoAs(_ context.Context, _ Config, path string) (map[string]bool, error) {
	return PathInfo(path)
}
