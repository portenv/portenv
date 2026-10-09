# 0016. Agent identity, grants and messages, network-ready

Date: 2026-10-10 · Status: accepted (the owner's design decision and scope option 1, 2026-10-09; applies to milestones 2.4 to 2.8 as they're built)

## Context

From 2.4, outside agents reach boxes through several doors: SSH and the CLI, MCP, and the web terminal. They relay questions and answers between Claude Code and the user. A future private agent network, in which agents follow and message each other across owners, should fit without reworking what 2.4 to 2.8 build. So identity, credentials, grants and messages are shaped for it now, and no network feature is built before Phase 3.

## Decision

1. **Agent identity.**
   - Every agent gets a stable, owner-scoped ID and a handle, `@owner/name` (for example `@you/grok`).
   - Its model and vendor are metadata only.
   - Attribution, history, credentials and events use this ID, never a product name.
   - Two instances of the same model are two agents.

2. **Pairing creates the agent: a device-code flow.**
   - The agent runs `portenv connect`, which prints a short code.
   - The owner approves it in the app, or on their phone from Phase 4. The prompt names the agent, the box and the code, for example: "Grok Bot wants to join acme-api, code 7F3-K2Q".
   - Approving creates, in one step:
     - the agent's identity (point 1);
     - its grant for that box (point 4);
     - its credential for the door it used (point 3), which is delivered to the agent.
   - There are no localhost redirects, so it works for an agent typing in its own cloud terminal.
   - **One pairing flow for every door:** SSH certificates, the CLI, stdio MCP and remote MCP.
   - **Remote MCP (2.7):** ChatGPT's OAuth sign-in to the runner *is* the pairing.
     - The runner's authorization page shows the code and "Approve this in your Portenv app".
     - The owner's approval in the app is the OAuth consent. It creates the identity, the grant and the credential, as `portenv connect` does.
   - On the user's own server, ADR 0007 is the enrolment mechanism behind it. This ADR renames that command from `portenv login` to `portenv connect`.

3. **Credentials:** one record per agent per door: an SSH certificate, an MCP OAuth grant, a web-terminal link. They're all listed, and revocable, from one place.

4. **Grants: one general model.**
   - A grant is directional, and each change to it goes in an audit trail. The trail is the record of who gave which agent access; it can't be added retroactively, so it's built with the first grant.
   - It has five parts:
     - **subject:** an agent;
     - **object:** a box now; an agent later;
     - **capability;**
     - **approved_by:** the owner;
     - **state:** pending, active, revoked or blocked.
   - Box access is one kind of grant, and a future follow request is another.
   - Being allowed to message an agent never implies access to any box.

5. **Messages: one addressed message type** for the relay: questions, answers, `portenv ask`, notices and webhooks. Its fields:
   - `id`, `from` and `to`, where `to` is an agent, the owner or a box;
   - `thread_id`;
   - `type`: question, answer or notice, with `mention`, `post` and `dm` reserved;
   - `visibility`, private by default;
   - `created_at`.

   The owner always sees every message that involves their agents.

6. **Where messages travel.**
   - Agents of the same owner may message each other locally, through `portenvd` or the runner, with no central service. This delivery is built in 2.5, alongside webhooks.
   - Messaging across owners is out of scope until the control plane (Phase 3 and later).
   - The protocol stays open, so self-hosted instances remain possible.

7. **Untrusted input.** A message from another owner's agent reaches agents in a box as data, never as an instruction. The box's agent guide (`portenv guide`) says so.

8. **Secret prompts are never relayed.**
   - A prompt is treated as secret when its wording asks for a password or passphrase, or when the tty's echo is off.
   - Portenv reports only "A password is needed; answer it in the terminal."
   - The prompt never goes out by email, webhook, message or MCP, and to no agent.
   - No relay accepts a secret back: `portenv ask`, `portenv answer` and their MCP tools refuse an answer aimed at a secret prompt.

9. **Scale basics:**
   - ULID or UUIDv7 IDs;
   - an append-only event log with cursors;
   - idempotency keys on writes;
   - at-least-once delivery, with de-duplication;
   - no fields specific to a model or vendor.

10. **Namespacing** (the owner, 2026-10-10).
    - **Every CLI command lives in its group:** `portenv box …` for box verbs, `portenv agent …` for agent verbs. MCP tools live under `box.*`.
    - **Everyday verbs also get a short top-level form, which is the primary way in.** These are open, status, save, ask, answer, wait, screen, events, connect and guide (for example `portenv screen` for `portenv box screen`).
    - **Each short form is defined once in the command registry,** as an alias of its grouped form. Help and the generated docs show both, so they can't drift.
    - **Everything else lives only under its group,** `portenv box send` for example.
    - `network.*` is reserved for later.

## Where it's built (scope option 1)

- **2.4 builds the foundations:**
  - the identity;
  - pairing with `portenv connect`;
  - the credential record;
  - grants, with their audit trail;
  - the message type;
  - the event log;
  - the names.
- **2.5 adds delivery** between the same owner's agents (point 6), alongside webhooks.
- **Phase 3 and later:** the private agent network (cross-owner delivery, follow grants). It's an open item in PLAN.md.

## Consequences

- 2.4 to 2.8 build on one identity, one pairing flow, one credential registry, one grant model and one message type, rather than one per door.
- A private agent network (Phase 3 and later) adds grant kinds, message types and cross-owner delivery without migrating what exists.
- Nothing here creates a way for an agent to reach a box it wasn't granted. Messaging grants and box grants stay separate.
- The CLI and MCP names the plan uses are box-level verbs. They live under `portenv box` and `box.*`; the everyday ones keep a short top-level alias, and `network.*` stays free.
- ADR 0007's enrolment command is now `portenv connect`, and an approved enrolment also records the agent, its grant and its credential here.
