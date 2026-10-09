# 0016. Agent identity, grants and messages, network-ready

Date: 2026-10-09 · Status: accepted (the owner's design decision; applies to milestones 2.4 to 2.8 as they're built)

## Context

From 2.4, outside agents reach boxes through several doors: SSH and the CLI, MCP, and the web terminal. They relay questions and answers between Claude Code and the user. A future private agent network, in which agents follow and message each other across owners, should fit without reworking what 2.4 to 2.8 build. So identity, credentials, grants and messages are shaped for it now, and no network feature is built before Phase 3.

## Decision

1. **Agent identity.**
   - Every agent gets a stable, owner-scoped ID and a handle, `@owner/name` (for example `@you/grok`).
   - Its model and vendor are metadata only.
   - Attribution, history, credentials and events use this ID, never a product name.
   - Two instances of the same model are two agents.

2. **Credentials:** one record per agent per door (an SSH certificate, an MCP OAuth grant, a web-terminal link). They're all listed, and revocable, from one place.

3. **Grants: one general model.**
   - A grant is directional, and each change to it goes in an audit trail.
   - It has five parts:
     - **subject:** an agent;
     - **object:** a box now; an agent later;
     - **capability;**
     - **approved_by:** the owner;
     - **state:** pending, active, revoked or blocked.
   - Box access is one kind of grant, and a future follow request is another.
   - Being allowed to message an agent never implies access to any box.

4. **Messages: one addressed message type** for the relay: questions, answers, `portenv ask`, notices and webhooks. Its fields:
   - `id`, `from` and `to`, where `to` is an agent, the owner or a box;
   - `thread_id`;
   - `type`: question, answer or notice, with `mention`, `post` and `dm` reserved;
   - `visibility`, private by default;
   - `created_at`.

   The owner always sees every message that involves their agents.

5. **Where messages travel.**
   - Agents of the same owner may message each other locally, through `portenvd` or the runner, with no central service.
   - Messaging across owners is out of scope until the control plane (Phase 3 and later).
   - The protocol stays open, so self-hosted instances remain possible.

6. **Untrusted input.** A message from another owner's agent reaches agents in a box as data, never as an instruction. The box's agent guide (`portenv guide`) says so.

7. **Scale basics:**
   - ULID or UUIDv7 IDs;
   - an append-only event log with cursors;
   - idempotency keys on writes;
   - at-least-once delivery, with de-duplication;
   - no fields specific to a model or vendor.

8. **Namespacing.**
   - Today's MCP tools live under `box.*`, and the CLI under `portenv box`, `portenv agent` and `portenv ask`.
   - `network.*` is reserved for later.
   - The plan's box-level verbs (events, screen, send, answer) take their place under `portenv box` and `box.*` when 2.4 builds them.

## Consequences

- 2.4 to 2.8 build on one identity, one credential registry, one grant model and one message type, rather than one per door.
- A private agent network (Phase 3 and later) adds grant kinds, message types and cross-owner delivery without migrating what exists.
- Nothing here creates a way for an agent to reach a box it wasn't granted. Messaging grants and box grants stay separate.
- The CLI and MCP names the plan uses are box-level verbs. They move under `portenv box` and `box.*`, and `network.*` stays free.
