# Portenv: Build Plan for Claude Code

Oct 7, 2026

## How to use this plan

This file (`docs/PLAN.md` in the repository) is the master copy of the plan. Change it in the same commit as the code that motivates the change.

Build Portenv phase by phase, starting with Phase 0, and never start a phase before the previous one passes its acceptance checklist. This document is the source of truth; the decision log is settled unless the owner reopens it.

Ground rules for Claude Code:

1. Read the whole plan before writing code. When the plan and your instinct disagree, ask before deviating.
2. Respect the box driver boundary: only driver packages may talk to Docker, containerd or Apple Containerization. From Phase 1 on, never use `docker exec` as the access path; go through the box agent.
3. Never weaken a security default to make something work: no plaintext keys on disk, no public ports, no disabled encryption. Stop and ask instead.
4. Save, resume, lease and key code are data-loss paths. Write their tests first, and keep the invariants in the save/resume section true at every commit.
5. Work in small, reviewable changes, one milestone at a time. End each milestone with a short demo note and the checklist ticked.
6. Record every unknown you hit as a line under Open questions rather than guessing.
7. Ask before anything that touches real production credentials, deletes snapshots or repositories, or publishes anything.

Start with the repository layout in the tech stack section, then Phase 0, milestone 0.1.

## What Portenv is

Portenv (short for portable environment) is a native Mac app that gives each project an encrypted workspace, a box, that you can save, move between your Mac, your own servers and Portenv Cloud, and open safely to AI agents.

**Goals**

- Work continues anywhere: close the laptop, resume on a server or another Mac with the same files, history and tools.
- The home directory is the unit of state. It is saved automatically, encrypted, incrementally, with full history.
- Any agent (Claude Code, a research agent, a test agent, a custom bot) can be let into a box with scoped, revocable, audited access.
- Nobody operates Portenv from a terminal. The terminal inside the app is for the user's work.

**Design principles**

- Apple design: the terminal is the window, chrome recedes, automation replaces buttons, undo replaces confirmation dialogs.
- Runtime-agnostic: the same box runs on Docker, Apple Containerization or a microVM behind one driver interface.
- Secure by default: encryption with user-held keys, outbound-only connectivity, least privilege for agents.
- Agent-agnostic: open doors (SSH, API, MCP, events) and one published skill document, never per-agent integrations.

**Non-goals for v1**

- Not an IDE. Editors attach to boxes separately.
- No live migration of running processes. Files move; processes restart.
- No Windows or Linux desktop app. Those come later as separate native apps sharing the core.
- Not a CI system or a general VM manager.

**Product layers**

| Layer | What it is |
| --- | --- |
| 1. Box and app | The Mac app, the box format, local engine, save and resume |
| 2. Control plane | Accounts, devices, gateway, leases, agent door, approvals, audit; works with any compute |
| 3. Portenv Cloud | Managed compute and storage, always on, elastic box clones |

Layer 1 and the open protocols live in this repository. The control plane and gateway live in the private `portenv/cloud` repository.

## Decisions already made

These were settled in planning on 7 October 2026. Reopen one only with the owner.

| Area | Decision | Why |
| --- | --- | --- |
| Name | Portenv, written as one word | Short for portable environment; Devbox is taken |
| Platform | Native macOS app in SwiftUI, Apple design language | Desktop-first product; best macOS integration |
| User interface | No terminal steps to operate Portenv; autosave instead of save buttons; box actions in the title menu | Apple design philosophy |
| Box format | OCI image (Dockerfile) for the toolbox plus a portable home | Every runtime can run OCI images |
| Home | `/home/work` for the main user `work`; projects directly in home; agent lanes at `/home/<agent>` | Fixed paths keep tool history valid across machines |
| Saved unit | All of `/home`, minus excludes | Agent lanes travel with the box |
| Runtime | Box driver interface; Docker first, Apple Containerization as the native Mac engine, microVMs (Firecracker or Kata) on Portenv Cloud | Docker ships fastest; VMs isolate tenants |
| Access path | Through an in-box agent, never `docker exec` (from Phase 1) | Lets a box move from container to VM unchanged |
| Snapshots | restic repositories, encrypted, incremental, one repository per box | Proven format, multiple keys per repository |
| Leases | One machine holds a box open at a time; control plane is authoritative, snapshot tags are the offline fallback | Prevents silent overwrites |
| Encryption | Snapshots encrypted client-side; working copies on encrypted disks; per-device keys; recovery key | Homes hold secrets |
| Networking | Outbound tunnels from box agents and runners to a Portenv gateway; no inbound ports; Tailscale not used | Product-owned, multi-tenant, works behind NAT |
| Agents | Open doors (SSH incl. port 443, CLI, HTTP API, MCP, events) plus a published skill; lanes per agent; approvals at privilege boundaries | Any agent with a terminal can work safely |
| Finder | File Provider extension shows each box in Documents › Portenv › \<box> | Native, works when the box runs remotely |
| Developer servers | An open-source runner, installed by an in-app wizard | Full control for developers, same features |
| Ownership | The user owns the workspace; agents are guests. Any agent from any vendor can be connected and disconnected without moving work. Work done by agents stays in the user's box, and history attributes every change to the agent that made it. Portenv ships no agent of its own | Users keep their work and choose their agents freely |
| Repositories and license | `portenv/portenv` is public under Apache-2.0 (apps, core, shims, images, proto, docs); the control plane and gateway live in the private `portenv/cloud` repository (decided 7 October 2026) | Box agent, runner and protocols must be auditable; the hosted service is not |

## Architecture

Every machine that runs boxes (the Mac, a developer server, a cloud host) runs the same daemon logic behind a box driver, and every box contains a small agent that dials out to the Portenv gateway.

```text
 Agents and people (research-agent, test-agent, you)
              | SSH 22/443, API, MCP
              v
 +-- Portenv cloud ----------+
 |  Gateway  <------------------------------+   (outbound tunnels only;
 |     |                     |              |    nothing listens inbound)
 |     v                     |              |
 |  Control plane            |              |
 |  (keys, leases, agents)   |              |
 +---------------------------+              |
        ^                                   |
        | outbound tunnel                   | outbound tunnel
 +-- Your Mac --------------+    +-- Server or cloud host --+
 | Portenv.app (SwiftUI)    |    | Runner (same core as     |
 |   v                      |    |   portenvd)              |
 | portenvd (autosave,      |    |   v                      |
 |   restic, relays)        |    | Box (portenv-agent,      |
 |   v                      |    |   /home)                 |
 | Engine driver            |    +------------+-------------+
 |   (Containerization or   |                 |
 |    Docker)               |                 |
 |   v                      |                 |
 | Box (portenv-agent,      |                 |
 |   /home)                 |                 |
 +------------+-------------+                 |
              | encrypted saves               | encrypted saves
              v                               v
        Snapshot storage (encrypted, anywhere: your bucket, your server, Portenv)
```

On the Mac, read top to bottom; on a server the runner takes the place of `portenvd`, and the gateway is the only public entry point.

| Component | Runs on | Language | Responsibility |
| --- | --- | --- | --- |
| Portenv.app | Mac | Swift, SwiftUI | Windows, terminal rendering, title menu, first run, settings, notifications |
| File Provider extension | Mac | Swift | Shows box homes in Finder, on-demand file transfer |
| `portenvd` | Mac, as a login item | Go | Owns boxes on this Mac: driver calls, autosave, restic, leases, port relays, gateway connection |
| Containerization shim | Mac | Swift | Implements the box driver on Apple Containerization, served to `portenvd` over a local socket |
| Runner | Developer servers, cloud hosts | Go (same code as `portenvd`, server build) | Boxes on that machine, registered with the control plane |
| Box agent (`portenv-agent`) | Inside every box | Go, static binary | Terminals (PTY and tmux), lanes, port discovery, command audit, outbound tunnel |
| Toolbox images | Registry | Dockerfile | OS, toolchain, Claude Code, tmux, the box agent |
| Control plane | Portenv cloud | Go + PostgreSQL | Accounts, devices, keys (wrapped only), leases, agents, policies, audit, push |
| Gateway | Portenv cloud | Go | Public front door: SSH on 22 and 443, WebSocket tunnels, HTTP API, MCP server, events |
| `portenv` CLI | Mac, servers, agents' computers | Go | Thin client of the same APIs, for agents and power users |
| iPhone companion | iPhone | Swift | Approvals and status (Phase 4) |

The app never talks to an engine directly. It talks to `portenvd` over a local gRPC socket; `portenvd` talks to drivers; drivers talk to engines.

## The box

A box is a toolbox image plus a home. The image is rebuilt or pulled on each machine and never saved; `/home` is the only state that travels.

**Toolbox image (default: `portenv/toolbox-node`)**

- Base: Ubuntu 24.04, multi-arch (arm64 and amd64).
- Toolchain: Node 22, PostgreSQL client, git, tmux, ripgrep, jq, build-essential, Python 3.
- Claude Code from Anthropic's signed apt repository (stable channel); updated by rebuilding the image, never into home.
- `portenv-agent` as the container's init process (it reaps children and supervises the agent's services).
- Images are versioned (`toolbox-node:v12`) and published to a registry by CI. A machine without network access to the registry builds from the Dockerfile.
- VM-ready: nothing in the image may assume Docker-only features.

**Home layout**

```text
/home/work/                  main user (uid 1000), the person
  <project>/                 e.g. /home/work/acme-api
  .claude/                   Claude Code login, settings, conversation history
  .local/                    npm -g prefix, pip --user, user tools
  .config/                   app configs (no secrets: see vault)
  .portenv/
    apt-packages.txt         system packages reinstalled on every start
    excludes                 restic exclude patterns, user-editable
/home/<agent>/               one lane per connected agent (uid 2000+)
  <project>/                 a git worktree on branch agent/<agent>
```

**Users and privileges**

- `work` has passwordless sudo, mediated by the approval helper once agents exist (see agent access).
- Agent users have no sudo by default and cannot read other homes (`chmod 700` on every home).
- Users and groups are recreated from a manifest in `/home/work/.portenv/` at every start, because `/etc` does not travel. Until milestone 4.1 only `work` exists, and the image creates it.
- A new box's home is created from the image's skeleton only when the caller says the box is new (`PORTENV_INIT_HOME=1`, resume rule 1); a missing or empty home otherwise fails the start (ADR 0003).

**Packages and excludes**

- `apt-packages.txt` is replayed at start: missing packages are installed before the box reports ready.
- Default excludes: `~/.cache`, `~/.npm/_cacache`, trash folders. `node_modules` is excluded automatically when a box moves between CPU architectures.
- Playwright and other browser downloads go to `~/.local/share/ms-playwright` (saved), not `~/.cache`.

**Start-time settings (Box settings window)**

Shared memory (default 1 GB, for browsers), CPU and memory limits, Docker inside the box (off), GPU (off). Changing one restarts the box after a save.

A box never starts with `CAP_SYS_PTRACE`, `CAP_SYS_ADMIN`, `CAP_SYS_MODULE`, `CAP_SYS_RAWIO`, `CAP_PERFMON` or `CAP_BPF`, and is never privileged: these would let root in the box read the repository password while restic runs (ADR 0005, Conditions). The box agent refuses to start otherwise. Docker inside the box must therefore not grant them, or the setting must warn plainly that it weakens this protection; how to provide it is a Phase 1 design question.

## Save, resume and leases

Each box has its own restic repository; saves happen automatically, and a lease guarantees only one machine has a box open at a time.

**Repository and snapshot conventions**

- One repository per box, at `<storage>/boxes/<box-id>`. Snapshot host is always `portenv`, path is always `/home`, so change detection works across machines.
- Tags: `machine:<machine-id>`; one of `release` (closed), `point` (save point, box still open) or `orphaned` (unsaved work kept during a conflict); `active:<machine-id>` on the newest snapshot while a machine holds the lease (offline fallback only, see below).
- The box's current save is the newest snapshot that is not `orphaned`. An orphan is history: it is never restored by default and never carries the lease, even though it is newer than the save restored over it.
- `restic tag` rewrites a snapshot and changes its ID: always re-read the newest snapshot after tagging. Compare homes by the snapshot's tree hash, which tagging does not change (ADR 0004).
- Backups use `--ignore-inode --ignore-ctime --exclude-caches` and the box's `excludes` file.
- Restores into an existing home use restic 0.17 or later with `--overwrite if-changed --delete`, so only changed files download.

**Per-machine state (never uploaded)**

`portenvd` keeps, per box: the snapshot ID the local home was last synced to, a dirty flag (opened since that sync) and the restic cache.

**When saves happen**

- Every 5 minutes while there are changes, and when the Mac sleeps or the lid closes (a power assertion holds sleep until the upload finishes, up to 60 seconds).
- Before Move To, before closing the window, before an agent's approved `sudo`, and on ⌘S (Make Save Point).
- The box agent tracks changed paths with inotify, so the toolbar can show unsaved state without scanning.
- An optional `~/.portenv/hooks/pre-save` runs first, for example to dump a database running in the box.

**Resume rules (in order)**

1. Nothing saved yet: create a fresh home from the image's skeleton.
2. Another machine holds the lease: show the lease sheet (Take over, Connect to it there, Peek read-only).
3. Local home equals the newest snapshot: start immediately, no download.
4. Local home has unsaved changes and the newest snapshot is newer: first save local as `orphaned`, then restore the newest. Tell the user both versions are in history.
5. Otherwise: incremental restore of the newest snapshot, then start.

**Invariants (tests must enforce these)**

- Never save an empty home.
- Never restore over unsaved changes without first saving them as a snapshot.
- Never let two machines hold a lease unless the user explicitly took over.
- A save is complete only when restic reports a snapshot ID and the per-machine state is updated.
- A save over a lease that changed since this machine took it requires confirmation, and the other version stays in history.

**Leases**

- From Phase 3, the control plane holds leases (box, machine, since, last heartbeat) and is authoritative.
- Before Phase 3, and whenever the control plane is unreachable, the `active:` tag on the newest snapshot is the lease.
- A lease with no heartbeat for 10 minutes is shown as stale, not released: the user decides.

**Retention**

Keep the last 20 saves, hourly for 24 hours, daily for 14 days, weekly for 8 weeks, monthly for 12 months, and every `orphaned` snapshot. Prune weekly, in the background.

## Encryption and key management

Data is encrypted everywhere it rests, keys are per device and revocable, and in the default mode Portenv's servers never hold a usable key.

| Where the data is | Protection |
| --- | --- |
| Snapshots in any storage | restic encryption (AES-256 with authentication) before upload |
| Working copy on a Mac | FileVault; the app checks it is on and warns if not |
| Working copy on a server | Box homes on a LUKS-encrypted volume, unlocked by the runner when a box starts |
| Working copy on Portenv Cloud | One encrypted volume per box with its own data key |
| Running box | Readable in memory by the host: the stated runtime trust boundary |
| Secrets (e.g. production database credentials) | A vault with per-secret grants, encrypted on top of the above |

**Keys**

- Device key: generated at first run, stored in the macOS Keychain (Secure Enclave-backed where possible), never exported.
- Per-box repository keys: restic supports several keys per repository. Each authorized device or runner gets its own key, stored in that device's protected store (Keychain; a root-only file or TPM-sealed secret on servers).
- Recovery key: one more repository key per box, derived from a recovery phrase shown once at setup. Offered as Save to Passwords, Print or Copy.
- The control plane stores only wrapped keys (encrypted to a device's public key) and relays enrollment requests.

**Modes (chosen at setup, changeable in Settings; before Phase 3 only Only you exists)**

- Only you (end-to-end, default): no escrow. Losing every device and the recovery key loses the data; setup says so plainly.
- You, with Portenv's help (recoverable): an escrowed key enables account recovery. Portenv could technically decrypt. Enterprise customers may bring their own KMS.

**Flows**

1. Add a device or server: the new machine requests access; an existing device approves; it adds a repository key for the new machine.
2. Revoke a device: remove its repository keys and end its sessions. Data is not re-encrypted.
3. Suspected compromise: Re-encrypt box creates a new repository, copies all snapshots (`restic copy`), then deletes the old one. Needed because restic cannot rotate a repository's master key.
4. Start a box on Portenv Cloud in end-to-end mode while the user is away: the request goes to the user's devices; approving releases a key for that session only.
5. Recovery: the recovery phrase unlocks every box and enrolls a new device.

**Phase 0 minimum**

Repository password in the Keychain on the Mac, a root-only file on the server, a LUKS volume for box homes on the server, and a printed recovery key.

## Networking and access

Nothing Portenv runs accepts inbound connections: box agents and runners dial out to the gateway, and everything else is routed through those tunnels.

**Phase 0 (single user, no control plane)**

- The Mac reaches the server over the user's own SSH. An `ssh portenv` host entry lands directly in the box's tmux session.
- This is the only phase where `docker exec` is allowed as an access path.

**Phase 3 onward**

- The box agent and the runner each hold one multiplexed tunnel (WebSocket over TLS on port 443, with yamux streams) to the gateway, and reconnect with backoff.
- The gateway routes by box, not by machine: `ssh <box>@gw.portenv.com` reaches the box wherever it runs today.
- SSH is accepted on ports 22 and 443. For locked-down agent sandboxes, `portenv ssh <box>` tunnels SSH over HTTPS.
- Authentication uses short-lived SSH certificates signed by the Portenv CA (hours, not months) for people and agents. Access decisions are made by identity, never by IP address.
- Revoking a device, agent or certificate closes its live sessions immediately.

**Port relay**

- The box agent reports listening sockets every 2 seconds. New ones appear as a quiet toolbar item and a one-time notification.
- Open creates a local listener on the Mac (`localhost:<port>`, or the next free port, shown as a mapping) and relays each connection through the agent from inside the box, so servers bound to the box's `localhost` work.
- Non-HTTP ports show Copy address instead of Open.
- When the box runs remotely, relays go through the gateway tunnel. Nothing is exposed publicly.
- From inside a local box, the Mac is reachable at `host.portenv.internal`.

**Considered and rejected**

Tailscale and similar mesh VPNs: they require each user's own account and cannot carry Portenv's multi-tenant identities and policies. WireGuard libraries may still be embedded later behind Portenv's own coordination for direct paths.

## Agent access

Any agent that can open a terminal can work in a box, in one of two modes chosen per agent in Connect an agent. A guest works freely in its own sandboxed lane and is stopped only at privilege boundaries, where the owner approves on the Mac or iPhone.

**Assume agents type anything.** Most agents hold a persistent SSH session from their own computer and send keystrokes. Control therefore lives in the box (operating system boundaries), not in parsing commands.

**Modes**

| Mode | The agent | Access |
| --- | --- | --- |
| Continue my work (stand-in) | Logs in with its own key but acts as the user `work`, lands in the user's tmux session, can read and type into any of the user's tabs (including a running Claude Code session) and run commands in the user's project folders | The user's full access. The app says so plainly when the agent is connected |
| Separate lane (guest) | Has its own Linux user, tmux session and git worktree on branch `agent/<name>` | No access to the user's files or sessions |

In both modes every session is recorded and tagged with the agent's name, there is one writer per terminal with explicit Take Over, and Revoke ends access and live sessions immediately.

**Lanes (guest mode)**

- One Linux user per agent (`/home/<agent>`), its own tmux session, and its own git worktree on branch `agent/<agent>`. A read-only view of the main checkout is mounted at `/home/<agent>/main`.
- An agent's SSH session lands in its tmux session, so dropped connections resume. One writer per terminal; others attach read-only; Take Over is explicit.
- Per-lane limits: CPU, memory, process count, network egress rules (production hosts blocked unless granted).
- Agent keystrokes count as activity, so idle sleep does not cut off a working agent. A maximum session length still applies.

**Permission levels** (guest mode; a stand-in has the user's full access)

| Level | Allows | Default |
| --- | --- | --- |
| Observe | Status, events, read terminal output | Ask at connect |
| Read files | Read the lane and the read-only main checkout | Ask at connect |
| Run commands | Shell in the lane | Ask at connect |
| Drive Claude | Run Claude Code in the lane (`claude -p`, `--resume`) | Off |
| Lifecycle | Start, wake, save, move the box | Off |
| Admin | Ports, settings, other agents | Never for agents |

Each privileged action is set to auto, ask or never.

**Approvals at privilege boundaries**

- `sudo`, `portenv secret get <name>`, protected scripts (owner-marked, e.g. `deploy-prod.sh`) and `git push` to protected branches pause in the program the agent ran and wait for an approval.
- Approvals arrive as actionable notifications on the Mac and the iPhone companion. Unanswered requests wait (default 30 minutes), then fail with a clear message; nothing else the agent does is blocked.
- A global Pause all agents switch suspends every lane.

**Secrets**

Secrets live in the box's vault, not in readable files. The main user's sessions get them by default; agent lanes only with a grant. Granted secrets are injected per command, never written to disk.

**Audit**

- The gateway records every session's terminal stream (replayable).
- The box agent records every executed program per user via auditd or eBPF, so what ran is known even inside interactive shells.
- Every action carries the agent's identity in the audit log, which travels with the box.

**Doors (all through the gateway, one identity and policy model)**

1. SSH on 22 and 443, plus `portenv ssh` over HTTPS for sandboxes that only allow web traffic.
2. HTTP API with an OpenAPI spec: status, run command, read output, files, lifecycle.
3. Remote MCP server with OAuth: `box_status`, `run_command`, `read_terminal`, `start_box` and similar tools.
4. Event stream and webhooks: box started, command finished, port opened, approval needed, save done.

**Connecting an agent**

1. Agents › Connect an agent: name, boxes, permissions, expiry.
2. The agent runs `portenv login`, which prints a device code; the owner approves it on the Mac or iPhone.
3. Portenv shows a ready-to-paste snippet: the published skill document (how to use a Portenv box) plus connection details.
4. Revoke ends access and live sessions immediately.

**Claude Code inside boxes**

Agents driving Claude Code headlessly use `claude -p` with `--resume`, `--output-format json` and `--allowedTools`. Boxes are logged in with the owner's account; heavy or multi-user automation should use a Console API key instead. Check Anthropic's terms for automated use before shipping this feature.

## Developer-owned servers

A developer adds their own server from the app in a few minutes; an open-source runner then gives it every Portenv feature while the developer keeps full control of the machine.

**Add a Server wizard (no terminal steps)**

1. Where is it: name, address, SSH user. Pick an existing key, or let Portenv generate a dedicated key and show the public key to paste into the provider's panel. Alternative path: Copy install command, a one-liner for the provider's web console or the server's user-data field.
2. Preflight checklist, each failure with a plain fix: supported OS (Ubuntu 22.04 and 24.04, Debian 12), CPU architecture, memory and disk, sudo, outbound access to Portenv, the registry and the snapshot storage, clock sync, KVM availability (recorded for later microVM support).
3. Install: Docker Engine from Docker's official repository if missing, the runner as a systemd service under its own user, an encrypted volume for box homes. Everything Portenv creates is namespaced (`portenv-*`) and coexists with the developer's own containers. Optional hardening (disable password logins, firewall) is offered, never forced.
4. Register: the runner gets its own identity, scoped to that server.
5. Toolbox and test: pull the toolbox image (or build it), start a throwaway box, check a terminal and a port relay, remove it.
6. Ready: the server appears in the sidebar under Machines, with Move a box here.

**Operating it**

- Health in the Machines view: online state, CPU, memory, disk, boxes, toolbox and runner versions, drift warnings.
- Runner updates are automatic by default; developers can pin a version.
- Remove server: save and release every box on it, uninstall the runner, revoke its identity. Docker stays unless the developer chooses otherwise.
- If Portenv's cloud is down, boxes keep running and the app falls back to direct SSH.

**Trust boundary**

- Snapshot storage is the developer's choice (their bucket, their server, or Portenv storage).
- The runner receives a box key only when it starts that box, and keeps it in a root-only store (TPM-sealed where available).
- Control-plane commands are limited to box operations. An optional local-approval mode requires the owner's confirmation before anything starts.
- The runner, box agent and box format are open source so developers can audit them.

**Phasing**

Phase 2 installs the runner over plain SSH and talks to it directly from the Mac. Phase 3 adds registration and the outbound tunnel; the wizard looks the same to the user. Later: Create a server for me through provider APIs (Hetzner, DigitalOcean) and team-owned servers.

## Desktop app UX

The main window is a terminal with a title; everything else appears only when it is relevant, and almost nothing needs a button. The approved mockup is the owner's "Portenv app mockup" canvas (main window, title menu, agent presence, six first-run screens).

**Principles**

1. Deference: a thin unified toolbar, full-width terminal, sidebar hidden by default (⌘0).
2. Automate instead of asking: autosave; closing the window saves and releases the box.
3. Box actions live in the title menu, like a document's title menu in macOS.
4. State is shown with quiet symbols (saved, saving, changes) with details on hover.
5. Undo over "Are you sure?": history makes most actions reversible.
6. Power lives in menus and keyboard shortcuts.
7. System font, SF Symbols, system materials, light and dark appearance, VoiceOver labels on every control.

**Main window**

- Title: box name, sync symbol, and a subtitle with machine and save state ("MacBook Air · Saved 2 min ago", "Offline · will save later", "Moving to build-server…").
- Tabs: the user's tabs plus agent lanes, labelled with the agent's monogram and a watching or driving indicator.
- Toolbar items appear only when relevant: listening ports (`:3000 ↗`), agent avatars with an approval badge.
- Drag a file onto the terminal to copy it into the current folder.

**Title menu** (Apple's document-menu pattern)

Rename… · Move To ▸ (this Mac, each server, Portenv Cloud, Add a Server…) · Duplicate for a Task… · Revert To ▸ (Last Save Point, Browse All Saves…) · Show in Finder ⌥⌘R

**Menu bar**

- File › Make Save Point ⌘S.
- Box › Box Settings… (also reachable from Settings).
- Changes are not a menu item: clicking the sync symbol next to the title shows what changed since the last save.

**Other views**

- Changes (click the sync symbol next to the title): files new, changed or deleted since the last save; a Never saved section with sizes; include or exclude per folder.
- Browse All Saves (Revert To ▸): a timeline of saves per machine; open any save read-only, compare any save with now; restore one file or everything.
- Agent popover (click an avatar): identity, where it connected from, lane and branch, a live glimpse of its terminal, pending approval, Watch, Take Over, permissions in one sentence, Revoke Access.
- Sidebar (⌘0): Boxes, Machines, Agents.
- Settings window (⌘,): Account and devices, Protection and recovery key, Storage, Machines, Agents and permissions, Secrets, Box defaults and the open box's Box Settings, Notifications.

**Lease and move sheets**

- Open on another machine: Take over here, Connect to it there, Peek read-only, Cancel.
- Move To: progress shown in the title subtitle; a sheet only if something fails.
- Closing with agents connected: their sessions are saved and paused, and a notification says so. No dialog.

**First run (six screens, full version from Phase 3)**

This is the first run once the control plane exists (Phase 3). The mockups show this version.

1. Welcome: Continue with Apple, Continue with GitHub, Use email instead.
2. Protection: Only you (default) or You, with Portenv's help.
3. Recovery key: Save to Passwords, Print, Copy; Continue enabled only after "I've saved my recovery key".
4. Storage: Portenv storage preselected; Use my own server or bucket… reveals the alternatives.
5. First box: name, Start from (GitHub repository, folder on this Mac, empty), detected toolbox with Change…, Show this box in Finder (on).
6. Getting ready: Setting up Portenv (first run only), toolbox, encrypted home, clone, start. The window opens as soon as the box is usable.

**First run in Phases 1 and 2 (no Portenv backend)**

Phases 0 to 2 have no Portenv backend, so the first run is reduced. Same six steps and layout, with these differences:

1. Welcome: a single Get started button. No sign-in; the device key is generated locally.
2. Protection: fixed to Only you, shown as an explanation (your keys stay on your devices; losing them and the recovery key loses the data). No choice is offered.
3. Recovery key: unchanged.
4. Storage: My own server, An S3-compatible bucket, or This Mac only. No Portenv storage. This Mac only keeps the box's repository at `~/Library/Application Support/Portenv/Repositories/<box-id>`, outside the box and the engine VM, and the screen says plainly that saves then have no copy off this Mac. Time Machine may back the folder up; it is already encrypted by restic. After a week of use, one dismissible notice says "Your saves only exist on this Mac. Add a server or bucket to keep a copy elsewhere.", and Settings › Storage shows the same fact as a permanent status line.
5. First box and 6. Getting ready: unchanged.

In Phase 3, sign-in, Portenv storage and the recoverable mode are switched on. Existing users get a one-time prompt to create an account and link their devices; their boxes, keys and storage stay as they are.

Unsupported Macs (Intel, or macOS before 26) get one extra screen after Welcome: use Docker Desktop or OrbStack, or run boxes on a server and use this Mac as a client. Notification permission is requested when the first agent connects, not during first run.

**Every launch after**

Reopen the last box directly. Show a sheet only if needed: lease held elsewhere, engine stopped, or recovery required. Offline: the box runs locally and saves queue.

**Finder (File Provider)**

Each box appears as Documents › Portenv › \<box> and under Locations in the Finder sidebar. Files download on demand when the box runs remotely. `node_modules` and caches are hidden. The home itself stays in the box (no bind mount of the whole home) for speed and Linux case sensitivity. Phase 1 may ship a simpler shared folder (`/home/work/Mac`) before the File Provider.

## Engines, dependencies and distribution

Portenv never links an engine into its code: drivers talk to engines through their APIs, and the Mac only ever installs the Portenv app.

**Box driver interface**

`Create`, `Start`, `Stop`, `Destroy`, `Exec` (bootstrap only), `Logs`, `Stats`, `SetResources`, `MountHome`, `Capabilities`. Terminals, ports and files go through the box agent, not the driver. One conformance test suite runs against every driver.

| Driver | Where | Engine | Phase |
| --- | --- | --- | --- |
| `docker` | Mac (fallback), Linux servers | Docker Engine API over its socket | 0 |
| `apple` | Mac, Apple silicon, macOS 26+ | Apple Containerization (Swift, Apache-2.0), each box in its own lightweight VM | 1 |
| `microvm` | Portenv Cloud, KVM-capable servers | Firecracker or Kata (Apple Containerization's cloud-hypervisor backend is an option to evaluate) | 5 |

Apple Containerization is pre-1.0: pin its version and expect API changes between minor versions.

**What ships where**

| Dependency | How it ships | License note |
| --- | --- | --- |
| restic | Signed binary inside the app bundle and the runner, called as a process | BSD-2-Clause |
| `portenvd`, Containerization shim, File Provider extension | Inside the app bundle | Ours |
| Linux kernel and base image for the Mac engine | Downloaded on first run with signature verification | Kernel is GPLv2: publish matching sources |
| Docker Engine on servers | Installed by the wizard from Docker's repository | Moby, Apache-2.0; Docker Desktop is not required |
| Toolbox contents (Node, Claude Code, tmux, psql) | Inside toolbox images only | Each its own |
| App updates | Direct distribution, signed and notarized, Sparkle for updates | Not the Mac App Store: helper daemons and VM networking conflict with its sandbox |

Docker Desktop and OrbStack carry commercial-use license terms, so they are optional fallbacks, never requirements.

**Minimum requirements**

Mac app: macOS 26 on Apple silicon for local boxes. Older Macs run as clients of remote boxes, or use the Docker fallback. Servers: Ubuntu 22.04/24.04 or Debian 12, arm64 or amd64.

## Tech stack and repository layout

One public monorepo for everything that runs on users' devices and servers: Swift for everything the user sees on Apple devices, Go for everything that runs boxes, shared protocol definitions between them. The control plane and gateway (Go + PostgreSQL) live in the private `portenv/cloud` repository and import the generated protocol module from here.

**Why this split:** Go has mature SSH, Docker, gRPC and tunnelling libraries, builds static binaries for every Linux architecture, and lets `portenvd` and the runner share one codebase. Swift is required for SwiftUI, the File Provider and Apple Containerization; the Containerization driver is therefore a small Swift process that `portenvd` calls locally.

```text
portenv/
  apps/
    mac/                  Portenv.app (SwiftUI), File Provider extension
    ios/                  iPhone companion (Phase 4)
  core/                   Go module github.com/portenv/portenv/core
    cmd/portenvd/         Mac daemon
    cmd/portenv-runner/   server daemon (same core, server build)
    cmd/portenv-agent/    in-box agent (static, linux/arm64 + amd64)
    cmd/portenv/          CLI for agents and power users
    agent/                portenv-agent: start sequence, readiness, init duties
    driver/               box driver interface + docker, apple (client), microvm; drivertest: the conformance suite
    sync/                 restic saves and restores, leases, invariants
    keys/                 key handling, Keychain and server key stores
    relay/                port relay, tunnels (yamux over WebSocket)
    lanes/                agent users, worktrees, approvals, audit
  shims/
    containerization/     Swift process implementing the apple driver
  images/
    toolbox-node/         Dockerfile, skeleton home, manifests
  proto/                  gRPC definitions shared by app, daemons, cloud
    gen/go/               generated Go code, its own module (imported by core and portenv/cloud)
  scripts/                repository checks (SPDX headers)
  docs/
    milestones/           one demo note per milestone (<id>.md)
    skill/SKILL.md        the published how-to-use-a-box skill for agents
    adr/                  architecture decision records
  tests/
    e2e/                  two-machine scenarios, the restic isolation probe, chaos tests
```

**Conventions**

- Local app ↔ daemon: gRPC over a Unix socket in the user's Library folder, authenticated by peer credentials.
- Configuration: no plaintext secrets in any config file; keys only in Keychain or the server key store.
- CI: GitHub Actions. macOS arm64 runners for the app and the apple driver; Linux arm64 and amd64 for Go; multi-arch toolbox builds pushed to the registry on tag.
- License: Apache-2.0. Every source file starts with an `SPDX-License-Identifier: Apache-2.0` header.
- Engine boundary: a test in `core/internal/boundary` fails if code outside `core/driver/` imports an engine SDK or runs an engine CLI.
- Each architecture decision that changes this plan gets an ADR in `docs/adr/`.

## Roadmap

Six phases, each ending in a gate: the box and its save engine first, the native app second, servers third, then the control plane, agents and the cloud. No dates are set yet; the owner assigns them per phase.

```text
Phase 0  Foundations              -> gate: Mac to server to Mac loses nothing; saves survive crashes
Phase 1  Native Mac app           -> gate: download to a working box without typing a command
Phase 2  Developer servers        -> gate: Move To works both ways from the title menu
Phase 3  Control plane, gateway   -> gate: a box is reachable by name wherever it runs
Phase 4  Agent access             -> gate: an outside agent works in its lane and cannot escape it
Phase 5  Portenv Cloud            -> gate: ten isolated clones of one box run in parallel
```

A phase starts only when the gate above it passes; the checklists below are the detailed gates.

### Phase 0: Foundations (owner only)

Milestones: 0.1 repository, CI and protocol scaffold · 0.2 `toolbox-node` image, multi-arch, with skeleton home · 0.3 `sync` package (restic saves and restores, tag-based leases, invariants) · 0.4 `docker` driver and a temporary `portenv` CLI (resume, save, point, status, history, move) · 0.5 server setup script (Docker, encrypted volume) and the `ssh portenv` entry.

Each milestone ends with a demo note in `docs/milestones/<id>.md` and its checklist ticked here.

**0.1 Repository, CI and protocol scaffold**

- [x] `make build test lint proto-check` passes from a clean clone on a Mac
- [x] All four binaries build; `portenv-agent` is statically linked for linux/arm64 and linux/amd64
- [x] `driver.v1` and `types.v1` are fully defined; `daemon.v1` and `agent.v1` are skeletons marked unstable; `buf lint` is clean and regenerating code produces no diff
- [x] The Go `Driver` interface mirrors `driver.v1` (signatures only)
- [x] The engine-boundary test rejects engine SDK imports and engine CLI calls outside `core/driver/`
- [x] CI covers Go on Linux arm64 and amd64, proto, Swift on macOS arm64, workflow lint and a secret scan
- [x] License, NOTICE and SPDX headers are in place; ADRs 0001 and 0002 are written

**0.2 Toolbox image**

- [x] `images/toolbox-node` builds for arm64 and amd64 from one Dockerfile, with `portenv-agent` as init
- [x] The skeleton home creates `work` (uid 1000), `.portenv/apt-packages.txt` and the default `excludes`
- [x] Packages in `apt-packages.txt` are reinstalled at start, before the box reports ready

**0.3 Sync package** (tests first)

- [x] Table-driven tests cover all five resume rules and every invariant, against real restic repositories
- [x] Tag-based leases (`active:<machine-id>`) work across two simulated machines
- [x] Killing restic mid-save leaves the repository consistent and the next save succeeds

**0.4 Docker driver and temporary CLI**

- [x] The `docker` driver implements the `Driver` interface and passes the conformance suite
- [x] ADR settles `MountHome` (an encrypted named volume on Docker; an ext4 disk image on Apple), and where restic runs (ADR 0005)
- [x] `portenv resume`, `save`, `point`, `status`, `history` and `move` work on a local box (`move`'s remote resume is exercised against a real server in 0.5)

**0.5 Server setup**

- [ ] The setup script installs Docker and an encrypted volume for box homes on Ubuntu 24.04
- [ ] `ssh portenv` lands in the box's tmux session
- [ ] The Phase 0 gate below passes

**Phase 0 gate**

- [ ] Mac → server → Mac round trip loses nothing (checksums of `/home` match)
- [x] All five resume rules and every invariant have passing tests, including two simulated machines
- [ ] A 5 MB change saves in under 10 seconds; resuming on the same machine takes under 5 seconds
- [x] Killing the process mid-save leaves the repository consistent and the next save succeeds
- [x] The toolbox image builds for arm64 and amd64 from one Dockerfile

### Phase 1: Native Mac app, local boxes

Milestones: 1.1 `portenvd` with the local gRPC API · 1.2 main window with a terminal view (evaluate SwiftTerm, MIT-licensed) and tmux-backed tabs · 1.3 `apple` driver shim, `docker` as fallback (the box agent drops the forbidden capabilities from every process it starts, since a VM's root holds them by default) · 1.4 autosave, sync symbol, Changes, Browse Saves · 1.5 first run (reduced, no sign-in; see Desktop app UX), Keychain keys, recovery key · 1.6 port relay · 1.7 shared folder, then the File Provider · 1.8 signing, notarization, Sparkle updates.

- [ ] A new user goes from download to a working box without typing a command or creating an account
- [ ] Closing the window saves and releases; reopening restores within 5 seconds
- [ ] Offline: work continues and saves upload when the network returns
- [ ] Every control has a VoiceOver label; light and dark appearance both pass review
- [ ] A dev server bound to the box's localhost opens in Safari through the relay
- [ ] Inside an `apple` box (a VM), the restic password probe (`tests/e2e/restic-isolation.sh`) passes: root in the box cannot read the password, because the agent drops the forbidden capabilities itself

### Phase 2: Developer servers

Milestones: 2.1 runner (server build of the core) installed over SSH · 2.2 Add a Server wizard with preflight · 2.3 Move To in the title menu · 2.4 lease sheet · 2.5 toolbox registry, versions and drift warnings · 2.6 agents over direct SSH.

**2.6 Agents over direct SSH** (no Portenv backend needed)

- Both agent modes, stand-in and guest, for boxes on a developer server.
- Connect an agent creates the agent's key, restricted to that box and mode, and shows the connection details to give the agent.
- The app lists connected agents with Watch, Take Over and Revoke.
- Agent signals, tool-agnostic:
  - Core events, for any terminal program: waiting for input (output idle, with the last screen of text attached), command finished with its exit code (from shell semantic-prompt markers), and new output since the last read.
  - Optional adapters add structured detail where a tool supports it, for example Claude Code hooks for permission prompts and finished replies. No feature may depend on an adapter.
  - `portenv events --follow` streams these events as JSON lines; `portenv send <tab> "<text>"` types into a tab; `portenv screen <tab>` returns the current screen as text.
  - `docs/skill/SKILL.md` documents the loop for any agent: follow events, relay questions to the human, send answers. It names no specific coding tool.
- Not in 2.6: phone approvals, the gateway and certificates. They stay in Phases 3 and 4.

- [ ] Move To works Mac → server → Mac from the title menu with no data loss
- [ ] Every lease sheet path behaves as specified, including a stale lease
- [ ] A server reboot leaves boxes recoverable without user intervention
- [ ] The wizard explains every preflight failure in plain language
- [ ] A stand-in agent connects from its own computer, drives the user's Claude Code session by keystrokes in `/home/work/<project>`, and the user sees its activity attributed by name
- [ ] A guest agent cannot read the user's home
- [ ] Revoke ends either kind of session immediately
- [ ] An agent relays a question from a CLI tool to a human and answers it using only `portenv events` and `portenv send`, tested with at least two different tools

### Phase 3: Control plane and gateway

Milestones: 3.1 accounts (Apple, GitHub, email) · 3.2 device enrollment and wrapped keys · 3.3 authoritative leases · 3.4 box agent tunnels and gateway routing by box name · 3.5 Portenv storage · 3.6 push notifications · 3.7 full first run (sign-in, Portenv storage, recoverable mode) and the one-time account prompt for existing users.

- [ ] A box is reachable by name wherever it runs, with no inbound ports anywhere
- [ ] Revoking a device ends its sessions within 5 seconds
- [ ] A control plane outage does not interrupt local work or saves to the user's own storage
- [ ] An existing Phase 1 or 2 user creates an account and links their devices without losing a box or re-entering keys

### Phase 4: Agent access

Milestones: 4.1 both agent modes everywhere a box runs, building on 2.6 (lanes: users, tmux, worktrees, limits) · 4.2 approval helper for sudo, secrets and protected scripts · 4.3 secrets vault · 4.4 session recording and command audit · 4.5 doors: SSH on 443, `portenv ssh`, HTTP API, MCP, events · 4.6 Connect an agent with device-code login and the published skill · 4.7 iPhone companion for approvals.

- [ ] An agent in a third-party sandbox connects with a device code and works in its lane
- [ ] It cannot read other homes or ungranted secrets, verified by an adversarial test suite
- [ ] A `sudo` request reaches the iPhone and resumes on approval
- [ ] Revoke ends the agent's session immediately

### Phase 5: Portenv Cloud

Milestones: 5.1 `microvm` driver and cloud hosts · 5.2 per-box encrypted volumes · 5.3 wake on demand (with end-to-end approval) · 5.4 Duplicate for a Task (fast clones from a snapshot). Cloud-side work for this phase lives in `portenv/cloud`.

- [ ] A box starts on Portenv Cloud in seconds from its latest save
- [ ] Ten clones of one box run in parallel, isolated from each other
- [ ] Tenant isolation passes an external review

**Later:** teams and shared servers, Create a server for me through provider APIs, Linux and Windows apps, a self-hosted control plane for enterprises.

## Testing and quality

The save engine is tested hardest, because a bug there loses someone's work; everything else is tested to the level its failure would cost.

| Area | How | Gate |
| --- | --- | --- |
| Sync and leases | Table-driven tests over every resume rule and invariant; property tests with random sequences of edits, saves and machine switches | Every commit |
| Two-machine scenarios | e2e harness with two simulated machines against real restic repositories (local folder, MinIO for S3, SFTP container) | Every merge |
| Crash safety | Kill `portenvd` or restic at random points during save and restore; cut the network mid-upload | Nightly |
| Drivers | One conformance suite run against `docker`, `apple` and `microvm` | Every merge touching a driver |
| Keys | Device add, revoke, recovery, re-encrypt; verify no key material on disk outside approved stores | Every merge touching keys |
| Agent isolation | Adversarial suite: an agent lane tries to read other homes and secrets, escalate privilege, reach blocked hosts, persist after revoke | Every merge from Phase 4 |
| Mac app | XCUITest for first run, title menu, lease sheet; accessibility audit; light and dark snapshots | Every merge touching the app |
| Performance | Budgets: 5 MB save under 10 s, same-machine resume under 5 s, port relay adds under 5 ms locally | Nightly |
| Recovery drill | Restore a box from the recovery phrase on a clean Mac | Before each release |

CI runs the Go suites on Linux arm64 and amd64 and the app suites on macOS arm64 runners. A release is blocked by any failing gate.

## Open questions

These need an owner decision; Claude Code should add new ones here instead of guessing.

- [ ] Default protection mode for new users: Only you (current default) or recoverable?
- [ ] Phase 2: install the runner as a service from the start, or plain SSH first? The plan assumes the runner.
- [ ] Support Intel Macs and macOS before 26 with the Docker fallback, or make them remote-only clients?
- [ ] Trademark and domain check for Portenv in the EU and US; confirm portenv.com is registrable.
- [ ] Stand-in agents in 2.6, before approvals exist: they act as `work`, who has passwordless sudo. Allow that (the app says the agent has full access), or withhold sudo from stand-ins until the Phase 4 approval helper?
- [ ] Docker inside the box (Phase 1): how to provide it without granting the box `CAP_SYS_ADMIN` or privileged mode (for example rootless Docker or a nested sandbox), or whether the setting ships with a plain warning that it weakens the repository-password protection (ADR 0005).
- [ ] Point-in-time autosaves while the box runs: worth a file system with snapshots (for example btrfs on the Apple disk image), or are file-consistent saves plus the pre-save hook enough? (ADR 0005)
- [ ] Approval timeout default (30 minutes assumed) and what happens when it expires.
- [ ] Anthropic's terms for agents driving Claude Code with a subscription login versus a Console API key.
- [ ] Open-source boundary in detail: this repository is Apache-2.0 and `portenv/cloud` is private (decided), but confirm before going public whether the Mac app, the File Provider and the iPhone companion stay in the public repository or move to a private one.
- [ ] Phase 3 control-plane milestones now live in `portenv/cloud`: how its CI pins and tests against `proto/gen/go` versions from this repository (tags, or a pseudo-version per merge).
- [ ] Scope of the iPhone companion: approvals only, or also status and a read-only terminal?
- [ ] Name of the CLI binary: `portenv` assumed.
