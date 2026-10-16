// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// The in-box commands (ADR 0022 §4): an agent's calls, made through its
// own socket ($PORTENV_SOCKET), which the helper relays to portenvd for
// the grant check. Each call is one protojson line out and one back.

var teamUsage = map[string]string{
	"split":                "split [--right|--down] [--pane PANE] -- COMMAND...",
	"tab":                  "tab new --name NAME -- COMMAND...",
	"focus":                "focus PANE",
	"close":                "close PANE",
	"msg":                  "msg AGENT TEXT [--thread ID] [--type notice|question|answer]",
	"ask-owner":            "ask-owner TEXT [--thread ID]",
	"inbox":                "inbox [--wait] [--since CURSOR]",
	"share":                "share [BRANCH] [--text TEXT]",
	"propose-agent-change": "propose-agent-change FILE",
	"request-package":      "request-package NAME [--reason TEXT]",
	"whoami":               "whoami",
	"status":               "status set TEXT [--progress N/M] [--blocked REASON] | status clear",
	"team":                 "team status",
	"handoff":              "handoff AGENT PATH [--note TEXT]",
}

// maxProposedFile is an agent file's limit (ADR 0022 §3).
const maxProposedFile = 64 << 10

// maxTeamReply caps an answer from the helper (R-0260 item 10).
const maxTeamReply = 1 << 20

// isTeamCommand is whether a command is an agent's own. "status" is also
// the person's command for a workspace, so it's an agent's only inside a
// workspace (with PORTENV_SOCKET set).
func isTeamCommand(name string) bool {
	if name == "status" {
		return false
	}
	_, ok := teamUsage[name]
	return ok
}

// isAgentCommand is isTeamCommand, with "status" inside a workspace.
func isAgentCommand(name string, getenv func(string) string) bool {
	return isTeamCommand(name) || (name == "status" && getenv("PORTENV_SOCKET") != "")
}

// runTeam runs one in-box command and returns its exit status: 0 done,
// 1 refused (the plain line on stderr), 2 a usage mistake.
func runTeam(ctx context.Context, args []string, getenv func(string) string, _ io.Reader, stdout, stderr io.Writer) int {
	name := args[0]
	usageFail := func(msg string) int {
		if msg != "" {
			_, _ = fmt.Fprintln(stderr, "portenv:", msg)
		}
		_, _ = fmt.Fprintf(stderr, "usage: portenv %s [--json]\n", teamUsage[name])
		return 2
	}
	sock := getenv("PORTENV_SOCKET")
	if sock == "" {
		_, _ = fmt.Fprintf(stderr, "portenv: %s works only for an agent inside a workspace (PORTENV_SOCKET isn't set).\n", name)
		return 2
	}
	call, jsonOut, err := buildTeamCall(name, args[1:], getenv)
	if err != nil {
		return usageFail(err.Error())
	}
	reply, err := sendTeamCall(ctx, sock, call)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "portenv:", err)
		return 1
	}
	if !reply.GetOk() {
		line := reply.GetLine()
		if line == "" {
			line = "Portenv refused that, without saying why."
		}
		_, _ = fmt.Fprintln(stderr, "portenv:", line)
		return 1
	}
	printTeamReply(stdout, name, call, reply, jsonOut)
	return 0
}

// teamArgs splits args into flags, positional words and the words after
// "--". Flags may come anywhere before "--".
type teamArgs struct {
	flags map[string]string
	pos   []string
	rest  []string
	dash  bool
}

func parseTeamArgs(args []string, bools, values []string) (teamArgs, error) {
	a := teamArgs{flags: map[string]string{}}
	isIn := func(list []string, s string) bool {
		for _, v := range list {
			if v == s {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "--":
			a.dash, a.rest = true, args[i+1:]
			return a, nil
		case strings.HasPrefix(s, "--"):
			f := strings.TrimPrefix(s, "--")
			switch {
			case isIn(bools, f):
				a.flags[f] = "1"
			case isIn(values, f):
				if i+1 >= len(args) {
					return a, fmt.Errorf("--%s needs a value", f)
				}
				i++
				a.flags[f] = args[i]
			default:
				return a, fmt.Errorf("unknown flag --%s", f)
			}
		default:
			a.pos = append(a.pos, s)
		}
	}
	return a, nil
}

func buildTeamCall(name string, args []string, getenv func(string) string) (*agentv1.TeamCallsResponse, bool, error) {
	bools, values := []string{"json"}, []string(nil)
	switch name {
	case "split":
		bools, values = append(bools, "right", "down"), []string{"pane"}
	case "tab":
		values = []string{"name"}
	case "msg":
		values = []string{"thread", "type"}
	case "ask-owner":
		values = []string{"thread"}
	case "inbox":
		bools, values = append(bools, "wait"), []string{"since"}
	case "share":
		values = []string{"text"}
	case "request-package":
		values = []string{"reason"}
	case "status":
		values = []string{"progress", "blocked"}
	case "handoff":
		values = []string{"note"}
	}
	a, err := parseTeamArgs(args, bools, values)
	if err != nil {
		return nil, false, err
	}
	jsonOut := a.flags["json"] != ""
	needPos := func(n int) error {
		if len(a.pos) != n {
			return errors.New("wrong number of arguments")
		}
		return nil
	}
	needCommand := func() error {
		if !a.dash || len(a.rest) == 0 {
			return errors.New("give the command to run after --")
		}
		return nil
	}
	c := &agentv1.TeamCallsResponse{}
	switch name {
	case "split":
		if err := needPos(0); err != nil {
			return nil, false, err
		}
		if err := needCommand(); err != nil {
			return nil, false, err
		}
		if a.flags["right"] != "" && a.flags["down"] != "" {
			return nil, false, errors.New("choose --right or --down, not both")
		}
		pane := a.flags["pane"]
		if pane == "" {
			pane = getenv("TMUX_PANE")
		}
		c.Op = &agentv1.TeamCallsResponse_Split{Split: &agentv1.Split{PaneId: pane, Down: a.flags["down"] != "", Argv: a.rest}}
	case "tab":
		if len(a.pos) != 1 || a.pos[0] != "new" {
			return nil, false, errors.New(`the only tab command is "tab new"`)
		}
		if a.flags["name"] == "" {
			return nil, false, errors.New("give the tab a --name")
		}
		if err := needCommand(); err != nil {
			return nil, false, err
		}
		c.Op = &agentv1.TeamCallsResponse_Tab{Tab: &agentv1.NewTeamTab{Name: a.flags["name"], Argv: a.rest}}
	case "focus", "close":
		if err := needPos(1); err != nil {
			return nil, false, err
		}
		ref := &agentv1.PaneRef{PaneId: a.pos[0]}
		if name == "focus" {
			c.Op = &agentv1.TeamCallsResponse_Focus{Focus: ref}
		} else {
			c.Op = &agentv1.TeamCallsResponse_Close{Close: ref}
		}
	case "msg":
		if err := needPos(2); err != nil {
			return nil, false, err
		}
		t := typesv1.MessageType_MESSAGE_TYPE_NOTICE
		switch a.flags["type"] {
		case "", "notice":
		case "question":
			t = typesv1.MessageType_MESSAGE_TYPE_QUESTION
		case "answer":
			t = typesv1.MessageType_MESSAGE_TYPE_ANSWER
		default:
			return nil, false, errors.New("--type is notice, question or answer")
		}
		c.Op = &agentv1.TeamCallsResponse_Msg{Msg: &agentv1.Msg{To: a.pos[0], Text: a.pos[1], ThreadId: a.flags["thread"], Type: t}}
	case "ask-owner":
		if err := needPos(1); err != nil {
			return nil, false, err
		}
		c.Op = &agentv1.TeamCallsResponse_AskOwner{AskOwner: &agentv1.AskOwner{Text: a.pos[0], ThreadId: a.flags["thread"]}}
	case "inbox":
		if err := needPos(0); err != nil {
			return nil, false, err
		}
		c.Op = &agentv1.TeamCallsResponse_Inbox{Inbox: &agentv1.Inbox{Wait: a.flags["wait"] != "", SinceCursor: a.flags["since"]}}
	case "share":
		if len(a.pos) > 1 {
			return nil, false, errors.New("wrong number of arguments")
		}
		s := &agentv1.Share{Text: a.flags["text"]}
		if len(a.pos) == 1 {
			s.Branch = a.pos[0]
		}
		c.Op = &agentv1.TeamCallsResponse_Share{Share: s}
	case "propose-agent-change":
		if err := needPos(1); err != nil {
			return nil, false, err
		}
		content, err := readProposed(a.pos[0])
		if err != nil {
			return nil, false, err
		}
		id := strings.TrimSuffix(filepath.Base(a.pos[0]), ".md")
		c.Op = &agentv1.TeamCallsResponse_Propose{Propose: &agentv1.ProposeAgentChange{AgentId: id, Content: content}}
	case "request-package":
		if err := needPos(1); err != nil {
			return nil, false, err
		}
		c.Op = &agentv1.TeamCallsResponse_RequestPackage{RequestPackage: &agentv1.RequestPackage{Name: a.pos[0], Reason: a.flags["reason"]}}
	case "whoami":
		if err := needPos(0); err != nil {
			return nil, false, err
		}
		c.Op = &agentv1.TeamCallsResponse_Whoami{Whoami: &agentv1.WhoAmI{}}
	case "status":
		switch {
		case len(a.pos) == 1 && a.pos[0] == "clear" && a.flags["progress"] == "" && a.flags["blocked"] == "":
			c.Op = &agentv1.TeamCallsResponse_SetStatus{SetStatus: &agentv1.SetStatus{Clear: true}}
		case len(a.pos) == 2 && a.pos[0] == "set":
			s := &agentv1.SetStatus{Text: a.pos[1], Blocked: a.flags["blocked"]}
			if p := a.flags["progress"]; p != "" {
				var done, total int32
				if n, err := fmt.Sscanf(p, "%d/%d", &done, &total); err != nil || n != 2 || done < 0 || total <= 0 || done > total || fmt.Sprintf("%d/%d", done, total) != p {
					return nil, false, errors.New("--progress is N/M, like 3/8")
				}
				s.ProgressDone, s.ProgressTotal = done, total
			}
			c.Op = &agentv1.TeamCallsResponse_SetStatus{SetStatus: s}
		default:
			return nil, false, errors.New(`use "status set TEXT" or "status clear"`)
		}
	case "team":
		if len(a.pos) != 1 || a.pos[0] != "status" {
			return nil, false, errors.New(`the only team command here is "team status"`)
		}
		c.Op = &agentv1.TeamCallsResponse_TeamStatus{TeamStatus: &agentv1.TeamStatus{}}
	case "handoff":
		if err := needPos(2); err != nil {
			return nil, false, err
		}
		path, err := filepath.Abs(a.pos[1])
		if err != nil {
			return nil, false, err
		}
		c.Op = &agentv1.TeamCallsResponse_Handoff{Handoff: &agentv1.Handoff{To: a.pos[0], Path: path, Note: a.flags["note"]}}
	}
	if a.dash && name != "split" && name != "tab" {
		return nil, false, errors.New(`"--" is only for split and tab new`)
	}
	return c, jsonOut, nil
}

func readProposed(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- the agent names its own file
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxProposedFile+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxProposedFile {
		return "", errors.New("an agent file is at most 64 KB")
	}
	return string(b), nil
}

// sendTeamCall sends one call to the agent's socket and reads the answer.
func sendTeamCall(ctx context.Context, sock string, call *agentv1.TeamCallsResponse) (*agentv1.TeamCallsRequest, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, fmt.Errorf("can't reach Portenv in this workspace: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// inbox --wait waits up to a minute; the helper gives up at 90 s.
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	b, err := protojson.Marshal(call)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("can't reach Portenv in this workspace: %w", err)
	}
	// The whole call is sent: say so, so the helper answers at once.
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	line, err := bufio.NewReaderSize(io.LimitReader(conn, maxTeamReply+1), 64<<10).ReadBytes('\n')
	if len(line) > maxTeamReply {
		return nil, errors.New("Portenv's answer is too large to show") //nolint:staticcheck // ST1005: a plain line
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("no answer from Portenv: %w", err)
	}
	var r agentv1.TeamCallsRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("the answer from Portenv couldn't be read: %w", err)
	}
	return &r, nil
}

func printTeamReply(w io.Writer, name string, call *agentv1.TeamCallsResponse, r *agentv1.TeamCallsRequest, jsonOut bool) {
	var result proto.Message
	switch x := r.GetResult().(type) {
	case *agentv1.TeamCallsRequest_Pane:
		result = x.Pane
	case *agentv1.TeamCallsRequest_Inbox:
		result = x.Inbox
	case *agentv1.TeamCallsRequest_Decision:
		result = x.Decision
	case *agentv1.TeamCallsRequest_Message:
		result = x.Message
	case *agentv1.TeamCallsRequest_Whoami:
		result = x.Whoami
	case *agentv1.TeamCallsRequest_TeamStatus:
		result = x.TeamStatus
	}
	if jsonOut {
		if result == nil {
			_, _ = fmt.Fprintln(w, "{}")
			return
		}
		b, _ := protojson.Marshal(result)
		_, _ = fmt.Fprintln(w, string(b))
		return
	}
	switch name {
	case "split", "tab":
		_, _ = fmt.Fprintf(w, "Opened pane %s.\n", shownPlain(r.GetPane().GetPaneId()))
	case "msg":
		_, _ = fmt.Fprintf(w, "Sent to %s.\n", shownPlain(call.GetMsg().GetTo()))
	case "status":
		if call.GetSetStatus().GetClear() {
			_, _ = fmt.Fprintln(w, "Status cleared.")
		} else {
			_, _ = fmt.Fprintln(w, "Status set.")
		}
	case "handoff":
		m := r.GetMessage()
		_, _ = fmt.Fprintf(w, "Handed %d files to %s.\n", len(m.GetFiles()), shownPlain(call.GetHandoff().GetTo()))
		for _, f := range m.GetFiles() {
			_, _ = fmt.Fprintf(w, "  %s  %s\n", shownPlain(f.GetSha256()), shownPlain(f.GetPath()))
		}
	case "team":
		for _, m := range r.GetTeamStatus().GetTeammates() {
			line := strings.ToLower(strings.TrimPrefix(m.GetState().String(), "AGENT_STATE_"))
			if st := m.GetStatus(); st != nil {
				line += " · " + st.GetText()
				if st.GetBlocked() != "" {
					line += " · blocked: " + st.GetBlocked()
				}
			}
			_, _ = fmt.Fprintf(w, "%s  %s\n", shownPlain(m.GetId()), shownPlain(line))
		}
	case "ask-owner", "request-package", "propose-agent-change":
		_, _ = fmt.Fprintf(w, "Asked the owner (decision %s). Wait for the answer with: portenv inbox --wait\n", shownPlain(r.GetDecision().GetId()))
	case "inbox":
		in := r.GetInbox()
		for _, m := range in.GetMessages() {
			_, _ = fmt.Fprintf(w, "%s  %s → %s  %s: %s\n", m.GetCreatedAt().AsTime().Format(time.DateTime), shownPlain(m.GetFrom()), shownPlain(m.GetTo()),
				strings.ToLower(strings.TrimPrefix(m.GetType().String(), "MESSAGE_TYPE_")), shownPlain(m.GetText()))
		}
		for _, d := range in.GetDecisions() {
			_, _ = fmt.Fprintf(w, "decision %s: %s\n", shownPlain(d.GetId()), strings.ToLower(strings.TrimPrefix(d.GetState().String(), "DECISION_STATE_")))
		}
		if in.GetCursor() != "" {
			_, _ = fmt.Fprintf(w, "(next: portenv inbox --since %s)\n", shownPlain(in.GetCursor()))
		}
	case "whoami":
		me := r.GetWhoami()
		_, _ = fmt.Fprintf(w, "You are %s (%s) in %s, on %s.\n", shownPlain(me.GetName()), shownPlain(me.GetId()), shownPlain(me.GetWorkspace()), shownPlain(me.GetLocation()))
		if len(me.GetCommands()) > 0 {
			_, _ = fmt.Fprintf(w, "You can use: %s\n", shownPlain(strings.Join(me.GetCommands(), ", ")))
		}
		_, _ = fmt.Fprintln(w, "For everything, run: portenv whoami --json")
	default:
		_, _ = fmt.Fprintln(w, "Done.")
	}
}

// shownPlain escapes what a terminal would act on (control and bidi
// characters) in text other agents wrote: it's shown, never run.
func shownPlain(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
