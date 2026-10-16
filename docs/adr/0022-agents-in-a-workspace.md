# 0022. Agents in a workspace: a team the owner approves, each agent its own user, one grant check for every way in

Date: 2026-10-16 · Status: proposed (R-0226, R-0227, R-0231, R-0232; lane A's tmux findings, CC-0345). No code before the reviewer approves it. Per-agent users are the owner's decision (13:19, R-0231), not a choice left in this ADR.

## Context

The owner wants "the agent team" in the tester build (R-0226): agents described in files in the repository, started by `portenvd` in the workspace, each in its own tab, talking to each other and to the owner through a board, with a Team section in the app. Outside agents (Grok Bot, Dots, Muse) must be able to work in a workspace too, through SSH, an MCP connector or web terminals (R-0227).

What's already settled, and what this ADR builds on:
- **ADR 0006:** the owner owns every box; agents are guests; two modes, stand-in (the owner's access) and lane (its own Unix user, `/home/<agent>`, own tmux, own worktree, limits). One core, thin doors, no door bypasses the checks.
- **ADR 0016:** one agent identity (a stable owner-scoped ID and a handle), one pairing flow (`portenv connect`, a device code the owner approves), one credential record per door, one grant model (subject, object, capability, approved_by, state, with an audit trail), one addressed message type (`id`, `from`, `to`, `thread_id`, `type`, `visibility`, `created_at`), an append-only event log, ULIDs.
- **ADR 0010:** `portenvd` reaches the box only through the box agent ("the helper") over pinned TLS on the Mac's loopback; never `docker exec`, never the engine's socket in a box.
- **ADR 0020:** each tab is its own terminal; every command carries an author set by `portenvd` from the caller's identity, never from the request (`CommandAuthor`, `author_agent_id`); `caller` in `core/daemon/caller.go` already has an agent kind waiting for this ADR.
- **ADR 0015:** public doors only on the user's own server, opt-in per server, off by default; the Mac never opens an inbound port.

Facts about today's workspace that decide the design:
- Everything the person runs in a box runs as `work` (uid 1000), and `work` has `NOPASSWD: ALL` sudo (`images/toolbox-node/rootfs/etc/sudoers.d/work`).
- The workspace's tmux server runs as `work`, and its socket is reachable by any `work` process.
- **Lane A's findings (CC-0345):** an agent running as `work` can, with plain `tmux`: make tabs the app shows, split the owner's tab, read the owner's screen and history (`capture-pane`), type into the owner's shell (`send-keys`), and turn the helper's passthrough seal back off. Hiding `TMUX` or the binary is no boundary.
- So **nothing inside the box can hold an agent that runs as `work`**: it is the owner, with root. Grants would be advice.

## Decision

### 1. Each agent runs as its own Unix user (the lane model of ADR 0006, now)

**Decided by the owner for the 27 Oct tester build (13:19, R-0231, replacing R-0228's choice):** per-agent users are required. There is no "agents run as you" build and no release-notes wording for one.

**The users: a fixed pool, made by init, never at run time.**
- **Eight slot users,** `pv-1` to `pv-8`, uid and gid 2001–2008 (inside ADR 0010's lane range, 2000–2999), homes `/home/pv-1` to `/home/pv-8` (0700, owned by the slot), login shell `/bin/bash`, no password, not in `sudoers`. Eight is the team ceiling (section 7).
- **A group `team`,** gid 2000, whose members are `work` and the eight slots (section 2).
- **init makes them,** as root, at every box start and before the helper starts: it adds any missing `/etc/passwd`, `/etc/group` and `/etc/shadow` lines, makes missing homes and fixes their owner and mode. Nothing writes those files while the box runs. Toolbox images may ship the lines already; init's step is then a check.
- **An agent gets a slot when the owner first starts it.** The map from agent id to slot is `/home/.portenv/team/slots.json` (written only by the helper, 0644, so it travels with the workspace). An agent keeps its slot, and its home, for its life. Removing an agent never deletes its home on its own: the owner chooses "Remove and delete its files" for that, and until then the slot stays taken.
- **Names:** the app and every message show the agent's name ("builder"), never `pv-3`.

**The helper's user switch.**
- The helper's API process (uid 991) already holds `cap_setuid`, `cap_setgid`, `cap_chown` and `cap_kill` (ADR 0010). It needs no new capability.
- **For each agent it starts two processes as the slot user,** in a forked child that calls `setgroups([team])`, `setgid`, `setuid`, clears the ambient capability set and sets `no_new_privs` before `exec`, so nothing an agent runs can gain a capability or use a set-uid binary (`sudo` included):
  - the agent's own tmux server, on `/run/portenv/tmux/pv-N/default`; the helper makes that directory owned by the slot, 0700;
  - one control client (ADR 0020's control mode) attached to that server, talking to the helper over pipes. It runs as the slot because tmux accepts only clients of the server's own uid.
- **Stop** kills every process of the slot's uid (`cap_kill`), after ending its tmux server.
- `work`'s tmux server and control client stay as they are, on `work`'s own 0700 socket directory.

**What this guarantees (the boundary; it is never what gets cut for time):**
- **Raw `tmux` from an agent reaches only its own server.** It can't open `work`'s socket (directory mode) or another slot's, and tmux refuses other uids anyway.
- **The only way into the owner's window is `portenv split` or `portenv tab new` (section 4), after `portenvd`'s grant check.** Those panes live on the agent's own server; the app shows them in the owner's window, labelled with the agent.
- **No agent reads, splits or types into the owner's terminals or another agent's.** There is no command for it, and no socket to do it with.

This closes every item of lane A's list:

  | Lane A's finding | With per-agent users |
  | --- | --- |
  | `new-window` makes a tab the app shows | Only in the agent's own server; the app shows it under that agent, labelled |
  | `split-window -t <owner tab>` | No socket to reach the owner's server |
  | `capture-pane` reads the owner's screen | Same |
  | `send-keys` into the owner's shell | Same; and `portenv` refuses keys to any pane the agent didn't open |
  | `allow-passthrough on` undoes the seal | Only on the agent's own server, whose control client the helper restarts sealed |

- **Separately (lane A's small fix, which goes in regardless):** each tab streams the pane recorded when it was made, never "the active pane", so a split can't swap what a tab shows.

### 2. Files, secrets and identity for per-agent users

**How agents change the project (the coordinator's question 1). Chosen: a shared group with setgid directories.**
- **Everyone works in the owner's checkout,** which is what a lead, builder and reviewer working on one tree expect.
- **The project's group becomes `team`.** At team start the helper sets the group on the tree (`cap_chown`) and the setgid bit on every directory, so new files and folders stay in `team`.
- **Group write comes from default ACLs** (`g:team:rwX`) on the tree, not from each tool's umask: a default ACL applies whatever umask an editor, a package manager or `git` uses. The agents' login environment also sets umask 002, for files made outside the project. If the filesystem refuses ACLs, the helper refuses to start the team, with a plain line; it never falls back to `chmod -R g+w`.
- **git works as a shared repository:** the helper sets `core.sharedRepository = group` in the owner's `.git/config`, so objects, refs and the index stay group-writable.
- **Never group-writable (owned by `work`, 0755 or 0644, no default ACL, checked at every team start):**
  - `.git/hooks/` and `.git/config` (a hook, `core.hooksPath`, `core.fsmonitor` or a filter would be code the owner runs);
  - the project's `.portenv/` (agent files, section 3).

  An agent's git commands that write config (`git remote add`, `branch --set-upstream-to`) fail; the agent asks the owner.
- **The owner's home:** `/home/work` becomes 0711 (agents can pass through, not list). The owner's dotfiles and logins (`~/.config`, `~/.claude`, `~/.ssh`) stay 0700. The helper refuses to start a team over a home whose modes would expose them, with a plain line.
- **The residual risk, stated plainly:** an agent can change any project file the owner later runs, such as a `Makefile`, a test or a `package.json` script. When the owner runs it, it runs as `work`, with sudo. That is the same trust as merging an agent's pull request without reading it, and the app says so the first time a team starts ("Agents can change your project's files. Check their changes before you run them."). File ownership shows who made a new file; git shows every change.

**Rejected: a bind mount of the project into each agent's home.** The uids still differ, so a plain bind mount changes no permission: the agent needs group write anyway. Making an agent's writes land as `work` needs an idmapped mount, which needs `CAP_SYS_ADMIN` in the box. The box doesn't have it, and this ADR won't add it. It would also make an agent's writes look like the owner's.

**Considered, a later option: each agent in its own clone** (`git clone --shared` into its home, committing on `agent/<id>`, the owner fetching). It keeps agents out of the owner's files entirely, but changes how a team works (no shared tree, a fetch for every hand-off). It can come later as an agent-file option (`checkout: own`) on top of this design.

**The owner's secrets (question 2):** an agent doesn't have `work`'s home, so it has none of the owner's logins: Claude Code's sign-in, git credentials, SSH keys, cloud tokens. It also has no sudo: a package it needs is a decision for the owner (`portenv ask-owner`), and the owner installs it. That's the point, and it has a cost:
- **Each agent signs in once,** in its own tab (Claude Code prints its sign-in link there). The login stays in that agent's home and travels with the workspace like everything else in `/home`.
- **Or the owner shares one login, explicitly, per agent:** an agent file may ask `share_login: [claude]`. `portenvd` asks the owner once ("Let builder use your Claude Code sign-in?"), and the helper copies only that tool's credential file into the agent's home, 0600. The prompt says plainly that a shared login can't be taken back by removing the agent: the owner signs out of that login to end it.
- **git pushes:** agents don't get the owner's git credentials. They commit in the shared checkout; a push to the remote is the owner's (or the lead's, if the owner grants `push` and shares a login for it).

**Identity (question 3):** see section 5. Every way in, local or remote, lands on the same per-agent user.

### 3. Agent files

**Where:** `<project>/.portenv/agents/<id>.md` in the repository, so a team travels with the code. The project is the workspace's project folder (`/home/work/<workspace name>`, as first run makes it). A workspace without one has no team, and the Team section says so.

**Personal overrides:** `~/Library/Application Support/Portenv/agents.local/<workspace id>/<id>.md` on the Mac, never in the repository or the box. An override may change `runner`, `avatar`, `share_login` and narrow a grant. It can't widen a grant past the repository file or the owner's limits.

**Format:** YAML frontmatter, then Markdown instructions (the agent's system prompt or `AGENTS.md`, handed to it at start as `AGENTS.md` in its home).

```yaml
---
id: builder                 # [a-z][a-z0-9-]{0,31}; the file's name must match
name: Builder               # 1–40 characters, shown in the sidebar
role: Writes the code the lead asks for   # up to 120 characters
runner: claude --permission-mode acceptEdits   # an argv, split like a shell would but run with no shell; or "remote"
via: mcp                    # only with runner: remote — ssh | mcp | web
avatar: hammer              # an SF Symbol name, or a colour from GUIDELINES §11's agent colours
share_login: []             # tools whose sign-in the owner may share (section 2); asked, never assumed
grant:
  panes: ask                # ask | allow | never — may it open panes and tabs
  max_panes: 4              # 1–8
  message: [lead, reviewer] # agent ids it may message; the owner is always allowed
  ask_owner: true           # may it ask the owner (portenv ask-owner)
---
You build what the lead asks for. …
```

**Validation, by `portenvd`, before anything starts:**
- only the keys above, with the types and ranges shown; the file at most 64 KB, its instructions at most 32 KB, UTF-8 without control or bidi characters (ADR 0020's rule) in `id`, `name` and `role`;
- a local `runner`'s first word must be on the agent user's `PATH` in the box (the helper checks; `portenvd` never runs it to find out);
- `message` names only agents in the team; `max_panes` within the owner's limit (section 7);
- each problem is one plain line the app shows by the agent's row ("builder: runner `claude` isn't installed in this workspace. Install it, then choose Start.").

**Two locks on the files:**
- **The owner approves each file's content before it runs.** A file is pinned by its SHA-256 once the owner approves it in the app. A changed file (an agent's proposal, a `git pull`, a teammate's commit) stops nothing that's running, but won't start again until the owner approves the new content, with the diff. This matters because a repository is shared: a commit from someone else could add an agent with `panes: allow` and any `runner`, which is code that runs in the owner's workspace.
- **Agents can't edit them in place.** The project's `.portenv/` is `work`'s and never group-writable (section 2), so an agent's uid can't change an agent file. It proposes a change with `portenv propose-agent-change <file>`, which lands as a decision for the owner, with the diff, and then only runs once the owner approves the new content.

**Templates:** `portenv team init` offers two, and asks the owner to approve each file it writes:
- **Starter:** three agents for Claude Code (lead, builder, reviewer).
- **Orchestrate team** (R-0232): lead, builder, reviewer and design-system keeper, the team the owner tests end to end before testers (section 11).

### 4. `portenv` inside the box

**The commands** (each runs as the calling agent's identity, section 5, through the same grant check as every other way in):

| Command | Grant | Notes |
| --- | --- | --- |
| `portenv split [--right\|--down] -- <cmd>` | `panes` | splits the agent's own current pane; counts toward `max_panes` |
| `portenv tab new --name <n> -- <cmd>` | `panes` | a new tab in the agent's own server, shown under it in the sidebar |
| `portenv focus <pane>` | own panes only | |
| `portenv close <pane>` | own panes only | |
| `portenv msg <agent> "<text>"` | `message` | at most 8 KB; to the board (section 6) |
| `portenv ask-owner "<text>"` | `ask_owner` | a decision for the owner (section 6), which the agent waits for or polls |
| `portenv inbox [--wait] [--since <cursor>]` | always, own messages only | JSON lines |
| `portenv share [<branch>]` | always | a message to the owner and the lead that its work is ready (a branch, or changes in the shared checkout) |
| `portenv propose-agent-change <file>` | always | a decision for the owner, with the diff |

- **Never:** keys into a pane the agent didn't open; anything in the owner's panes; another workspace; anything about keys, saves, Move To or the workspace's lifecycle.
- `panes: ask` makes each `split` or `tab new` a decision for the owner ("builder wants a new tab running `npm test`"); `allow` runs it at once; `never` refuses it with a plain line.
- The CLI is the same `portenv` binary, already in the image; inside a box it talks only to the helper's per-agent socket (below), never to `portenvd`'s socket on the Mac (which isn't there).

**The path:** the in-box command → the helper's socket `/run/portenv/team/<id>.sock` (0600, owned by the agent's uid, in a 0711 directory) → the helper fills in the caller from the socket (below) → over the existing agent channel to `portenvd` → the grant check → the action, carried out by the helper. The helper has no say in grants; it only names the caller.

### 5. Identity: one per agent, the same on every way in

- **The identity is ADR 0016's,** made when the owner approves the agent: for a team agent, approving its file; for a remote agent, approving its pairing code. The Unix user is that identity's body inside the box.
- **Inside the box, the caller is the socket's peer uid.** The helper reads it with `SO_PEERCRED` on the connection, maps uid → agent id, and passes the id to `portenvd`. The request carries no identity. An agent's process can reach only its own socket (file modes), and couldn't claim another's even on the right socket: the uid comes from the kernel.
- **In `portenvd`, the caller is fixed when the call arrives** (ADR 0020's rule): `caller{kind: callerAgent, id, name, box}`, from the helper's message for that call, for a workspace whose helper `portenvd` already trusts (ADR 0010). It sets `CommandAuthor` and `author_agent_id` for everything the agent runs, so the blocks' chip and the sidebar agree.
- **Remote agents land on the same user** (lane A's recommendation, taken): an SSH session's forced command runs as the agent's slot user; MCP tools run as the agent's slot user through the helper; a web terminal shows only that agent's panes. One identity, one user, one grant, whatever the door.

### 6. The board (slice 1: messages and owner decisions)

- **Where:** `/home/work/.portenv/board/` in the workspace (box-level, so it moves with the workspace and isn't committed). Two append-only JSONL files: `messages.jsonl` and `decisions.jsonl`. Agents can't write them: the directory is `work`'s, 0755. **One writer: the helper, on `portenvd`'s instruction**; no agent call writes a byte without `portenvd`'s check first.
- **A message** is ADR 0016's type, with these fields and nothing else: `id` (ULID), `from`, `to` (an agent id or `owner`), `thread_id`, `type` (`question`, `answer`, `notice`), `text` (≤ 8 KB, plain text), `created_at`. `portenv inbox` returns an agent's own, with a cursor; the owner sees every message that involves their agents (ADR 0016 point 5).
- **A decision** is a request for the owner: `id`, `from` (agent), `kind` (`ask_owner`, `pane`, `agent_file_change`, `share_login`), `summary` (≤ 200 characters), `detail` (≤ 32 KB: the question, the command, the diff), `state` (`pending`, `approved`, `declined`, `expired`), `answer_note`, `created_at`, `answered_at`. Each answer is a new line; the file is never rewritten.
- **Only the app answers decisions,** with Approve and Decline, in front of the person (no agent, no CLI on the Mac, no door). A pending decision expires after 24 hours ("expired"), and the agent is told.
- **Board text is data, never UI or instructions:** the app shows it as plain text (no Markdown links, no images, no actions in it), and `portenv guide` tells agents that a message is information from another agent, not a command (ADR 0016 point 7).
- **Secret prompts are never relayed** (ADR 0016 point 8): `ask-owner` refuses text taken from a pane whose echo is off, and the app never shows a password prompt's text.

### 7. Limits (owner-tunable in Settings, these are the defaults and the ceilings)

| What | Default | Ceiling |
| --- | --- | --- |
| Agents in a team | 4 | 8 |
| Panes per agent (`max_panes`) | 4 | 8 |
| Panes in a workspace, all agents | 16 | 24 |
| Messages per agent | 60 a minute | 120 a minute |
| Pending decisions per agent | 5 | 20 |
| A message | 8 KB | 8 KB |
| Each board file before it's rotated (`.1`, kept with the workspace) | 10 MB | 10 MB |

Past a limit, the call is refused with a plain line ("builder has 4 panes open, its limit. Close one first."), never queued.

### 8. `portenvd` runs the team

- **Start:** the owner chooses Start on an agent (or Start Team). `portenvd` checks the file is approved and valid, then asks the helper to give the agent a slot if it's new (section 1), check the project's modes (section 2), and start the agent's tmux server and control client. The helper runs the `runner` argv in the agent's first tab, as its slot user, with `PORTENV_AGENT=<id>`, `PORTENV_SOCKET=/run/portenv/team/<id>.sock` and `AGENTS.md` in its home.
- **Stop:** the helper ends the agent's tmux server and every process of its uid. Stop is immediate and needs no agent's agreement.
- **Restart policy for the tester build:** none. An agent that exits shows Stopped, with its last line; the owner starts it again.
- **States, shown in the sidebar:** `stopped`, `starting`, `running`, `idle` (ADR 0017's waiting detector: a prompt on screen and nothing happening), `needs_you` (a pending decision from it, or it's waiting at a question), `invalid` (with the line from section 3); for remote agents, `connected` and `disconnected`.
- **Moving and saving:** agent users and homes are in `/home`, so they travel with the workspace. init remakes the slot users at every box start and `slots.json` travels in `/home`, so each agent finds its own home again, and a team is stopped before Move To (it starts again, by the owner's choice, where the workspace lands).

### 9. Proto

All appended per the merge guard: new RPCs at the end of `DaemonService`, new messages at the end of `daemon.proto`, enums in `types/v1/types.proto`.

```proto
// daemon.proto, DaemonService (app ↔ portenvd)
rpc ListAgents(ListAgentsRequest) returns (ListAgentsResponse);
rpc WatchTeam(WatchTeamRequest) returns (stream WatchTeamResponse);
rpc StartAgent(StartAgentRequest) returns (StartAgentResponse);
rpc StopAgent(StopAgentRequest) returns (StopAgentResponse);
rpc AnswerDecision(AnswerDecisionRequest) returns (AnswerDecisionResponse);
rpc ApproveAgentFile(ApproveAgentFileRequest) returns (ApproveAgentFileResponse); // added to R-0226's five: the content pin of section 3

message ListAgentsRequest { string name = 1; }            // the workspace
message ListAgentsResponse { repeated AgentInfo agents = 1; }
message AgentInfo {
  string id = 1; string name = 2; string role = 3; string avatar = 4;
  bool remote = 5; string via = 6;                       // "ssh" | "mcp" | "web" when remote
  portenv.types.v1.AgentState state = 7;
  string problem = 8;                                    // plain line for invalid or a failed start
  string file_sha256 = 9; bool approved = 10;            // the pinned content, and whether this content is it
  repeated string tab_ids = 11;                          // its tabs, for the sidebar
  string last_line = 12;                                 // its last board line, plain text
  int32 pending_decisions = 13;
}
message WatchTeamRequest { string name = 1; string cursor = 2; }
message WatchTeamResponse {
  oneof event { AgentInfo agent = 1; BoardMessage message = 2; Decision decision = 3; }
  string cursor = 4;
}
message BoardMessage { string id = 1; string from = 2; string to = 3; string thread_id = 4;
  portenv.types.v1.MessageType type = 5; string text = 6; google.protobuf.Timestamp created_at = 7; }
message Decision { string id = 1; string from = 2; portenv.types.v1.DecisionKind kind = 3;
  string summary = 4; string detail = 5; portenv.types.v1.DecisionState state = 6;
  string answer_note = 7; google.protobuf.Timestamp created_at = 8; google.protobuf.Timestamp answered_at = 9; }
message StartAgentRequest { string name = 1; string agent_id = 2; }
message StartAgentResponse { AgentInfo agent = 1; }
message StopAgentRequest { string name = 1; string agent_id = 2; }
message StopAgentResponse { AgentInfo agent = 1; }
message AnswerDecisionRequest { string name = 1; string decision_id = 2; bool approve = 3; string note = 4; }
message AnswerDecisionResponse { Decision decision = 1; }
message ApproveAgentFileRequest { string name = 1; string agent_id = 2; string file_sha256 = 3; } // must match what the app showed
message ApproveAgentFileResponse { AgentInfo agent = 1; }

// types.proto
enum AgentState { AGENT_STATE_UNSPECIFIED = 0; AGENT_STATE_STOPPED = 1; AGENT_STATE_STARTING = 2;
  AGENT_STATE_RUNNING = 3; AGENT_STATE_IDLE = 4; AGENT_STATE_NEEDS_YOU = 5; AGENT_STATE_INVALID = 6;
  AGENT_STATE_CONNECTED = 7; AGENT_STATE_DISCONNECTED = 8; }
enum MessageType { MESSAGE_TYPE_UNSPECIFIED = 0; MESSAGE_TYPE_QUESTION = 1; MESSAGE_TYPE_ANSWER = 2; MESSAGE_TYPE_NOTICE = 3; }
enum DecisionKind { DECISION_KIND_UNSPECIFIED = 0; DECISION_KIND_ASK_OWNER = 1; DECISION_KIND_PANE = 2;
  DECISION_KIND_AGENT_FILE_CHANGE = 3; DECISION_KIND_SHARE_LOGIN = 4; }
enum DecisionState { DECISION_STATE_UNSPECIFIED = 0; DECISION_STATE_PENDING = 1; DECISION_STATE_APPROVED = 2;
  DECISION_STATE_DECLINED = 3; DECISION_STATE_EXPIRED = 4; }
```

```proto
// agent.proto, AgentService (portenvd ↔ helper), appended
// The helper relays in-box `portenv` calls up to portenvd, each with the caller it read from the socket.
rpc TeamCalls(stream TeamReply) returns (stream TeamCall);   // portenvd opens it; the helper sends calls, portenvd answers
rpc StartTeamAgent(StartTeamAgentRequest) returns (StartTeamAgentResponse);
rpc StopTeamAgent(StopTeamAgentRequest) returns (StopTeamAgentResponse);
rpc AppendBoard(AppendBoardRequest) returns (AppendBoardResponse);           // the board's one writer
message TeamCall { string call_id = 1; string agent_id = 2;                  // agent_id from SO_PEERCRED, never the request
  oneof op { Split split = 3; NewTab tab = 4; PaneRef focus = 5; PaneRef close = 6; Msg msg = 7;
             AskOwner ask_owner = 8; Inbox inbox = 9; Share share = 10; ProposeAgentChange propose = 11; } }
message TeamReply { string call_id = 1; bool ok = 2; string line = 3; bytes result = 4; } // line: the plain refusal
```

The doors (section 10) call the same `portenvd` functions behind `TeamCalls`; there's no second implementation of a grant check.

### 10. Remote agents (R-0227)

**In the agent file:** `runner: remote` with `via: ssh | mcp | web`. A remote agent gets the same identity (made by pairing, ADR 0016), the same per-agent user, the same sidebar row (connected or disconnected) and the same grant. Portenv doesn't start it.

**One rule for all three:** every action a remote agent takes is one of section 4's commands, made as that agent's identity, through `portenvd`'s grant check. No path skips it, and none reaches the owner's panes, the Mac, or another workspace.

**Network exposure is per workspace, opt-in, and off by default.** On the Mac it never means an inbound port (CLAUDE.md, ADR 0015).

**SSH (designed, after testers):**
- **Keys:** only the owner adds a key, while approving the agent's pairing code. The agent's private key never leaves its side. ADR 0016's short-lived certificates replace bare keys when the runner's CA is used (ADR 0007).
- **Where it lands:** on a server, the runner's sshd, with a forced command (`portenv agent-shell <id>`) that attaches to the agent's own tmux server as the agent's slot user. `restrict` is set, so there's no port forwarding, no agent forwarding, no X11 and no user rc.
- **On a Mac:** none in the tester build. There's no inbound port, so a cloud agent can't reach the Mac. A local SSH client would gain nothing over MCP.

**MCP (recommended for the tester build, in its local form):**
- **The tools map one to one onto section 4's commands:** `box.split`, `box.tab_new`, `box.focus`, `box.close`, `box.msg`, `box.ask_owner`, `box.inbox`, `box.share`, and `box.run` (run a command in the agent's own pane and return its output when it finishes, ADR 0017's done signal), plus the read-only `box.screen` and `box.events`, for its own panes only. The names live under `box.*` (ADR 0016 point 10).
- **Credentials:** a per-agent token the owner issues in the app (Team › agent › Connect…) and can revoke at once. It's stored in the Keychain on the Mac, shown once to copy into the agent's MCP settings, and kept by `portenvd` only as a hash.
- **Local, on the Mac (tester build):**
  - **How it runs:** `portenv mcp --agent <id>`, a stdio MCP server the agent's app starts (Claude Desktop, Cursor, Codex and similar). It connects to `portenvd`'s owner-only socket and presents the token. `portenvd` then fixes the caller as that agent, not the owner. There's no listener.
  - **Honest limit:** a local MCP client runs as the owner on the owner's Mac, so it could open the owner's socket without the token. Its identity is for attribution and grants by consent, as with any app the owner installs.
- **Remote (designed, after testers):** streamable HTTP with OAuth, served by the runner on the user's own server under ADR 0015 (opt-in public door, or the user's own tunnel), or later by Portenv's gateway (3.9). The OAuth consent is the pairing (ADR 0016). This is how ChatGPT's Dots gets in.

**Web terminals (designed, after testers):**
- **Where:** served on a private network only, the user's tailnet or LAN, never a public address. On a server it can be ADR 0015's door, under its conditions.
- **Who:** the owner signs in with a passkey, then hands the agent a one-time link tied to one agent and one workspace (ADR 0015's rule).
- **What it shows:** only that agent's panes, as the agent's slot user. Muse's way in.

**Recommendation: build MCP, locally, for the tester build** (R-0231 asks for MCP or SSH; MCP is the one a Mac can serve without an inbound port).
- **Why MCP:** one door serves several agents (Claude Desktop, Cursor, Codex today; ChatGPT once remote MCP exists). It needs no listener on the Mac. Its tools are section 4's commands, so it adds no new power to check.
- **SSH next, then web.**
- **The reviewer should know:** none of the three lets a *cloud* agent (Grok Bot, Dots, Muse) reach a workspace on a Mac in the tester build. Every path for them needs something reachable from the internet: the runner on the user's own server (2.7, 2.8, after testers, with 2.9 Set Up) or Portenv's gateway (Phase 3). Opening the Mac to them would break the no-inbound-port rule.
- **What testers get:** local agents can join the team through MCP. The cloud agents' door comes with servers after the tester build.
- **If time is short, remote goes first** (section 11). The per-agent boundary is never what gets cut.

### 11. Build order and dates (R-0231, R-0232)

**Week of 19 Oct, in this order** (each step after its tests, below):
1. **Mon 19 – Tue 20: users.** init's slot pool and `team` group, the helper's user switch, per-agent tmux servers and control clients, the shared-group modes and checks, lane A's tab-streams-its-own-pane fix.
2. **The command and its grants:** the per-agent sockets with `SO_PEERCRED`, `portenv` in the box (section 4), the grant checks in `portenvd`.
3. **The board:** messages and decisions (section 6), and Approve and Decline in the app.
4. **The sidebar:** the Team section (lane D's mockups), agent states, approving agent files with their diff.
5. **One remote path:** local MCP (section 10). First to drop if the week runs short.

**Checkpoint, Wed 21 evening:** per-agent users working end to end in a lane build (an agent started as its slot user, lane A's repro failing at every step as that user, a `portenv split` showing in the owner's window). If not, lane C tells the reviewer at once.

**After the build:**
- **Fri 23 evening:** release-candidate build.
- **Sat 24:** the reviewer's UI tests.
- **Sun 25 – Mon 26:** the owner runs the Orchestrate team end to end (R-0232), from a short run sheet lane C writes in the build week: install, add a project, `portenv team init` (Orchestrate team), start the agents, watch the sidebar, answer a decision, open an agent's panes, move the workspace, close and reopen the window.
- **Tue 27:** the tester build, only on the owner's word.

## Consequences

- The tester build gets a team that the grants actually hold: each agent its own user, its own tmux, no sudo, and only `portenvd`'s checked calls can touch what the owner sees.
- **Agents lose the owner's logins.** Each signs in, or the owner shares one login per agent, with a warning that sharing can't be undone by removing the agent. Testers will notice the extra sign-in; first-run copy for the Team should say why.
- **Agents share the owner's checkout.** That's what a team on one tree expects, and it means the owner runs files agents changed (section 2's residual risk, which the app states once). Own clones per agent stay a later option.
- **New code, all after approval:**
  - **The helper:** the user switch into the slot pool (init makes the users), one tmux server and control client per agent, per-agent sockets with `SO_PEERCRED`, the board writer, and the shared-group and home-mode checks.
  - **`portenvd`:** agent-file validation and the content pin, the grant checks, the board and decisions, `TeamCalls`, the RPCs above, and `portenv mcp`.
  - **The app:** the Team section (lane D's mockups), Approve and Decline, approving a file with its diff, and issuing and revoking MCP tokens.
  - **Templates and docs:** the Orchestrate team template and the owner's run sheet (section 11).
- **Tests before code (data-loss and security paths):**
  - an agent's raw `tmux` can't reach the owner's server: lane A's repro, run as a slot user, fails at every step, and so does reaching another slot's server;
  - a request naming another agent is attributed to its socket's uid;
  - `send-keys` into a pane the agent didn't open is refused;
  - a changed agent file doesn't start until approved, with the diff;
  - an agent's uid can write the project (new files stay in `team`, whatever its umask) but can't write `.git/hooks`, `.git/config` or the project's `.portenv/`, or read the owner's dotfiles;
  - the team won't start over a project or home whose modes are wrong, or a filesystem without ACLs;
  - an agent's process can't gain a capability or use `sudo` (`no_new_privs`, no ambient set);
  - a removed agent's home stays until the owner deletes it, and its slot stays taken;
  - each limit refuses with its line;
  - board text with Markdown or escape sequences renders as plain text;
  - Stop ends every process of the agent's uid;
  - a secret prompt's text never reaches the board;
  - a revoked MCP token is refused at once;
  - the team's slots, homes and the project's group and ACLs survive a save, a Move To and a restore.
- **PLAN.md** gains the team in Phase 1's tester-build scope (R-0226) when this is approved. 2.4's stand-in mode stays as designed. Lanes (4.1) now arrive early, in this team form, for team agents.
- **Open questions** for PLAN when approved:
  - whether stand-in agents (the owner's access, ADR 0006 B) still make sense once team agents exist, or become a team agent with wider grants;
  - per-agent CPU and memory limits (cgroups need the engine's delegation, which the box lacks today; see the rootless spike, R-0223);
  - whether restic keeps the project's ACLs and setgid bits on restore, or the helper reapplies them at team start (the tests above decide);
  - when to offer `checkout: own` (per-agent clones).
