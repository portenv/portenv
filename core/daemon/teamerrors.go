// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/agent"
)

// A team start's failures reach the owner as one plain line: what
// happened, then the next step (GUIDELINES §10). The raw detail (the
// command, its exit status, setfacl's or chown's words) goes to portenvd's
// log only (R-0323).

// teamStep is the part of a team start that failed.
type teamStep string

const (
	stepLoadTeam teamStep = "read the agent files"
	stepHelper   teamStep = "reach the workspace helper"
	stepProject  teamStep = "open the project to the team"
	stepStart    teamStep = "start the agent"
)

// plainError is a plain line for the owner, with the gRPC code it travels
// with. It doesn't unwrap: the raw error is logged, never shown.
type plainError struct {
	code codes.Code
	line string
}

func (e *plainError) Error() string { return e.line }

// GRPCStatus keeps the line as the status message.
func (e *plainError) GRPCStatus() *status.Status { return status.New(e.code, e.line) }

const startAgain = " Click Start again; if it happens again, restart the workspace."

// teamStartLine is the owner's line for err at step, and its code.
func teamStartLine(box string, step teamStep, err error) (codes.Code, string) {
	var pe *plainError
	if errors.As(err, &pe) {
		return pe.code, pe.line
	}
	raw := err.Error()
	if st, ok := status.FromError(err); ok {
		raw = st.Message()
	}
	low := strings.ToLower(raw)
	code := status.Code(err)
	if line, ok := agent.TeamStartLine(raw); ok {
		return codes.FailedPrecondition, line
	}
	switch {
	case code == codes.Unavailable || code == codes.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) ||
		raw == AgentUnavailable || strings.Contains(low, "connection refused") || strings.Contains(low, "connect to init"):
		return codes.Unavailable, "The workspace helper isn't answering. Restart the workspace, then click Start again."
	case strings.Contains(low, "no space left on device") || strings.Contains(low, "disk quota exceeded"):
		return codes.ResourceExhausted, "This workspace is out of disk space. Free some space in it, then click Start again."
	case strings.Contains(low, "read-only file system"):
		return codes.FailedPrecondition, "This workspace's files can't be changed right now. Restart the workspace, then click Start again."
	case strings.Contains(raw, "check "+projectDir(box)+": ") && strings.Contains(low, "no such file"):
		return codes.FailedPrecondition, "This workspace has no project folder (~/" + box + ") to share with the team. Make it, then click Start again."
	}
	notAllowed := strings.Contains(low, "operation not permitted") || strings.Contains(low, "permission denied")
	switch step {
	case stepLoadTeam:
		return codes.FailedPrecondition, "Portenv couldn't read this workspace's agent files." + startAgain
	case stepHelper:
		return codes.Unavailable, "The workspace helper isn't answering. Restart the workspace, then click Start again."
	case stepProject:
		if notAllowed {
			return codes.FailedPrecondition, "Portenv wasn't allowed to share some of the project's files with the team." + startAgain
		}
		return codes.FailedPrecondition, "Portenv couldn't share the project with the team." + startAgain
	default:
		if notAllowed {
			return codes.FailedPrecondition, "Portenv wasn't allowed to set up this agent in the workspace." + startAgain
		}
		return codes.FailedPrecondition, "Portenv couldn't start this agent's terminal." + startAgain
	}
}

// startError logs err's raw detail and returns the owner's plain line.
func (e *teamEngine) startError(box, agentID string, step teamStep, err error) error {
	var pe *plainError
	if errors.As(err, &pe) {
		return err
	}
	code, line := teamStartLine(box, step, err)
	e.logger().Warn("team start failed", "box", box, "agent", agentID, "step", string(step), "detail", err.Error(), "shown", line) // workspace-word: ok (a log key)
	return &plainError{code: code, line: line}
}

func (e *teamEngine) logger() *slog.Logger {
	if e.log != nil {
		return e.log
	}
	return slog.New(slog.DiscardHandler)
}
