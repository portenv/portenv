// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The owner's team commands on the Mac (ADR 0027 §5, S3): msg, wake, tab
// new, team start and stop, deploy and decision. Each is one portenvd
// call, the same one the app makes, on the owner-only socket, so portenvd
// decides exactly as it does for the app: the CLI has no power the app
// lacks, and no flag skips an approval. An agent's portenv never runs
// these: it has only its grant socket (msg and tab new there are its own,
// grant-checked calls), and the workspace's own shell has no owner socket.

// ownerUsage is each owner command's usage line.
var ownerUsage = map[string]string{
	"msg":      "msg AGENT TEXT [--thread ID] [--workspace NAME]",
	"wake":     "wake AGENT [--workspace NAME]",
	"tab":      "tab new --name NAME [--workspace NAME]",
	"team":     "team start [AGENT...] | team stop [AGENT...]   [--workspace NAME]",
	"deploy":   "deploy TARGET [--workspace NAME]",
	"decision": "decision list [--all] [--json] | decision answer ID approve|decline|N   [--workspace NAME]",
}

// ownerTeamCommand is whether args name one of the owner's team commands.
// msg and tab are also an agent's own; team is the owner's only for start
// and stop.
func ownerTeamCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "msg", "tab", "wake", "deploy", "decision":
		return true
	case "team":
		return len(args) > 1 && (args[1] == "start" || args[1] == "stop")
	}
	return false
}

// sharedWithAgents is whether an owner's command is also an agent's, which
// an agent's portenv sends through its own socket.
func sharedWithAgents(name string) bool { return name == "msg" || name == "tab" }

// ownerDial connects to portenvd.
type ownerDial func() (daemonv1.DaemonServiceClient, func(), error)

// runOwner runs one of the owner's team commands and returns its exit
// status: 0 done, 1 refused (portenvd's plain line on stderr), 2 a usage
// mistake.
func runOwner(ctx context.Context, args []string, dial ownerDial, stdout, stderr io.Writer) int {
	name := args[0]
	usageFail := func(msg string) int {
		if msg != "" {
			_, _ = fmt.Fprintln(stderr, "portenv:", msg)
		}
		_, _ = fmt.Fprintf(stderr, "usage: portenv %s\n", ownerUsage[name])
		return 2
	}
	bools, values := []string(nil), []string{"workspace"}
	switch name {
	case "msg":
		values = append(values, "thread")
	case "tab":
		values = append(values, "name")
	case "decision":
		bools = []string{"all", "json"}
	}
	a, err := parseTeamArgs(args[1:], bools, values)
	if err != nil {
		return usageFail(err.Error())
	}
	// Check the words before dialling: a usage mistake reaches no one.
	var run func(c daemonv1.DaemonServiceClient, ws string) error
	switch name {
	case "msg":
		if len(a.pos) != 2 || a.dash {
			return usageFail("give the agent and the text (quote it)")
		}
		run = func(c daemonv1.DaemonServiceClient, ws string) error {
			r, err := c.SendMessage(ctx, &daemonv1.SendMessageRequest{Name: ws, To: a.pos[0], Text: a.pos[1], ThreadId: a.flags["thread"]})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Sent to %s (message %s).\n", a.pos[0], r.GetMessage().GetId())
			return nil
		}
	case "wake":
		if len(a.pos) != 1 || a.dash {
			return usageFail("give the agent to wake")
		}
		run = func(c daemonv1.DaemonServiceClient, ws string) error {
			if _, err := c.WakeAgent(ctx, &daemonv1.WakeAgentRequest{Name: ws, AgentId: a.pos[0]}); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Portenv asked %s to read its inbox.\n", a.pos[0])
			return nil
		}
	case "tab":
		if len(a.pos) != 1 || a.pos[0] != "new" {
			return usageFail(`the only tab command is "tab new"`)
		}
		if a.dash {
			// The app's New Tab opens a shell, and so does this: the owner's
			// CLI runs nothing the app can't.
			return usageFail("tab new opens a shell tab; type the command in it")
		}
		if a.flags["name"] == "" {
			return usageFail("give the tab a --name")
		}
		run = func(c daemonv1.DaemonServiceClient, ws string) error {
			r, err := c.NewTab(ctx, &daemonv1.NewTabRequest{Box: ws, Name: a.flags["name"]})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Opened tab %s (%s).\n", r.GetTab().GetId(), r.GetTab().GetName())
			return nil
		}
	case "team":
		if a.dash {
			return usageFail("")
		}
		start := a.pos[0] == "start"
		ids := a.pos[1:]
		run = func(c daemonv1.DaemonServiceClient, ws string) error {
			return teamStartStop(ctx, c, ws, start, ids, stdout, stderr)
		}
	case "deploy":
		if len(a.pos) != 1 || a.dash {
			return usageFail("give the runbook's target, for example staging")
		}
		run = func(c daemonv1.DaemonServiceClient, ws string) error {
			r, err := c.Deploy(ctx, &daemonv1.DeployRequest{Name: ws, Target: a.pos[0]})
			if err != nil {
				return err
			}
			// Once deploys run, a deploy waits on the app's Approve, like
			// the button's.
			if d := r.GetDecision(); d != nil {
				_, _ = fmt.Fprintf(stdout, "Waiting for your approval in Portenv: %s\n", d.GetSummary())
			}
			return nil
		}
	case "decision":
		if a.dash || len(a.pos) == 0 {
			return usageFail("")
		}
		switch a.pos[0] {
		case "list":
			if len(a.pos) != 1 {
				return usageFail("")
			}
			run = func(c daemonv1.DaemonServiceClient, ws string) error {
				return listDecisions(ctx, c, ws, a.flags["all"] != "", a.flags["json"] != "", stdout)
			}
		case "answer":
			if len(a.pos) != 3 || a.flags["all"] != "" || a.flags["json"] != "" {
				return usageFail("give the request's ID and approve, decline or a choice's number")
			}
			run = func(c daemonv1.DaemonServiceClient, ws string) error {
				return answerInTheApp(ctx, c, ws, a.pos[1], a.pos[2])
			}
		default:
			return usageFail("")
		}
	}
	c, done, err := dial()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "portenv:", err)
		return 1
	}
	defer done()
	ws, err := ownerWorkspace(ctx, c, a.flags["workspace"])
	if err != nil {
		var u usageError
		if errors.As(err, &u) {
			return usageFail(u.Msg)
		}
		_, _ = fmt.Fprintln(stderr, "portenv:", plain(err))
		return 1
	}
	if err := run(c, ws); err != nil {
		var u usageError
		if errors.As(err, &u) {
			return usageFail(u.Msg)
		}
		if !errors.As(err, new(quietError)) {
			_, _ = fmt.Fprintln(stderr, "portenv:", plain(err))
		}
		return 1
	}
	return 0
}

// ownerWorkspace is the named workspace, or the one open on this Mac.
func ownerWorkspace(ctx context.Context, c daemonv1.DaemonServiceClient, named string) (string, error) {
	if named != "" {
		return named, nil
	}
	r, err := c.ListBoxes(ctx, &daemonv1.ListBoxesRequest{})
	if err != nil {
		return "", err
	}
	var open []string
	for _, b := range r.GetBoxes() {
		if b.GetState() == typesv1.BoxState_BOX_STATE_RUNNING {
			open = append(open, b.GetBox().GetName())
		}
	}
	switch len(open) {
	case 0:
		return "", errors.New("No workspace is open. Open one in Portenv, or name it with --workspace NAME.") //nolint:staticcheck // ST1005: a plain line, shown as it is
	case 1:
		return open[0], nil
	}
	return "", usageError{Msg: "several workspaces are open (" + strings.Join(open, ", ") + "); name one with --workspace NAME"}
}

// waitingForApproval is portenvd's refusal while the team's first start
// waits on the owner's Approve in the app.
const waitingForApproval = "approve the team's first start in Portenv first"

// teamStartStop starts or stops the named agents, or with none named,
// every agent the app's Start Team or Stop Team would: to start, the
// approved, local, stopped ones; to stop, the running and starting ones.
// Each is StartAgent or StopAgent, so a start that needs the owner's
// approval waits for it in the app, and the CLI never gives it.
func teamStartStop(ctx context.Context, c daemonv1.DaemonServiceClient, ws string, start bool, ids []string, stdout, stderr io.Writer) error {
	if len(ids) == 0 {
		r, err := c.ListAgents(ctx, &daemonv1.ListAgentsRequest{Name: ws})
		if err != nil {
			return err
		}
		for _, ag := range r.GetAgents() {
			running := ag.GetState() == typesv1.AgentState_AGENT_STATE_RUNNING || ag.GetState() == typesv1.AgentState_AGENT_STATE_STARTING
			if (start && ag.GetApproved() && !ag.GetRemote() && !running) || (!start && running) {
				ids = append(ids, ag.GetId())
			}
		}
		if len(ids) == 0 {
			if start {
				_, _ = fmt.Fprintln(stdout, "No agent to start: each is running, remote, or waiting for you to approve its file.")
			} else {
				_, _ = fmt.Fprintln(stdout, "No agent is running.")
			}
			return nil
		}
	}
	failed, waiting := false, false
	for _, id := range ids {
		var err error
		if start {
			_, err = c.StartAgent(ctx, &daemonv1.StartAgentRequest{Name: ws, AgentId: id})
		} else {
			_, err = c.StopAgent(ctx, &daemonv1.StopAgentRequest{Name: ws, AgentId: id})
		}
		switch {
		case err != nil && start && status.Convert(err).Message() == waitingForApproval:
			waiting = true
		case err != nil:
			failed = true
			_, _ = fmt.Fprintf(stderr, "portenv: %s: %v\n", id, plain(err))
		case start:
			_, _ = fmt.Fprintf(stdout, "Started %s.\n", id)
		default:
			_, _ = fmt.Fprintf(stdout, "Stopped %s.\n", id)
		}
	}
	if waiting {
		_, _ = fmt.Fprintln(stdout, "Waiting for your approval in Portenv to start the team: Approve it in the Team sidebar, and the agents start then.")
	}
	if failed {
		return quietError{errors.New("some agents didn't " + map[bool]string{true: "start", false: "stop"}[start])}
	}
	return nil
}

// listDecisions prints the workspace's decisions: one line each, with any
// choices numbered beneath, or JSON.
func listDecisions(ctx context.Context, c daemonv1.DaemonServiceClient, ws string, all, asJSON bool, stdout io.Writer) error {
	r, err := c.ListDecisions(ctx, &daemonv1.ListDecisionsRequest{Name: ws, All: all})
	if err != nil {
		return err
	}
	if asJSON {
		parts := make([]string, 0, len(r.GetDecisions()))
		for _, d := range r.GetDecisions() {
			b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(d)
			if err != nil {
				return err
			}
			parts = append(parts, string(b))
		}
		_, _ = fmt.Fprintf(stdout, "[%s]\n", strings.Join(parts, ","))
		return nil
	}
	if len(r.GetDecisions()) == 0 {
		_, _ = fmt.Fprintln(stdout, "Nothing is waiting for you.")
		return nil
	}
	for _, d := range r.GetDecisions() {
		state := ""
		if all && d.GetState() != typesv1.DecisionState_DECISION_STATE_PENDING {
			state = "  " + strings.ToLower(strings.TrimPrefix(d.GetState().String(), "DECISION_STATE_"))
		}
		_, _ = fmt.Fprintf(stdout, "%s  %s  %s%s\n", d.GetId(), d.GetFrom(), oneLine(d.GetSummary()), state)
		for i, ch := range d.GetChoices() {
			_, _ = fmt.Fprintf(stdout, "    %d %q\n", i+1, ch)
		}
	}
	return nil
}

// oneLine keeps a summary to one printable line.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// answerInTheApp checks an answer against the request, then points to the
// app: only the app answers a decision, in front of the person (ADR 0022
// §6), so the CLI never calls AnswerDecision. Whether the owner's CLI may
// answer is an open question (docs/PLAN.md).
func answerInTheApp(ctx context.Context, c daemonv1.DaemonServiceClient, ws, id, answer string) error {
	r, err := c.ListDecisions(ctx, &daemonv1.ListDecisionsRequest{Name: ws, All: true})
	if err != nil {
		return err
	}
	var d *typesv1.Decision
	for _, x := range r.GetDecisions() {
		if x.GetId() == id {
			d = x
		}
	}
	if d == nil {
		return fmt.Errorf("there's no request %s in %s", id, ws)
	}
	n, numErr := strconv.Atoi(answer)
	switch {
	case answer == "approve" || answer == "decline":
		if answer == "approve" && len(d.GetChoices()) > 0 {
			return usageError{Msg: fmt.Sprintf("this request has choices: choose 1 to %d, or decline", len(d.GetChoices()))}
		}
	case numErr == nil && len(d.GetChoices()) == 0:
		return usageError{Msg: "this request has no choices: approve or decline it"}
	case numErr == nil && (n < 1 || n > len(d.GetChoices())):
		return usageError{Msg: fmt.Sprintf("choose 1 to %d, or decline", len(d.GetChoices()))}
	case numErr != nil:
		return usageError{Msg: "answer approve, decline or a choice's number"}
	}
	if d.GetState() != typesv1.DecisionState_DECISION_STATE_PENDING {
		return errors.New("that request was already answered, or it expired")
	}
	return status.Errorf(codes.FailedPrecondition,
		"Answer it in Portenv: only the app answers a request, in front of you. %q from %s is waiting in the Team sidebar.",
		oneLine(d.GetSummary()), d.GetFrom())
}
