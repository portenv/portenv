// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// An ask-owner with --choice (CC-0501).

func askChoices(e *teamEngine, from, text string, choices ...string) *agentv1.TeamCallsRequest {
	return e.handleCall(context.Background(), "w", &agentv1.TeamCallsResponse{AgentId: from,
		Op: &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: text, Choices: choices}}})
}

func TestAskOwnerChoicesAreChecked(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	long := strings.Repeat("é", maxChoiceCharacters)
	for _, c := range []struct {
		name    string
		choices []string
		ok      bool
	}{
		{"two", []string{"Option a", "Option b"}, true},
		{"four", []string{"a", "b", "c", "d"}, true},
		{"80 characters, not bytes", []string{long, "b"}, true},
		{"one", []string{"only"}, false},
		{"five", []string{"a", "b", "c", "d", "e"}, false},
		{"empty label", []string{"a", ""}, false},
		{"blank label", []string{"a", "   "}, false},
		{"81 characters", []string{long + "x", "b"}, false},
		{"a newline", []string{"a\nb", "c"}, false},
		{"a tab", []string{"a\tb", "c"}, false},
		{"a control character", []string{"a\x1b[31m", "c"}, false},
		{"a bidi override", []string{"a\u202eb", "c"}, false},
		{"an isolate", []string{"a\u2066b", "c"}, false},
		{"invalid UTF-8", []string{"a\xff", "c"}, false},
		{"the same twice", []string{"same", "same"}, false},
	} {
		before := len(h.boardLines())
		r := askChoices(e, "lead", "Which way?", c.choices...)
		if r.GetOk() != c.ok {
			t.Errorf("%s: ok = %v (%s), want %v", c.name, r.GetOk(), r.GetLine(), c.ok)
			continue
		}
		if !c.ok {
			if r.GetLine() == "" {
				t.Errorf("%s: refused without a line", c.name)
			}
			if len(h.boardLines()) != before {
				t.Errorf("%s: a refused ask reached the board", c.name)
			}
			continue
		}
		if d := r.GetDecision(); !slices.Equal(d.GetChoices(), c.choices) || d.GetChoice() != 0 {
			t.Errorf("%s: decision choices %q, choice %d", c.name, d.GetChoices(), d.GetChoice())
		}
		// Answer it so the pending limit doesn't get in the way.
		if _, err := e.answer(context.Background(), "w", r.GetDecision().GetId(), false, ""); err != nil {
			t.Fatalf("%s: decline: %v", c.name, err)
		}
	}
}

func TestAskOwnerChoiceRoundTrip(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	choices := []string{"Option a: wait", "Option b: install deps", "Option c: skip it"}
	d := askChoices(e, "builder", "Tests need a library. What now?", choices...).GetDecision()
	if d == nil {
		t.Fatal("ask with choices refused")
	}
	// A fresh portenvd answers it, from the board alone: the choices are
	// stored with the decision, not in portenvd's memory.
	fresh := newTeamEngine(
		func(context.Context, string) (agentv1.AgentServiceClient, error) { return h, nil },
		func(context.Context, string) (Team, error) { return Team{Agents: defaultTeam()}, nil },
	)
	fresh.now = e.now
	fresh.readBoard = func(context.Context, string) ([]*agentv1.ReadBoardResponse, error) { return h.boardLines(), nil }
	x, err := fresh.answerChoice(ctx, "w", d.GetId(), ownerAnswer{approve: true, choice: 2, label: "Option b: install deps"})
	if err != nil {
		t.Fatalf("choose b: %v", err)
	}
	if x.GetState() != typesv1.DecisionState_DECISION_STATE_APPROVED || x.GetChoice() != 2 || !slices.Equal(x.GetChoices(), choices) {
		t.Fatalf("answered: %v", x)
	}
	// The agent's inbox carries the choice.
	in := inbox(t, fresh, "builder", "")
	var got *typesv1.Decision
	for _, y := range in.GetDecisions() {
		if y.GetId() == d.GetId() && y.GetState() == typesv1.DecisionState_DECISION_STATE_APPROVED {
			got = y
		}
	}
	if got == nil || got.GetChoice() != 2 || got.GetChoices()[got.GetChoice()-1] != "Option b: install deps" {
		t.Fatalf("builder's inbox: %v", in.GetDecisions())
	}
	// Answered once only.
	if _, err := fresh.answerChoice(ctx, "w", d.GetId(), ownerAnswer{approve: true, choice: 1, label: choices[0]}); err == nil {
		t.Error("a chosen decision was answered again")
	}
}

func TestForgedChoiceIsRefused(t *testing.T) {
	e, h, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	choices := []string{"Option a", "Option b"}
	d := askChoices(e, "builder", "Which?", choices...).GetDecision()
	before := len(h.boardLines())
	for _, a := range []struct {
		name string
		a    ownerAnswer
	}{
		{"no choice", ownerAnswer{approve: true}},
		{"index 0", ownerAnswer{approve: true, choice: 0, label: "Option a"}},
		{"index past the end", ownerAnswer{approve: true, choice: 3, label: "Option b"}},
		{"negative index", ownerAnswer{approve: true, choice: -1, label: "Option a"}},
		{"label of another index", ownerAnswer{approve: true, choice: 1, label: "Option b"}},
		{"a label it never offered", ownerAnswer{approve: true, choice: 1, label: "Option z"}},
		{"label without index", ownerAnswer{approve: true, label: "Option a"}},
		{"decline with a choice", ownerAnswer{approve: false, choice: 1, label: "Option a"}},
	} {
		if x, err := e.answerChoice(ctx, "w", d.GetId(), a.a); err == nil {
			t.Errorf("%s: accepted: %v", a.name, x)
		}
	}
	if len(h.boardLines()) != before {
		t.Error("a refused answer reached the board")
	}
	if p := e.box("w").pendingLocked("builder"); len(p) != 1 {
		t.Fatalf("pending after forged answers: %d", len(p))
	}
	// Decline still works, with no choice.
	x, err := e.answerChoice(ctx, "w", d.GetId(), ownerAnswer{note: "neither"})
	if err != nil || x.GetState() != typesv1.DecisionState_DECISION_STATE_DECLINED || x.GetChoice() != 0 {
		t.Fatalf("decline: %v, %v", x, err)
	}
}

func TestAskOwnerWithoutChoicesIsUnchanged(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	ctx := context.Background()
	d := askChoices(e, "builder", "May I add left-pad?").GetDecision()
	if d == nil || len(d.GetChoices()) != 0 {
		t.Fatalf("yes/no ask: %v", d)
	}
	if _, err := e.answerChoice(ctx, "w", d.GetId(), ownerAnswer{approve: true, choice: 1, label: "yes"}); err == nil {
		t.Error("a yes/no decision took a choice")
	}
	x, err := e.answer(ctx, "w", d.GetId(), true, "")
	if err != nil || x.GetState() != typesv1.DecisionState_DECISION_STATE_APPROVED || x.GetChoice() != 0 {
		t.Fatalf("approve: %v, %v", x, err)
	}
}

// The app's AnswerDecision carries the choice and its label to the check.
func TestAnswerDecisionRPCCarriesTheChoice(t *testing.T) {
	e, _, _ := newTestEngine(t, defaultTeam()...)
	s := &Server{}
	s.teamOnce.Do(func() {})
	s.team = e
	ctx := context.Background()
	d := askChoices(e, "builder", "Which?", "Option a", "Option b").GetDecision()
	_, err := s.AnswerDecision(ctx, &daemonv1.AnswerDecisionRequest{Name: "w", DecisionId: d.GetId(), Approve: true, Choice: 2, ChoiceLabel: "Option a"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("forged label over gRPC: %v", err)
	}
	r, err := s.AnswerDecision(ctx, &daemonv1.AnswerDecisionRequest{Name: "w", DecisionId: d.GetId(), Approve: true, Choice: 2, ChoiceLabel: "Option b"})
	if err != nil || r.GetDecision().GetChoice() != 2 {
		t.Fatalf("choose b over gRPC: %v, %v", r, err)
	}
}
