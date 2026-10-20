// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The owner's portenv commands (ADR 0027 §5, S3). Each is one portenvd
// call, the same one the app makes, with the same checks: the CLI gets no
// power the app lacks, and no agent reaches any of them.

// agentCallable is the DaemonService calls an agent's connection may make.
// Every other call is the owner's. TabTerminal checks its caller itself
// (ADR 0020 decision 4) and attributes what an agent types to it.
var agentCallable = map[string]bool{
	daemonv1.DaemonService_TabTerminal_FullMethodName: true,
}

// ownerOnlyLine is the refusal an agent's connection gets.
const ownerOnlyLine = "Only this Mac's owner can do that; an agent can't."

// ownerOnlyCall refuses an agent's connection on every DaemonService call
// it may not make, before the call runs: nothing in the request matters,
// and no workspace helper is contacted. Calls outside DaemonService (a
// test's own) are left alone.
func ownerOnlyCall(ctx context.Context, method string) error {
	if !strings.HasPrefix(method, "/"+daemonv1.DaemonService_ServiceDesc.ServiceName+"/") || agentCallable[method] {
		return nil
	}
	who, err := callerFrom(ctx)
	if err != nil {
		return err
	}
	if who.kind != callerApp {
		return status.Error(codes.PermissionDenied, ownerOnlyLine)
	}
	return nil
}

// ownerOnlyInterceptors are ownerOnlyCall, for unary and stream calls.
func ownerOnlyInterceptors() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := ownerOnlyCall(ctx, info.FullMethod); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := ownerOnlyCall(ss.Context(), info.FullMethod); err != nil {
				return err
			}
			return handler(srv, ss)
		}),
	}
}

// WakeAgent is the owner's portenv wake.
func (s *Server) WakeAgent(ctx context.Context, req *daemonv1.WakeAgentRequest) (*daemonv1.WakeAgentResponse, error) {
	if err := s.teamEng().ownerWake(ctx, req.GetName(), req.GetAgentId()); err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.WakeAgentResponse{}, nil
}

// ListDecisions is portenv decision list.
func (s *Server) ListDecisions(ctx context.Context, req *daemonv1.ListDecisionsRequest) (*daemonv1.ListDecisionsResponse, error) {
	ds, err := s.teamEng().listDecisions(ctx, req.GetName(), req.GetAll())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.ListDecisionsResponse{Decisions: ds}, nil
}

// Deploy is the owner's portenv deploy.
func (s *Server) Deploy(ctx context.Context, req *daemonv1.DeployRequest) (*daemonv1.DeployResponse, error) {
	d, err := s.teamEng().deploy(ctx, req.GetName(), req.GetTarget())
	if err != nil {
		return nil, teamStatus(err)
	}
	return &daemonv1.DeployResponse{Decision: d}, nil
}

// agentDisplayName is the agent's name from its file, else its id.
func agentDisplayName(a *TeamAgent) string {
	if a.Spec != nil && a.Spec.Name != "" {
		return a.Spec.Name
	}
	return a.ID
}

// ownerWake types the agent's wake-up line for its unread batch now,
// through the helper's WakeAgent, so every check a routine gets applies:
// a busy agent is refused and never typed into (R-0359). It never types a
// line of its own: with nothing unread there is nothing to read, and the
// owner is told to send a message. The rate limit between the tick's own
// wake-ups doesn't hold the owner's back, but the owner's counts as the
// batch's line, so the tick doesn't type it again.
func (e *teamEngine) ownerWake(ctx context.Context, name, agentID string) error {
	team, err := e.loadTeam(ctx, name)
	if err != nil {
		return err
	}
	a := team.agent(agentID)
	if a == nil {
		return fmt.Errorf("this workspace has no agent %q", agentID)
	}
	who := agentDisplayName(a)
	b, err := e.lockBox(ctx, name)
	if err != nil {
		return err
	}
	defer b.mu.Unlock()
	w := b.wakes[agentID]
	switch {
	case b.states[agentID] != typesv1.AgentState_AGENT_STATE_RUNNING:
		return fmt.Errorf("%s isn't running. Start it with portenv team start %s.", who, agentID) //nolint:staticcheck // ST1005: a plain line, shown as it is
	case !b.wakeable[agentID]:
		return fmt.Errorf("%s's runner can't be woken: Portenv types only into a runner it can tell is idle.", who) //nolint:staticcheck // ST1005: a plain line, shown as it is
	case w == nil || w.count == 0:
		return fmt.Errorf("%s has nothing new in its inbox. Send it a message with portenv msg %s TEXT.", who, agentID) //nolint:staticcheck // ST1005: a plain line, shown as it is
	}
	h, err := e.helper(ctx, name)
	if err != nil {
		return err
	}
	if _, err := h.WakeAgent(ctx, &agentv1.WakeAgentRequest{AgentId: agentID, From: w.from, Count: int32(min(w.count, 1<<20))}); err != nil { // #nosec G115 -- bounded
		if status.Code(err) == codes.FailedPrecondition {
			return fmt.Errorf("%s is busy, so Portenv won't type into it. It'll see your message when it's next idle.", who) //nolint:staticcheck // ST1005: a plain line, shown as it is
		}
		return errors.New(status.Convert(err).Message())
	}
	w.typed, w.last = true, e.now()
	return nil
}

// listDecisions is the workspace's decisions, oldest first (their ids are
// ULIDs): pending ones only, unless all. Overdue ones are expired first,
// as for the app.
func (e *teamEngine) listDecisions(ctx context.Context, name string, all bool) ([]*typesv1.Decision, error) {
	b, err := e.lockBox(ctx, name)
	if err != nil {
		return nil, err
	}
	defer b.mu.Unlock()
	var out []*typesv1.Decision
	for _, d := range b.decisions {
		if all || d.GetState() == typesv1.DecisionState_DECISION_STATE_PENDING {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, c *typesv1.Decision) int { return strings.Compare(a.GetId(), c.GetId()) })
	return out, nil
}

// deployTargetRE is a runbook target's name, as ADR 0026 names secrets:
// never a path, a flag or a space.
var deployTargetRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// noRunbookLine is ADR 0027 §5's refusal (R-0359).
const noRunbookLine = "This project has no deploy runbook, so Portenv won't guess how to deploy. Add DEPLOY.md (see portenv guide), then try again."

// runbookPaths are where a project's runbook may be (ADR 0026 §2); the
// first wins.
func runbookPaths(name string) []string {
	return []string{projectDir(name) + "/.portenv/runbook.md", projectDir(name) + "/DEPLOY.md"}
}

// deploy is the owner's portenv deploy. With no runbook it refuses (ADR
// 0027 §5). With one it refuses too, saying why: running a target needs
// ADR 0026's pinned runbook, its secrets and its decision, none of which
// is built yet, so no decision is made that nothing would carry out. When
// they are, this is where the deploy's decision is made, and production
// waits on the app's Approve like the button's.
func (e *teamEngine) deploy(ctx context.Context, name, target string) (*typesv1.Decision, error) {
	if !deployTargetRE.MatchString(target) {
		return nil, errors.New("a deploy target is a name from the runbook: lower-case letters, digits and -, up to 40")
	}
	h, err := e.helper(ctx, name)
	if err != nil {
		return nil, err
	}
	found := ""
	for _, p := range runbookPaths(name) {
		info, err := h.GetPathInfo(ctx, &agentv1.GetPathInfoRequest{Path: p})
		if err != nil {
			return nil, errors.New(status.Convert(err).Message())
		}
		if info.GetExists() && !info.GetIsDir() {
			found = p
			break
		}
	}
	if found == "" {
		return nil, errors.New(noRunbookLine) //nolint:staticcheck // ST1005: a plain line, shown as it is
	}
	return nil, status.Errorf(codes.Unimplemented,
		"This project has a deploy runbook (%s), but Portenv can't run deploys yet: the approved runbook and deploy secrets aren't built. Nothing was run.",
		strings.TrimPrefix(found, projectDir(name)+"/"))
}
