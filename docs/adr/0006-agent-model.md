# 0006. Agent model: ownership, two modes, one core with thin doors

Date: 2026-10-07 · Status: accepted

## Context

Portenv's promise is "resume your work anywhere, and let your agents work in it for you". The plan already said agents get lanes and are stopped at privilege boundaries. The owner has since settled who owns what, how agents join, what is and is not policed, and in which order agents can reach a box. This records it; docs/PLAN.md (Agent access, Roadmap) follows it.

## Decision

### A. Ownership

The user owns every box. Agents are guests the user invites. Any agent that can open a terminal or use a connector can join; nothing in Portenv is specific to one agent, and Portenv ships no agent of its own. Work done by agents stays in the user's box, and history attributes every change to the agent that made it.

### B. Two modes, chosen per agent when it is connected

- **Continue my work (stand-in).** The agent acts as the user, with the user's access to that box, in the user's sessions. It can drive existing tabs, start Claude Code, Codex or anything else in the same codebase, and relay questions back to the user. The app says plainly that the agent has the user's full access to the box.
- **Lane.** The agent gets its own user (`/home/<agent>`), tmux session, git worktree on `agent/<name>`, and its own limits. For agents the user wants kept separate.

### C. No policing in stand-in mode

Stand-in mode has no approval prompts by default. What it has is for the user's benefit:

- **Attribution:** who typed what, in every session (recorded and tagged with the agent's name).
- **Save points:** Portenv makes a save point when a stand-in connects, so Revert To ▸ Last Save Point undoes an agent's work.
- **One-switch revoke:** ends access and live sessions at once.

Approvals are opt-in, per action (for example `git push`, deleting, production secrets), mainly to guard against prompt injection and misheard voice commands. Limits apply to lanes.

Enforced for every agent, stand-in included: no agent can extract encryption keys or the restic password (ADR 0005 and its conditions), and no agent can reach any box but the one it was invited to (credentials are per box).

### D. Tool-agnostic signals and the relay loop

The box agent's core is **events, screen, send and wait for input**:

- "Waiting for input": a tab is idle with a prompt on screen. "Command finished": from shell prompt markers, with the exit code. Both work the same for Claude Code, Codex or a plain y/n script. Optional adapters (for example Claude Code hooks) only make a signal more precise; no feature depends on one.
- Agent-facing commands: `portenv events --follow`, `portenv screen <tab>`, `portenv send <tab> "<text>"`.
- How an agent asks its user (voice, chat) and what it answers on its own is between the user and their agent. Portenv does not enforce it.

### E. Doors, and the order they are built

**One core, thin doors.** Every door is a different way to reach the same core, with the same login, short-lived per-box credentials, attribution and revoke. No door may bypass any of these. The HTTP API is the base the doors share; it is built as part of doors 2 to 4, not as a separate step.

First target agents:

- **Grok Bot**: terminal, through SSH and the CLI.
- **ChatGPT Dots**: through a plugin built on the remote MCP door.
- **Meta Muse**: watch; no way in yet.

Build order:

1. **SSH** on ports 22 and 443 with short-lived certificates.
2. **`portenv` CLI and a published skill.** Agents install the CLI on their own computer, sign in with a device code, and the skill teaches them events, screen and send.
3. **Webhooks.** The box's agent or runner (not a Portenv server) pushes events (waiting for input, command finished) to a URL the user registers for their agent, signed, so trigger-driven agents wake without polling.
4. **MCP server** over the same core. Stdio first (it runs on the agent's computer through the CLI) for agents that run MCP servers locally, before the gateway exists; remote with OAuth on the gateway once Phase 3 lands. The remote server is what becomes the ChatGPT plugin.
5. **Web terminal on portenv.com**, for browser-only agents and for the user on any computer or phone. Passkeys required.
6. **Later, only if usage shows the need:** a GitHub bridge (the box pushes a branch, Codex works, the box pulls), chat or email bridges (every reply carries a signed, single-use token), A2A.

### Roadmap placement

- **2.6 append-only storage comes first:** before any outside agent gets stand-in access, the storage credential inside a box becomes append-only (it cannot delete or overwrite a save), and forget and prune use a host-only credential.
- **2.7, end of Phase 2 (no control plane, no lanes):** stand-in access over doors 1 and 2 to a box on the user's own server. Without the control plane, the runner on that server is the certificate authority for its boxes: the agent runs `portenv connect` (named `portenv login` until ADR 0016), the user approves the code in the app (which reaches the runner over SSH), and the runner issues a short-lived SSH certificate scoped to that box and mode. The core (events, screen, send, wait for input), attribution, recording, the connect save point, Take Over and Revoke ship here. Acceptance: a terminal agent (Grok Bot, or a test agent) relays a tool's question to the user and answers it.
- **2.8 webhooks** and **2.9 stdio MCP** (`portenv mcp` on the agent's computer, over the CLI's connection): Phase 2, after 2.7.
- **3.8 SSH door through the gateway**: certificates from the Portenv CA, routing by box name, ports 22 and 443 and `portenv ssh` over HTTPS. Depends on 3.1 (accounts), 3.2 (device enrollment and revocation) and 3.4 (tunnels and routing).
- **3.9 remote MCP door with OAuth**, then the ChatGPT plugin listing. Depends on 3.1 (OAuth identities), 3.4 (gateway) and the 2.7 core. It does not depend on lanes, the approval helper or the vault.
- **3.10 web terminal on portenv.com** with passkeys. Depends on 3.1 and 3.4.
- **Phase 4** keeps lanes (4.1), the opt-in approval helper (4.2), the vault (4.3) and full recording and command audit (4.4). Its doors milestone (4.5) becomes "remaining doors": the later list above, only if usage shows the need.

## Consequences

- The open question "sudo for stand-in agents before approvals exist" is answered: a stand-in has the user's full access to the box, sudo included, with no approval by default. The repository password stays out of reach anyway (ADR 0005).
- Guest lanes move out of 2.7 into Phase 4 (4.1); 2.7 is stand-in only.
- Device-code login and short-lived credentials arrive in Phase 2, issued by the runner, before the Portenv CA exists. When Phase 3 lands, the gateway CA takes over and runner-issued certificates end.
- A stand-in's credential is usable by whatever runs on the agent's computer while it is valid. Short lifetimes, per-box scope and revoke bound the damage; the app says so when connecting.
