// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/portenv/portenv/core/internal/teamkit"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// maxHookInput caps a hook's JSON: Write's tool_input carries the whole
// file. Anything longer isn't read as a hook.
const maxHookInput = 16 << 20

// notifyPermissionWait is how long the call may take: a hook must never
// hold the runner up.
const notifyPermissionWait = 5 * time.Second

// notifyPermission is `portenv notify-permission`, run by Claude Code's
// hooks (R-0412 item 18, teamkit/claudehooks.go): PermissionRequest tells
// portenvd the prompt shows, with a one-line summary; the other hooks tell
// it the prompt is over. It prints nothing and never fails, so Claude
// Code shows its prompt as it would without it. A marker in the agent's
// own cache folder says a wait was sent, so the hooks that run on every
// tool call send nothing when there's nothing to end.
func notifyPermission(ctx context.Context, getenv func(string) string, stdin io.Reader) {
	b, err := io.ReadAll(io.LimitReader(stdin, maxHookInput+1))
	if err != nil || len(b) > maxHookInput {
		return
	}
	h, err := teamkit.ParsePermissionHook(b)
	if err != nil {
		return
	}
	sock := getenv("PORTENV_SOCKET")
	if sock == "" {
		return
	}
	marker := ""
	if home := getenv("HOME"); home != "" {
		marker = filepath.Join(home, ".cache", "portenv", "permission-waiting")
	}
	if h.Waiting {
		if marker != "" && os.MkdirAll(filepath.Dir(marker), 0o700) == nil {
			_ = os.WriteFile(marker, nil, 0o600)
		}
	} else {
		if marker == "" {
			return
		}
		if err := os.Remove(marker); err != nil {
			return // no wait was sent (or none this folder knows of)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, notifyPermissionWait)
	defer cancel()
	_, _ = sendTeamCall(ctx, sock, &agentv1.TeamCallsResponse{Op: &agentv1.TeamCallsResponse_Permission{
		Permission: &agentv1.PermissionWait{Waiting: h.Waiting, Summary: h.Summary},
	}})
}
