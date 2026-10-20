// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// The command list (ADR 0027 §7): every portenv command a person or an
// agent runs about the team, with who may run it and whether it asks the
// owner, as portenv help --json prints it. The app bundles a copy
// (apps/mac/Resources/portenv-commands.json, checked against this by a
// test) for the editor's completions and suggestions. approval is for
// display only: portenvd decides.

// commandsSchema is the list's format; a reader refuses another major.
const commandsSchema = "portenv.commands/1"

// commandsVersion is the list's own version: bumped whenever a command,
// argument or flag changes, and each new command's since.
const commandsVersion = "1.0.0"

type cmdParam struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Repeated bool     `json:"repeated"`
	Choices  []string `json:"choices,omitempty"`
	// Caller, when set, is the callers this one parameter is for, narrower
	// than its command's.
	Caller []string `json:"caller,omitempty"`
}

type cmdSpec struct {
	Path         []string   `json:"path"`
	Aliases      []string   `json:"aliases"`
	Summary      string     `json:"summary"`
	Caller       []string   `json:"caller"`
	Approval     string     `json:"approval"`
	ApprovalNote string     `json:"approval_note"`
	Args         []cmdParam `json:"args"`
	Flags        []cmdParam `json:"flags"`
	Since        string     `json:"since"`
}

var (
	owner      = []string{"owner"}
	agentOnly  = []string{"agent"}
	ownerAgent = []string{"owner", "agent"}
)

func arg(name, typ string, required bool) cmdParam {
	return cmdParam{Name: name, Type: typ, Required: required}
}

func repeated(p cmdParam) cmdParam { p.Repeated = true; return p }

func forCaller(p cmdParam, who []string) cmdParam { p.Caller = who; return p }

func choice(name string, required bool, choices ...string) cmdParam {
	return cmdParam{Name: name, Type: "choice", Required: required, Choices: choices}
}

// workspaceFlag names the workspace when more than one is open.
var workspaceFlag = forCaller(arg("workspace", "string", false), owner)

// jsonFlag is the agent commands' --json.
var jsonFlag = forCaller(arg("json", "bool", false), agentOnly)

// commandRegistry is the list, in the order help shows it.
var commandRegistry = []cmdSpec{
	{
		Path: []string{"msg"}, Summary: "Send a message to an agent on the team's board.",
		Caller: ownerAgent, Approval: "never",
		Args: []cmdParam{arg("agent", "agent_id", true), arg("text", "text", true)},
		Flags: []cmdParam{
			arg("thread", "string", false),
			forCaller(choice("type", false, "notice", "question", "answer"), agentOnly),
			workspaceFlag, jsonFlag,
		},
	},
	{
		Path: []string{"wake"}, Summary: "Ask an idle agent to read its new messages; a busy agent is never typed into.",
		Caller: owner, Approval: "never",
		Args:  []cmdParam{arg("agent", "agent_id", true)},
		Flags: []cmdParam{workspaceFlag},
	},
	{
		Path: []string{"tab", "new"}, Summary: "Open a new tab: a shell for the owner, a command in its own tab for an agent.",
		Caller: ownerAgent, Approval: "never",
		Args:  []cmdParam{forCaller(repeated(arg("command", "command", true)), agentOnly)},
		Flags: []cmdParam{arg("name", "string", true), workspaceFlag, jsonFlag},
	},
	{
		Path: []string{"team", "start"}, Summary: "Start the team's agents, or the ones named.",
		Caller: owner, Approval: "depends", ApprovalNote: "The team's first start asks you in the app.",
		Args:  []cmdParam{repeated(arg("agent", "agent_id", false))},
		Flags: []cmdParam{workspaceFlag},
	},
	{
		Path: []string{"team", "stop"}, Summary: "Stop the team's running agents, or the ones named.",
		Caller: owner, Approval: "never",
		Args:  []cmdParam{repeated(arg("agent", "agent_id", false))},
		Flags: []cmdParam{workspaceFlag},
	},
	{
		Path: []string{"deploy"}, Summary: "Run the project's deploy runbook for a target.",
		Caller: owner, Approval: "depends", ApprovalNote: "Production always asks in the app.",
		Args:  []cmdParam{arg("target", "runbook_target", true)},
		Flags: []cmdParam{workspaceFlag},
	},
	{
		Path: []string{"decision", "list"}, Summary: "List the requests waiting for the owner.",
		Caller: owner, Approval: "never",
		Flags: []cmdParam{arg("all", "bool", false), arg("json", "bool", false), workspaceFlag},
	},
	{
		Path: []string{"decision", "answer"}, Summary: "Check a request and an answer to it; the owner answers it in the app.",
		Caller: owner, Approval: "always", ApprovalNote: "Only the app answers a request, in front of you.",
		Args:  []cmdParam{arg("decision", "decision_id", true), arg("answer", "string", true)},
		Flags: []cmdParam{workspaceFlag},
	},
	{
		Path: []string{"status"}, Summary: "Show what this Mac recorded about a workspace, without starting it.",
		Caller: owner, Approval: "never",
		Args: []cmdParam{arg("workspace", "string", true)},
	},
	{
		Path: []string{"split"}, Summary: "Split the agent's own pane and run a command in the new one.",
		Caller: agentOnly, Approval: "depends", ApprovalNote: "Asks you in the app unless the agent's grant allows panes.",
		Args: []cmdParam{repeated(arg("command", "command", true))},
		Flags: []cmdParam{
			arg("right", "bool", false), arg("down", "bool", false), arg("pane", "string", false), jsonFlag,
		},
	},
	{
		Path: []string{"focus"}, Summary: "Bring one of the agent's own panes to the front.",
		Caller: agentOnly, Approval: "never",
		Args: []cmdParam{arg("pane", "string", true)}, Flags: []cmdParam{jsonFlag},
	},
	{
		Path: []string{"close"}, Summary: "Close one of the agent's own panes.",
		Caller: agentOnly, Approval: "never",
		Args: []cmdParam{arg("pane", "string", true)}, Flags: []cmdParam{jsonFlag},
	},
	{
		Path: []string{"ask-owner"}, Summary: "Ask the owner a question, with up to four choices.",
		Caller: agentOnly, Approval: "always", ApprovalNote: "You answer it in the app.",
		Args:  []cmdParam{arg("text", "text", true)},
		Flags: []cmdParam{repeated(arg("choice", "string", false)), arg("thread", "string", false), jsonFlag},
	},
	{
		Path: []string{"inbox"}, Summary: "Read the agent's messages and answers.",
		Caller: agentOnly, Approval: "never",
		Flags: []cmdParam{arg("wait", "bool", false), arg("since", "string", false), jsonFlag},
	},
	{
		Path: []string{"share"}, Summary: "Tell the owner the agent's work is ready to look at.",
		Caller: agentOnly, Approval: "never",
		Args:  []cmdParam{arg("branch", "string", false)},
		Flags: []cmdParam{arg("text", "text", false), jsonFlag},
	},
	{
		Path: []string{"propose-agent-change"}, Summary: "Propose a change to an agent file.",
		Caller: agentOnly, Approval: "always", ApprovalNote: "You approve the change in the app.",
		Args: []cmdParam{arg("file", "path", true)}, Flags: []cmdParam{jsonFlag},
	},
	{
		Path: []string{"request-package"}, Summary: "Ask for a system package to be installed.",
		Caller: agentOnly, Approval: "always", ApprovalNote: "You approve the exact packages in the app.",
		Args: []cmdParam{arg("name", "string", true)}, Flags: []cmdParam{arg("reason", "text", false), jsonFlag},
	},
	{
		Path: []string{"whoami"}, Summary: "Show the agent's identity, grant and teammates.",
		Caller: agentOnly, Approval: "never", Flags: []cmdParam{jsonFlag},
	},
	{
		Path: []string{"status", "set"}, Summary: "Say what the agent is working on.",
		Caller: agentOnly, Approval: "never",
		Args:  []cmdParam{arg("text", "text", true)},
		Flags: []cmdParam{arg("progress", "string", false), arg("blocked", "text", false), jsonFlag},
	},
	{
		Path: []string{"status", "clear"}, Summary: "Clear what the agent said it's working on.",
		Caller: agentOnly, Approval: "never", Flags: []cmdParam{jsonFlag},
	},
	{
		Path: []string{"team", "status"}, Summary: "Show the teammates' state, if the agent's grant allows it.",
		Caller: agentOnly, Approval: "never", Flags: []cmdParam{jsonFlag},
	},
	{
		Path: []string{"team", "init"}, Summary: "Write a team template's agent files into the project.",
		Caller: owner, Approval: "never",
		Flags: []cmdParam{choice("template", false, "starter", "orchestrate"), arg("dir", "path", false)},
	},
	{
		Path: []string{"guide"}, Summary: "Show the team guide: how agents work in Portenv.",
		Caller: ownerAgent, Approval: "never", Flags: []cmdParam{arg("json", "bool", false)},
	},
	{
		Path: []string{"handoff"}, Summary: "Hand a file or folder to a teammate.",
		Caller: agentOnly, Approval: "never",
		Args:  []cmdParam{arg("agent", "agent_id", true), arg("path", "path", true)},
		Flags: []cmdParam{arg("note", "text", false), jsonFlag},
	},
}

// commandList is the list as JSON, with every list present (never null)
// and every command at commandsVersion.
func commandList() ([]byte, error) {
	out := make([]cmdSpec, len(commandRegistry))
	for i, c := range commandRegistry {
		if c.Aliases == nil {
			c.Aliases = []string{}
		}
		if c.Args == nil {
			c.Args = []cmdParam{}
		}
		if c.Flags == nil {
			c.Flags = []cmdParam{}
		}
		if c.Since == "" {
			c.Since = commandsVersion
		}
		out[i] = c
	}
	b, err := json.MarshalIndent(struct {
		Schema   string    `json:"schema"`
		Version  string    `json:"version"`
		Commands []cmdSpec `json:"commands"`
	}{commandsSchema, commandsVersion, out}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// printCommandList is portenv help --json.
func printCommandList(stdout, stderr io.Writer) int {
	b, err := commandList()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "portenv:", err)
		return 1
	}
	if _, err := stdout.Write(b); err != nil {
		return 1
	}
	return 0
}
