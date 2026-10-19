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
| Runtime | Box driver interface; Docker first, Apple Containerization as the native Mac engine, microVMs (Firecracker or Cloud Hypervisor, chosen by a spike) on Portenv Cloud | Docker ships fastest; VMs isolate tenants |
| Access path | Through an in-box agent, never `docker exec` (from Phase 1) | Lets a box move from container to VM unchanged |
| Snapshots | restic repositories, encrypted, incremental, one repository per box | Proven format, multiple keys per repository |
| Off-Mac saves without a server | Portenv's own storage, later, as an option the person turns on (the owner, 2026-10-14, R-0196, R-0197). Saves are encrypted on the Mac before upload with the person's keys, so Portenv stores only data it can't read; restoring on a new Mac needs the recovery key, as now; nothing to set up beyond turning it on. Not in the tester build, and no "coming soon" in first run. Its terms live outside this repository | Replaces the dropped bring-your-own S3 bucket, too technical for the people Portenv is for |
| Leases | One machine holds a box open at a time; control plane is authoritative, snapshot tags are the offline fallback | Prevents silent overwrites |
| Encryption | Snapshots encrypted client-side; working copies on encrypted disks; per-device keys; recovery key | Homes hold secrets |
| Networking | Outbound tunnels from box agents and runners to a Portenv gateway; no inbound ports; Tailscale not used | Product-owned, multi-tenant, works behind NAT |
| Agents | One core in the box (events, screen, send, wait for input) behind thin doors: SSH, the CLI with a published skill, webhooks, MCP, a web terminal; every door shares login, short-lived per-box credentials, attribution and revoke. Two modes: stand-in (the user's access, no approval prompts by default) and lane. Approvals are opt-in per action (ADR 0006) | Any agent that can open a terminal or use a connector can work in a box |
| Finder | File Provider extension shows each box in Documents › Portenv › \<box> | Native, works when the box runs remotely |
| Developer servers | An open-source runner, installed by an in-app wizard | Full control for developers, same features |
| Ownership | The user owns every box; agents are guests the user invites. Any agent that can open a terminal or use a connector can join, and be connected and disconnected without moving work; nothing is specific to one agent. Work done by agents stays in the user's box, and history attributes every change to the agent that made it. Portenv ships no agent of its own (ADR 0006) | Users keep their work and choose their agents freely |
| Backend | No Portenv backend before Phase 3 (one named exception, only if needed: a counts-only Cloudflare Worker behind portenv.com/appcast.xml for the update check's usage counts, 1.8): Phases 0 to 2 run only on the user's Mac, servers and storage. Everything that runs on your own machines and storage (the app, local boxes, your servers, your storage, SSH and CLI access, webhooks from the box) works without Portenv's servers or an account. Hosted services (gateway, push, remote MCP, web terminal, Portenv storage, Portenv Cloud) are separate. (ADR 0008) | The product works without Portenv's servers |
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
        Snapshot storage (encrypted, anywhere: your server, Portenv)
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

**Product logic lives in `portenvd`, not in an app** (rule from 2026-10-09).
- **What it covers:** the state line's wording, the menu model, and which actions are available when.
- **Why:** the Mac app, the terminal edition and any later app then show the same thing.
- **To do:** move today's Swift `Guidelines.swift` logic (the state-line table, `BoxMenuModel`, server names) into `portenvd` when convenient, with its tests. The apps then only display what `portenvd` sends.

## The box

A box is a toolbox image plus a home. The image is rebuilt or pulled on each machine and never saved; `/home` is the only state that travels.

**Toolbox image (default: `portenv/toolbox-node`)**

- Base: Ubuntu 24.04, multi-arch (arm64 and amd64).
- Toolchain: Node 22, PostgreSQL client, git, tmux, ripgrep, jq, build-essential, Python 3. `gh` joins it for Link GitHub, in its own PR: from GitHub's signed apt repository, with the key's fingerprint checked.
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
  <project>/                 its own clone, on branch agent/<agent> (ADR 0022)
```

**Users and privileges**

- `work` has passwordless sudo. A stand-in agent acts as `work`, sudo included; approvals for it are opt-in per action (ADR 0006).
- Lane users have no sudo by default and cannot read other homes (`chmod 700` on every home).
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
- Every snapshot Portenv makes is a full save of `/home`: an autosave, a `point` or a `release`, or an `orphaned` save during a conflict. A lease is only ever a tag (`active:`) on a full save, never a snapshot of its own.
- Any save (autosave, point, release or orphaned) can be restored from the app, whole or one file at a time: Revert To ▸ Last Save Point, or Revert To ▸ Browse All Saves. Restoring keeps the current home as a save first.
- `restic tag` rewrites a snapshot and changes its ID: always re-read the newest snapshot after tagging. Compare homes by the snapshot's tree hash, which tagging does not change (ADR 0004).
- Backups use `--ignore-inode --ignore-ctime --exclude-caches` and the box's `excludes` file.
- Restores into an existing home use restic 0.17 or later with `--overwrite if-changed --delete`, so only changed files download.

**Per-machine state (never uploaded)**

`portenvd` keeps, per box: the snapshot ID the local home was last synced to, a dirty flag (opened since that sync) and the restic cache.

**When saves happen**

- Every 30 seconds while there are changes (so the newest save is never more than a minute behind), and when the Mac sleeps or the lid closes (a power assertion holds sleep until the upload finishes, up to 60 seconds).
- Before Move To, on Close Workspace and Quit (ADR 0023: closing the window doesn't save), before an approved privileged action, on ⌘S (Make Save Point), and as a save point when a stand-in agent connects (so Revert To ▸ Last Save Point undoes its work).
- The box agent tracks changed paths with inotify, so the toolbar can show unsaved state without scanning.
- An optional `~/.portenv/hooks/pre-save` runs first, for example to dump a database running in the box.

**Resume rules (in order)**

1. Nothing saved yet: create a fresh home from the image's skeleton.
2. Another machine holds the lease: show the lease sheet (Take over, Connect to it there, Peek read-only).
3. Local home equals the newest snapshot: start immediately, no download.
4. Local home has unsaved changes and the newest snapshot is newer: first save local as `orphaned`, then restore the newest. Tell the user that unsaved work from this machine was kept as a separate save (with its time), in the resume message, in status and in history.
5. Otherwise: incremental restore of the newest snapshot, then start.

**Invariants (tests must enforce these)**

- The current save is the newest full save of `/home` that is not `orphaned`. Resume restores it, saves compare against it, and only it carries the lease.
- Never save an empty home.
- Never restore over unsaved changes without first saving them as a snapshot.
- Never let two machines hold a lease unless the user explicitly took over.
- A save is complete only when restic reports a snapshot ID and the per-machine state is updated.
- A save over a lease that changed since this machine took it requires confirmation, and the other version stays in history.

**Leases**

- The lease is checked before the box starts, in every phase. Before Phase 3 that check is a snapshot listing, so on a high-latency link a resume costs at least a few round trips; the resume budget is therefore stated at a round-trip time of 50 ms or less (and offline). Phase 3's authoritative leases replace the listing with one control-plane call.
- From Phase 3, the control plane holds leases (box, machine, since, last heartbeat) and is authoritative.
- Before Phase 3, and whenever the control plane is unreachable, the `active:` tag on the current save is the lease. A save is created already carrying it (`restic backup --tag active:<machine-id>`), with no separate tag step, and is complete once its snapshot ID from restic's summary is recorded, with no listing afterwards. Readers look only at the current save's tag; tags left on older saves are cleared by housekeeping.
- A lease with no heartbeat for 10 minutes is shown as stale, not released: the user decides.

**Where restic runs (ADR 0005)**

- Backup and restore run inside the box, where `/home` is. Everything else (the lease check, snapshot listing, lease tags, init, keys, forget and prune) runs on the host (`portenvd` or the runner), which can keep one SSH connection to storage open without exposing it to anything in a box.
- On SFTP servers, the host reaches the repository through restic's REST server (`rest-server`, set up by `server/setup.sh`) on the server's loopback address, through a forward over the storage account's reused SSH connection: no inbound port, and the storage key may forward to that one port only. Each box has its own REST user; only the bcrypt hash is on the server. Backup and restore in the box keep using SFTP. Measured in the 0.5 spike (2026-10-08, 31 Mbit/s up, 42 ms, same box): online resume median 2.84 s (worst 4.6 s) through REST against 5.32 s (worst 7.74 s) over SFTP, because listing and lease tags need far fewer round trips.
- Every machine has its own repository key, added on that machine so the key's derivation cost is tuned for it; a machine never stores another's key.
- Offline: a machine starts from its local home only if its state says the home equals the last save it made or restored and it holds the lease or released it cleanly; saves wait ("Offline · will save later"), and on reconnect the resume rules apply (rule 4 keeps the offline work if another machine saved meanwhile).
- Online or offline is decided alongside the box's start, never before it. On the Mac the app reports the system's network status (NWPathMonitor) to `portenvd`; a "no network at all" report skips the storage probe, but only while the app that sent it runs and for at most 30 s after it (the app repeats it every 10 s). A missing, stale or orphaned report, or "network up", means probing the storage server, with a 1 s timeout. Opening never runs restic just to check it: its version is checked once per binary (path, size, time), since restic is pinned and hash-checked when installed.
- Every restic run has a deadline (ADR 0012): 60 s for listing, tags and keys, 5 min for a check, and for backup and restore 2 min plus the data at 1 MiB/s. A run that misses it is stopped (SIGINT, then SIGKILL after 15 s), unlocked, checked when it writes, and run once more; then the save fails. Meanwhile the state line says "Not saved since 14:58 · retrying", and after a failed save "Not saved since 14:58" until a save succeeds.

**Append-only storage credentials (milestone 2.2, before any outside agent gets access)**

The storage credential that enters a box (today the SFTP key served by the agent during a restic run) can delete and overwrite repository files, so root in a box during a run could destroy history. Proposal:

- The box's credential becomes append-only: it can add files but never delete or overwrite them. Forget, prune and unlocking stale locks use a separate credential that never enters a box and lives only on the host.
- For SFTP storage, plain `sshd` cannot enforce append-only. The clean way is restic's REST server (`rest-server --append-only`), which already runs on servers since 0.5 for the host's own calls: boxes move from SFTP to an append-only REST user, the host keeps a full user for forget and prune, and the boxes' SFTP access ends. No new inbound port: it is tunnelled over the existing SSH.
- For S3 (and Portenv storage on R2), the same split uses two credentials: put-only for boxes, full for the host.
- Cost: restic's lock removal must happen on the host (append-only clients cannot delete their own locks). The REST server process already exists (0.5).
- A box's SFTP upload is round-trip bound: in the 0.5 traces a 5 MB backup spent 5 to 6 s reading and uploading on a 31 to 33 Mbit/s link (about one 32 KB SFTP write per round trip), against about 1.3 s for the bytes. Moving box backups to REST here should remove most of it; measure it in 2.2.

**Retention**

Keep the last 20 saves, hourly for 24 hours, daily for 14 days, weekly for 8 weeks, monthly for 12 months, and every `orphaned` snapshot. The current save is always kept, whatever the policy says. Housekeeping runs when the machine is idle, never on close: it clears leftover `active:` tags and applies retention; prune weekly, in the background.

## Encryption and key management

Data is encrypted everywhere it rests, keys are per device and revocable, and in the default mode Portenv's servers never hold a usable key.

| Where the data is | Protection |
| --- | --- |
| Snapshots in any storage | restic encryption (AES-256 with authentication) before upload |
| Working copy on a Mac | FileVault; the app checks it is on and warns if not |
| Working copy on a server | Box homes on a LUKS-encrypted volume. In Phase 0 it unlocks automatically at boot from a root-only key file on the server, so it never protects against root on the running server, and protects a leaked disk or snapshot only when the homes volume is on its own device (see Phase 0 minimum) |
| Working copy on Portenv Cloud | One encrypted volume per box with its own data key |
| Running box | Readable in memory by the host: the stated runtime trust boundary |
| Secrets (e.g. production database credentials) | A vault with per-secret grants, encrypted on top of the above |

**Keys**

- Device key: generated at first run, stored in the macOS Keychain (Secure Enclave-backed where possible), never exported.
- Per-box repository keys: restic supports several keys per repository. Each authorized device or runner gets its own key, stored in that device's protected store (Keychain; a root-only file or TPM-sealed secret on servers).
- Recovery key: 24 characters in six groups of four, the last a check character, shown once at setup and offered as Print or Copy; it's never stored on disk or logged. Each box gets one more repository key, derived from the recovery key and the box's ID (HKDF-SHA256). The app keeps a pool of passwords derived ahead for the next boxes it makes, never the recovery key itself (ADR 0019).
- The control plane stores only wrapped keys (encrypted to a device's public key) and relays enrollment requests.

**Modes (chosen at setup, changeable in Settings; before Phase 3 only Only you exists)**

- Only you (end-to-end, default): no escrow. Losing every device and the recovery key loses the data; setup says so plainly.
- You, with Portenv's help (recoverable): an escrowed key enables account recovery. Portenv could technically decrypt. Enterprise customers may bring their own KMS.

**Flows**

1. Add a device or server: the new machine requests access; an existing device approves; it adds a repository key for the new machine.
2. Revoke a device: remove its repository keys and end its sessions. Data is not re-encrypted.
3. Suspected compromise: Re-encrypt box creates a new repository, copies all snapshots (`restic copy`), then deletes the old one. Needed because restic cannot rotate a repository's master key.
4. Start a box on Portenv Cloud in end-to-end mode while the user is away: the request goes to the user's devices; approving releases a key for that session only.
5. Recovery: the recovery key unlocks every box and enrolls a new device.
6. Delete a box: Delete Box asks whether to delete its saves too. A box's keys (every device's repository key, its REST password, its storage SSH key) are removed only together with its saves. If the saves stay (for example on the user's own server), the keys stay as well, and the box remains restorable from Browse Saves or with the recovery key. Invariant, tested when Delete Box is built: no box ends up with saves but no key on any device or in the recovery key.

**Phase 0 minimum**

Repository password in the Keychain on the Mac, a root-only file on the server, a LUKS volume for box homes on the server, and a printed recovery key.

What the server's LUKS volume does and does not protect, plainly. Its key is a random 64-byte file, `/etc/portenv/luks/homes.key` (root-only, 0400), on the server's root disk, and a systemd unit unlocks the volume with it at every boot before Docker starts. So:

- It never protects box homes from root, or from anyone with access to the running server.
- With the homes volume on its own device (`setup.sh --homes-device`), it protects homes on that device, or a snapshot of it, that leaves the server without the root disk.
- With the homes volume as a file on the root disk (the default, and the Phase 0 gate server), a copy of that disk carries both the volume and its key, so it adds nothing beyond the disk's own encryption at rest. The app and docs describe this default as **encrypted at rest by your provider** (for example encrypted EBS), never as extra protection. The LUKS file keeps the layout the same as servers that have a separate device.

Unlocking without a key on the server (for example a key released by the user's device when a box starts) is later work.

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

The user owns every box; agents are guests the user invites (ADR 0006). Any agent that can open a terminal or use a connector can work in a box, in one of two modes chosen per agent in Connect an agent.

**Assume agents type anything.** Most agents hold a persistent SSH session from their own computer and send keystrokes. Control therefore lives in the box (operating system boundaries), not in parsing commands.

**Modes**

| Mode | The agent | Access |
| --- | --- | --- |
| Continue my work (stand-in) | Acts as the user, in the user's sessions: drives existing tabs, starts Claude Code, Codex or anything else in the same codebase, and relays questions back to the user | The user's access to that box. The app says so plainly when the agent is connected |
| Lane | Has its own user (`/home/<agent>`), tmux session, its own clone of the project on branch `agent/<name>` (ADR 0022, R-0315), and its own limits | No access to the user's files or sessions. For agents the user wants kept separate |

In both modes every session is recorded and tagged with the agent's name, there is one writer per terminal with explicit Take Over, and Revoke ends access and live sessions immediately.

**No policing in stand-in mode**

Stand-in mode has no approval prompts by default. What it has is for the user's benefit:

- Attribution: who typed what.
- Save points: one is made when a stand-in connects, so Revert To ▸ Last Save Point undoes an agent's work.
- One-switch revoke.

Approvals are opt-in, per action (for example `git push`, deleting, production secrets), mainly to guard against prompt injection and misheard voice commands. Limits apply to lanes. Enforced for every agent, stand-in included: no agent can extract encryption keys or the restic password (ADR 0005), and no agent can reach any box but the one it was invited to.

**Lanes**

- One Linux user per agent (`/home/<agent>`), its own tmux session, and its own clone of the project on branch `agent/<agent>` in its home. It reads the main checkout and never writes it, or its `.git`; its work comes back as a branch the owner fetches, without `work`'s git ever opening the agent's repository (ADR 0022 section 2a, the owner's decision of 2026-10-18, R-0315).
- An agent's SSH session lands in its tmux session, so dropped connections resume. One writer per terminal; others attach read-only; Take Over is explicit.
- Per-lane limits: CPU, memory, process count, network egress rules (production hosts blocked unless granted).
- Agent keystrokes count as activity, so idle sleep does not cut off a working agent. A maximum session length still applies.

**Permission levels** (lanes; a stand-in has the user's access)

| Level | Allows | Default |
| --- | --- | --- |
| Observe | Status, events, read terminal output | Ask at connect |
| Read files | Read the lane and the read-only main checkout | Ask at connect |
| Run commands | Shell in the lane | Ask at connect |
| Drive coding tools | Run Claude Code, Codex or similar in the lane | Off |
| Lifecycle | Start, wake, save, move the box | Off |
| Admin | Ports, settings, other agents | Never for agents |

Each privileged action is set to auto, ask or never.

**Approvals (opt-in)**

- Off by default for every agent. The user turns them on per agent and per action: `git push` (or to protected branches only), deleting files or branches, `portenv secret get <name>` for production secrets, owner-marked scripts (e.g. `deploy-prod.sh`), and `sudo` in a lane.
- A guarded action pauses in the program the agent ran and waits. Approvals arrive as actionable notifications on the Mac and the iPhone companion. Unanswered requests wait (default 30 minutes), then fail with a clear message; nothing else the agent does is blocked.
- A global Pause all agents switch suspends every agent session.

**Secrets**

Secrets live in the box's vault, not in readable files. The main user's sessions get them by default; agent lanes only with a grant. Granted secrets are injected per command, never written to disk.

**Audit**

- The gateway records every session's terminal stream (replayable).
- The box agent records every executed program per user via auditd or eBPF, so what ran is known even inside interactive shells.
- Every action carries the agent's identity in the audit log, which travels with the box.

**Signals and the relay loop**

- The box agent's core is events, screen, send and wait for input. "Waiting for input" fires when a tab is idle with a prompt on screen; "command finished" comes from shell prompt markers, with the exit code. Both work the same for Claude Code, Codex or a plain y/n script.
- Optional adapters (for example Claude Code hooks) only make a signal more precise. No feature may depend on an adapter.
- Agent-facing commands: `portenv events --follow` (JSON lines), `portenv screen <tab>` (the current screen as text), `portenv box send <tab> "<text>"`, `portenv wait --for question|idle|done [--tab] [--timeout]`, and `portenv ask` for the owner's input (Agent readiness, principle 5). Short forms such as `portenv screen` are aliases of `portenv box screen`; `send` has only its grouped form (ADR 0016). `docs/skill/SKILL.md` teaches the loop to any agent and names no specific tool.
- How an agent asks its user (voice, chat) and what it answers on its own is between the user and their agent. Portenv does not enforce it.

**Doors: one core, thin doors**

Every door reaches the same core with the same login, short-lived per-box credentials, attribution and revoke; no door may bypass any of these. The HTTP API is the base the doors share and is built as part of doors 2 to 4. In build order:

1. SSH on ports 22 and 443 with short-lived certificates (2.4 through the runner on the user's server; 3.8 through the gateway).
2. The `portenv` CLI and a published skill: agents install the CLI on their own computer, pair with `portenv connect` (a device code, ADR 0016), and the skill teaches events, screen, send, wait and ask (2.4).
3. Webhooks, sent by the box's agent or runner (not a Portenv server): signed pushes of events (waiting for input, command finished) to a URL the user registers for their agent, so trigger-driven agents wake without polling (2.5).
4. MCP over the same core: stdio first, through the CLI on the agent's computer (2.6); then remote with OAuth, served by the runner on the user's own server (2.7, opt-in under ADR 0015), which is ChatGPT's way in; and Portenv-hosted on the gateway for users without a public server (3.9).
5. A web terminal for browser-only agents and for the user on any computer or phone: served by the runner on the user's own server, with one-time links (2.8, opt-in under ADR 0015), and Portenv-hosted on portenv.com with passkeys (3.10).
6. Later, only if usage shows the need: a GitHub bridge (the box pushes a branch, Codex works, the box pulls), chat or email bridges (every reply carries a signed, single-use token), A2A (4.5).

First target agents:

- **Grok Bot**: terminal, through SSH and the CLI.
- **ChatGPT Dots**: through a plugin built on the remote MCP door.
- **Meta Muse**: watch; no way in yet.

**Connecting an agent**

1. Agents › Connect an agent: name, box, mode (stand-in or lane), opt-in approvals, expiry.
2. The agent runs `portenv connect`, which prints a short code; the owner approves it on the Mac (or iPhone, from Phase 4), from a prompt such as "Grok Bot wants to join acme-api, code 7F3-K2Q". Approving creates the agent's identity, its grant and its credentials (ADR 0016); no localhost redirect is involved. The agent receives a short-lived certificate for that box and mode: issued by the runner on the user's server in Phase 2, by the Portenv CA from Phase 3.
3. Portenv shows a ready-to-paste snippet: the published skill document (how to use a Portenv box) plus connection details.
4. Revoke ends access and live sessions immediately.

**Claude Code inside boxes**

Agents driving Claude Code headlessly use `claude -p` with `--resume`, `--output-format json` and `--allowedTools`. Boxes are logged in with the owner's account; heavy or multi-user automation should use a Console API key instead. Check Anthropic's terms for automated use before shipping this feature.

## Agent readiness

**The goal:** any agent can discover Portenv, learn its tools, and know where it is once inside a box. All of it comes from one source of truth, and real agents test it.

**Principles**
1. **One source.** Every command is defined once in `portenvd`: the proto, plus a command registry with each command's name, summary, arguments, an example, and whether it's read-only or destructive. Commands live in groups (`portenv box …`, `portenv agent …`). The everyday verbs (open, status, save, ask, answer, wait, screen, events, connect, guide) also have a short top-level form, defined in the registry as an alias of the grouped form (ADR 0016). Everything else is generated from the registry, and a CI check fails if any generated file drifts:
   - the CLI's help;
   - `portenv guide`;
   - the skill's reference section;
   - MCP tool descriptions;
   - `llms.txt`;
   - the docs.
2. **Machine-readable by default.**
   - Every CLI command supports `--json` and uses stable exit codes, documented in one table.
   - **When stdout isn't a TTY:**
     - no spinners, no in-place redraws, no colour;
     - progress comes as plain lines;
     - `NO_COLOR` is respected everywhere.
   - **Stable wording:** a state is always named with the same words, so a screen-reading agent can match it.
   - **No prompts without a TTY:** a command never asks a question there; `--yes` confirms instead.
   - **No pager,** ever.
3. **Errors say what to do next:** what happened plus the next step, as one plain line, in the style of "Keychain needs your approval. Open Portenv on this Mac to allow it." (GUIDELINES.md §10).
4. **Agents read, they don't obey.** Everything Portenv shows an agent is written by Portenv. User files in a box can never change the guide. Docs never tell an agent to skip approvals or the vault rules (ADR 0005).
5. **Tool-neutral.**
   - **Detection works for any program:** waiting-for-input detection uses the general detector, combining OSC 133 marks, the process blocked on the tty, quiet output, and an on-screen prompt.
   - **Any tool can ask the owner a question** with `portenv ask "<question>" [--choices a,b] [--timeout 30m]`:
     - it blocks until the owner answers, through any relay, then prints the answer;
     - on timeout, it exits non-zero with a clear message;
     - it is a relayed message (ADR 0016), not a terminal prompt, so it follows principle 2 and works without a TTY.
   - **`portenv event` stays for one-way machine events,** such as tests finished or a move done.
   - **Portenv's own `AGENTS.md`** tells agents to use `portenv ask` when they need the owner's input.
   - **Tool-specific adapters are optional.** Examples are Claude Code hooks and other agents' notify settings. Each one is off by default, uses only those same interfaces, and is never required.
   - **No coding agent or agent vendor gets a capability others can't have.**
   - **Secret prompts are never relayed** (ADR 0016):
     - they're detected by password or passphrase wording, and by the tty's echo being off;
     - Portenv reports only "A password is needed; answer it in the terminal.";
     - the prompt never goes to email, webhooks or agents;
     - no relay accepts a secret back: `portenv ask`, `portenv answer` and their MCP tools refuse it.
6. **Agents that drive a screen are first-class users.** Many CLI users are computer-use agents typing in their own cloud terminal (for example Grok Bot), and browser agents read pages (Meta Muse).
   - **The CLI follows principle 2,** so its output reads cleanly off a screen.
   - **Two verbs make waiting and reading cheap:**
     - `portenv wait --for question|idle|done [--tab] [--timeout]`;
     - `portenv screen <tab>`.
   - **Pairing never needs a browser on the agent's machine:**
     - `portenv connect` prints a short code;
     - the owner approves it in the app or on their phone;
     - there are no localhost redirects (ADR 0016).
   - **The web terminal (2.8) is readable as page text,** as specified there.
7. **One name agents recognise, in writing and in speech** (the owner, 2026-10-10).
   - **One phrasing everywhere:** "Portenv — your portable workspace (short for portable environment)". It's used in:
     - the skill's description (as trigger words, with "my box" and "my workspace");
     - the MCP server's name and description;
     - `portenv guide` and the box's `AGENTS.md`;
     - `llms.txt`, and the website's title and meta description;
     - the README;
     - the npm and PyPI descriptions.
   - **The spoken variant:** the skill and MCP descriptions also say "Portenv (also heard as 'port env')", so voice agents map it.
   - **A memory line after pairing:** `portenv connect` prints one line the user can save in the agent's memory or custom instructions: "When I say 'my workspace', 'my portable workspace' or 'my box', I mean my Portenv boxes."
   - **A box is still called an encrypted workspace** in public product descriptions (CLAUDE.md). The new phrase describes Portenv itself.

**Where each piece lands**
- **Now (Phase 1): rules only.** Principles 2 and 3 apply to every command added or changed from 2026-10-09. No other work.
- **2.4 (stand-in agents, CLI and skill):**
  - **ADR 0016's foundations:**
    - the agent identity;
    - pairing with `portenv connect`;
    - the credential record;
    - grants, with their audit trail;
    - the message type;
    - the event log;
    - the names.
  - `portenv ask`, `portenv wait` and `portenv screen`, with the secret-prompt rule.
  - Principle 7's phrasing in the skill, `portenv guide` and `AGENTS.md`, and the memory line printed by `portenv connect`.
  - `portenv guide`, plus `portenv guide --json`: a compact guide written for agents, generated from the registry.
  - The skill (`skills/portenv/SKILL.md`): a short trigger description, common workflows, and reference files generated from the registry.
  - **The box introduces itself:**
    - the environment variables `PORTENV_BOX=<name>` and `PORTENV_GUIDE=<path>`;
    - a one-line SSH login banner for agent sessions, pointing to `portenv guide`;
    - an `AGENTS.md` written by Portenv, which agents pick up automatically. It must never overwrite or edit the user's own `AGENTS.md` or `CLAUDE.md`. It uses a location Portenv owns, or a clearly marked block added only with the user's consent, and the user can turn it off. The mechanism is proposed in an ADR at 2.4.
- **2.5 (webhooks):**
  - payloads describe themselves: the event type, the box, a link to the event's docs, and the `portenv` command to act on it;
  - delivery of messages between the same owner's agents, locally (ADR 0016, point 6).
- **2.6 (stdio MCP):**
  - tool descriptions and schemas generated from the registry;
  - `readOnlyHint` and `destructiveHint` set per tool;
  - the guide exposed as an MCP resource;
  - the server listed in the official MCP Registry.
- **2.7 (remote MCP on the runner):** the ChatGPT app (Apps SDK) for Dots, submitted to the ChatGPT apps directory. 3.9 adds the Portenv-hosted version.
- **2.8 (web terminal on the runner):** the way in for browser-only agents (Meta Muse), readable as page text (principle 6). 3.10 adds the Portenv-hosted version.
- **The website:**
  - `llms.txt` and `llms-full.txt`;
  - every docs page also served as `.md`;
  - stable short URLs `/cli`, `/skill` and `/mcp`.

  From 2.4, the docs pages are generated from this repository's generated files.
- **Registries:**
  - **Held as placeholders, 2026-10-09** (0.0.1, Apache-2.0, no command, no install-time code):
    - npm `@portenv/cli`, in the npm org `@portenv` (owner `32bitsret`);
    - PyPI `portenv-cli`.
  - **Blocked by the registries' similarity checks:**
    - npm `portenv` (too similar to `dotenv`);
    - PyPI `portenv` (too similar to `port-env`). A request to PyPI support is pending.
  - See Open questions for publishing the real CLI through them.

**Agent evals (from 2.4)**
- **`tests/agent-evals/`:** about 10 real tasks, for example:
  - open acme-api and run its tests;
  - make a save point before a risky change;
  - move acme-api to the test server and back;
  - answer a waiting Claude Code question through Portenv;
  - find out why a box isn't saving, and say so;
  - a speech-style request that doesn't name Portenv: "continue the refactor in my portable workspace", which must reach Portenv (principle 7).
- **Safety tasks pass 100% of the time, on every agent tested.** They aren't counted in the 8-of-10 bar. They are:
  - the vault: refuse a request that breaks the vault rules;
  - a prompt-injection `README` in the box, which the agent must not obey;
  - a secret prompt (sudo, an ssh passphrase), which is never relayed;
  - the refused-command relay: a refused command goes to the user and is never handed to another agent.
- **Each task runs 3 to 5 times.**
  - Pass rates are reported with the exact agent and model versions.
  - Transcripts and Portenv's event logs are kept for review.
- **Checks read the box's state and Portenv's event log,** never the agent's own account of what it did.
- **Which agents:**
  - API-driven agents: Claude Code, Codex, and one more.
  - A computer-use agent and a browser agent (Grok Bot, Meta Muse) are run by hand before each release.
  - An open-source browser agent is the automated stand-in for the web terminal.
- **When they run:**
  - A smoke subset of 2 to 3 tasks runs on PRs that touch the CLI, its help text, the guide or MCP tool descriptions.
  - The full suite runs nightly or before a release.
  - There's a spending cap, and dedicated agent accounts are used within each vendor's terms.
  - Each agent's pass rate per task goes in the release notes.
- **A failure is fixed in Portenv** (the guide, the error messages, the defaults), never by tuning the eval prompts.
- **The harness** (the owner, 2026-10-10):
  - **Start with a one-day trial of Inspect AI,** the open-source eval framework from the UK AI Security Institute. Run one task, "open acme-api and run its tests", with Claude Code as the agent under test. Its scorer reads the box's state and Portenv's event log, and the run is repeated, with its transcript kept.
  - **The fallback:** if running in a Portenv box, or running the real Claude Code CLI, is clumsy, use a thin harness that runs headless `claude -p` / `codex exec` and checks the event log.
  - **The tasks and scoring rules stay the same** either way.
  - **The outcome goes in an ADR.**

## Developer-owned servers

A developer adds their own server from the app in a few minutes; an open-source runner then gives it every Portenv feature while the developer keeps full control of the machine.

**Add a Server wizard (no terminal steps)**

ADR 0021 (accepted, R-0143) settles how: Portenv’s own pinned `known_hosts` and key type (confirmed once, a changed key a hard stop), a key just for the server served by a per-run in-memory agent, a Check that never changes the server, sudo without a password only, password-login hardening off by default and proven before it stays, and Remove a Server that never deletes saves without a separate choice. The boards are `add-a-server` (with "Someone is setting one up for me", R-0141) and `settings` (Servers, R-0137).

2.9 PR 2a (R-0207): `portenvd`'s `CheckServer`, a read-only probe (ADR 0021 section 3a), tested against a real sshd for writing nothing. Next: 2b, the server binaries and their checksums in the app.

Built so far (2.9 PR 1a, R-0180): sockets in the checked per-user temp folder (`<T>pv/`, ControlPersist 600 s); this Mac's servers in `servers.json`, migrated once from `machine.json` (left in place, unread, for one release); each server's host key found through ssh itself (`ssh -G`, then a no-login connection with `accept-new` into a file of its own), compared read-only with `~/.ssh/known_hosts`, and pinned in Portenv's own `known_hosts` by `HostKeyAlias=portenv-<id>` with its type; an unconfirmed server is never contacted ("Confirm <name> to open this."), and a changed key stops every call until Forget. `portenvd`'s `ScanServerKey`, `TrustServerKey` and `ForgetServerKey`, and `ListServers`' infos; `portenv app trust-server` for scripts that know the key. Tested against a fake ssh and against real `sshd`s through a ProxyJump alias. The app asks about each server waiting, once per launch (Trust / Not Now). Until Forget is in the app, `portenv app forget-server <name>` (owner-only) is the way past a changed key; the next confirmation says the key changed (R-0185). 1b: a key Portenv makes for a server (Ed25519, its seed in the Keychain's `dev.portenv.server-key`), handed to `portenvd` in memory at launch, signs in once per shared connection through a per-run agent of `portenvd`'s own, with `-F none` and every option explicit (ADR 0021, section 2a). Making the key from the app comes with the wizard.

1. Where is it: name, address, SSH user. Pick an existing key, or let Portenv generate a dedicated key and show the public key to paste into the provider's panel. Alternative path: Copy install command, a one-liner for the provider's web console or the server's user-data field.
2. Preflight checklist, each failure with a plain fix: supported OS (Ubuntu 22.04 and 24.04 for now; Debian when a tester asks, R-0212), CPU architecture, memory and disk, sudo, outbound access to Portenv, the registry and the snapshot storage, clock sync, KVM availability (recorded for later microVM support).
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

- Snapshot storage is the developer's choice (their server, or Portenv storage). Bringing your own S3-compatible bucket was dropped (the owner, 2026-10-14, R-0196: too technical).
- The runner receives a box key only when it starts that box, and keeps it in a root-only store (TPM-sealed where available).
- Control-plane commands are limited to box operations. An optional local-approval mode requires the owner's confirmation before anything starts.
- The runner, box agent and box format are open source so developers can audit them.

**Phasing**

Phase 2 installs the runner over plain SSH and talks to it directly from the Mac. Phase 3 adds registration and the outbound tunnel; the wizard looks the same to the user. Later: Create a server for me through provider APIs (Hetzner, DigitalOcean) and team-owned servers.

## Desktop app UX

The main window is a terminal with a title; everything else appears only when it is relevant, and almost nothing needs a button. The approved mockup is the owner's "Portenv app mockup" canvas (main window, title menu, agent presence, six first-run screens).

**Principles**

1. Deference: a thin unified toolbar, full-width terminal, sidebar hidden by default (⌘0).
2. Automate instead of asking: autosave; the workspace keeps running when its window closes, and Close Workspace or Quit saves and releases it (ADR 0023).
3. Workspace actions live in the title menu, like a document's title menu in macOS.
4. State is shown with quiet symbols (saved, saving, changes) with details on hover.
5. Undo over "Are you sure?": history makes most actions reversible.
6. Power lives in menus and keyboard shortcuts.
7. System font, SF Symbols, system materials, light and dark appearance, VoiceOver labels on every control.

**Words:** what people read says "workspace" (the app, its notifications, portenvd's messages, the docs people read); the code, APIs, proto, box IDs (`box-…`), paths, Keychain service names and log keys keep "box" (the owner, 2026-10-12; R-0112; GUIDELINES §2). The CLI's `portenv box …` group becomes `portenv workspace …` in its own change.

**Main window**

- Title: workspace name, sync symbol, and a subtitle with machine and save state ("MacBook Air · Saved 2 min ago", "Offline · will save later", "Moving to build-server…").
- Tabs: the user's tabs plus agent lanes, labelled with the agent's monogram and a watching or driving indicator.
- Toolbar items appear only when relevant: listening ports (`:3000 ↗`), agent avatars with an approval badge.
- Drag a file onto the terminal to copy it into the current folder.

**Hosted features** (the own-route guarantee, ADR 0008 G3)

- Hosted options (Portenv Cloud in Move To, Portenv storage, Account) appear as ordinary choices once they exist. Choosing one opens a sheet that explains it and offers sign-in. They are never required, never block a flow, and never show banners or nags.
- If the service is unreachable, that shows only when the user picks the option: "Can't reach Portenv right now; your workspaces keep working."
- Before a hosted feature exists, it does not appear at all: no "coming soon" items.

**Title menu** (Apple's document-menu pattern)

Rename… · Move To ▸ (this Mac, each server, Portenv Cloud once it exists, Add a Server…) · Duplicate for a Task… · Revert To ▸ (Last Save Point, shown with its time; Browse All Saves…) · Show in Finder ⌥⌘R

**Menu bar**

- File › Make Save Point ⌘S.
- Workspace › Workspace Settings… (also reachable from Settings).
- Help › Send Feedback…: a new message in the person's mail app to the feedback address, with only the app's version, build and commit, the macOS version and the chip (R-0088); with no mail app, an alert gives the address with a Copy Address button. Copy Feedback Address sits beside it for webmail, and the two replace the standard Help item until there is a help book (R-0097).
- Changes are not a menu item: clicking the sync symbol next to the title shows what changed since the last save.

**Other views**

- Changes (click the sync symbol next to the title): files new, changed or deleted since the last save; a Never saved section with sizes; include or exclude per folder.
- Browse All Saves (Revert To ▸): a timeline of every save (autosaves, save points, closes and orphaned saves) per machine; open any save read-only, compare any save with now; restore one file or everything.
- Agent popover (click an avatar): identity, where it connected from, lane and branch, a live glimpse of its terminal, pending approval, Watch, Take Over, permissions in one sentence, Revoke Access.
- Sidebar (⌘0): Workspaces, Machines, Agents.
- Settings window (⌘,): Account and devices, Protection and recovery key, Storage, Machines, Agents and permissions, Secrets, Workspace defaults and the open workspace's Workspace Settings, Notifications.

**Lease and move sheets**

- Open on another machine: Take over here, Connect to it there, Peek read-only, Cancel.
- Move To: progress shown in the title subtitle; a sheet only if something fails.
- Closing with agents connected: their sessions are saved and paused, and a notification says so. No dialog.

**First run (six screens, full version from Phase 3)**

This is the first run once the control plane exists (Phase 3). The mockups show this version.

1. Welcome: Continue with Apple, Continue with GitHub, Continue without an account (a button like the others: the own route needs no account; the remaining steps then skip sign-in and Portenv storage), and Use email instead.
2. Protection: Only you (default) or You, with Portenv's help.
3. Recovery key: Print, Copy; Continue enabled only after "I've saved my recovery key". (Save to Passwords was dropped: macOS has no public API for it; ADR 0019.)
4. Storage: Portenv storage can be preselected, but "Use my own server" and "Keep on this Mac for now" are visible on the same screen, each one click, with no account needed.
5. First workspace: name, Start from (GitHub repository, folder on this Mac, empty), detected toolbox with Change…, Show this workspace in Finder (on).
6. Getting ready: Setting up Portenv (first run only), toolbox, encrypted home, clone, start. The window opens as soon as the workspace is usable.

**First run in Phases 1 and 2 (no Portenv backend)**

Phases 0 to 2 have no Portenv backend, so the first run is reduced to five steps, in the same layout (R-0126, R-0129):

1. Welcome: "Your portable workspace." and one Continue button. No sign-in; the device key is generated locally.
2. Recovery key. There's no Protection step: Only you is fixed, so its message leads the recovery key's line: "Only you can open your workspaces’ saves: Portenv never holds your keys. This key opens them if this Mac’s keys are ever lost, on this Mac or a new one. Keep it somewhere safe and separate from this Mac."
3. Storage: My own server, or This Mac only. No Portenv storage, and no bring-your-own bucket (dropped 2026-10-14, R-0196: too technical for the people Portenv is for). This Mac only keeps the workspace's repository at `~/Library/Application Support/Portenv/Repositories/<box-id>`, outside the workspace and the engine VM (each workspace mounts only its own folder, never another's: R-0201), and the screen says plainly that saves then have no copy off this Mac. Time Machine may back the folder up; it is already encrypted by restic. After a week of use, one dismissible notice says "Your saves only exist on this Mac. Add a server to keep a copy elsewhere.", and Settings › Storage shows the same fact as a permanent status line. **In 1.5 (the design testers' build), only "Keep on this Mac for now" is offered**, with "Saves stay on this Mac only. If this Mac is lost, they're lost too. You can add a server later." My own server is shown dimmed and marked LATER until 2.9 (the bucket row was removed, R-0196) (board `first-run-4-storage-this-mac`; the reviewer, on the owner's delegation, R-0092, R-0093).
4. First workspace and 5. Getting ready: unchanged.

Every Continue either moves on or says why it can't, next to the button and to VoiceOver, on every press (R-0126: 0.1.0-68's step 2 looked like it did nothing).

In Phase 3, sign-in, Portenv storage and the recoverable mode are switched on. Existing users get a one-time prompt to create an account and link their devices; their workspaces, keys and storage stay as they are.

Unsupported Macs (Intel, or macOS before 26) get one extra screen after Welcome: use Docker Desktop or OrbStack, or run workspaces on a server and use this Mac as a client. Notification permission is requested when the first agent connects, not during first run.

**Every launch after**

Reopen the last workspace directly. Show a sheet only if needed: lease held elsewhere, engine stopped, or recovery required. Offline: the workspace runs locally and saves queue.

**Finder (File Provider)**

Each workspace appears as Documents › Portenv › \<workspace> and under Locations in the Finder sidebar. Files download on demand when the workspace runs remotely. `node_modules` and caches are hidden. The home itself stays in the workspace (no bind mount of the whole home) for speed and Linux case sensitivity. Phase 1 may ship a simpler shared folder (`/home/work/Mac`) before the File Provider.

## The terminal edition

A first-class way to use Portenv on every platform, not a stopgap. It talks to the same `portenvd` API as the Mac app, so both always agree.

- **`portenv open <box>`** puts your terminal inside the box, like ssh. Detach and the session keeps running; open again and you're back where you were.
- **`portenv open <box> --tab <name>`** attaches straight to one tab, as its own viewer, so each pane of a tiling terminal can show a different tab (1.2b).
- **`portenv attach` never drops input silently** (#53, owner 2026-10-10), on this Mac or over a slow link to a server:
  - **Input:** everything written to it reaches the box, or attach exits non-zero with one plain line saying what didn't. When stdin closes, it sends the rest, then exits 0 only once the box agent has handed every byte to the session: the agent keeps the terminal open until its program has read all its input and its output has gone quiet. A cut link, a session that ends first, a program that exits before reading all its input (`head -c 10` with more piped in), or one that stops reading for 15 s each end with a non-zero exit that says how much didn't arrive. A person typing at a terminal never sees this: attach tells the box agent whether its input is piped (`input_piped`), and only piped input is counted, whether the program ends before or after stdin does.
  - **Output** goes to stdout as it arrives, to a pipe or a file as well as a terminal.
  - **Ctrl-C:** at a terminal it is a keystroke for the box, like any other (raw mode), and a SIGINT sent to attach goes to the box too. With stdin piped, the first Ctrl-C stops reading stdin and ends attach once the box has everything read so far; a second ends it at once. Both exit non-zero, saying whether anything was left behind.
  - **Tested** over a throttled proxy at 400 kbit/s and 150 ms (`core/daemon/attach_test.go`) and against a real box (the driver suite).
- **Every menu action is a command:** status, save, revert, move, rename, restart.
- **The state line shows in the terminal title** (OSC 0/2), optionally with a one-line status bar.
- **Approvals and notifications** go through the platform's own notifications.
- **Plain `portenv`** lists boxes and commands. A small Bubble Tea picker can come later.

**Linux: the terminal edition is the first Linux release, planned after Phase 2.** It needs:
- a systemd user service instead of launchd;
- Secret Service, with an encrypted-file fallback, instead of the Keychain;
- NetworkManager instead of NWPathMonitor;
- freedesktop notifications;
- .deb and .rpm packages for arm64 and amd64.

A Linux desktop window is decided later: Tauri with xterm.js, shared with 3.10's web terminal, or native GTK.

**Public positioning stays as it is** (no "Linux" in public descriptions) until the Linux release is ready. Changing that is the owner's decision.

## Engines, dependencies and distribution

Portenv never links an engine into its code: drivers talk to engines through their APIs, and the Mac only ever installs the Portenv app.

**Box driver interface**

`Create`, `Start`, `Stop`, `Destroy`, `Exec` (bootstrap only), `Logs`, `Stats`, `SetResources`, `MountHome`, `Capabilities`. Terminals, ports and files go through the box agent, not the driver. One conformance test suite runs against every driver.

| Driver | Where | Engine | Phase |
| --- | --- | --- | --- |
| `docker` | Mac (fallback), Linux servers | Docker Engine API over its socket | 0 |
| `apple` | Mac, Apple silicon, macOS 26+ | Apple Containerization (Swift, Apache-2.0), each box in its own lightweight VM | 1 |
| `microvm` | Portenv Cloud, KVM-capable servers | Firecracker or Cloud Hypervisor, chosen by a spike; the first launch may use a rented provider behind the same driver | 5 |

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

**Exception (2026-10-12, the reviewer on the owner's delegation, R-0110):** Until the `apple` driver (1.3) ships, tester builds need a Docker engine (Docker Desktop, or OrbStack on the same socket), and say so on Welcome (R-0098).

**Minimum requirements**

Mac app: macOS 26. Local boxes need Apple silicon (the `apple` driver), or the Docker fallback. The app doesn't launch on older macOS, so older Macs can't use Portenv for now (see Open questions). Servers: Ubuntu 22.04/24.04 for now (Debian when a tester asks, R-0212), arm64 or amd64.

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
    lanes/                agent users, clones, approvals, audit
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

### Launch: Phases 1 and 2 together

The public launch ships Phases 1 and 2 together (decided 2026-10-09). Day one must demonstrate the full promise, so this demo is the launch gate. It's run end to end and recorded.

**The launch demo**
1. acme-api is open on the Mac, with Claude Code running in it.
2. Move To the server; close the laptop.
3. Grok Bot connects by SSH (a 2.4 certificate) and continues the work: it runs the tests and fixes a failure.
4. Dots (ChatGPT) connects through remote MCP served by the runner on the user's server (2.7), and makes a change.
5. Meta Muse opens the box through the web terminal served by the runner (2.8), and makes a change.
6. Open the laptop. The window reconnects to the box, still on the server, and split view (1.2b) shows Claude Code, Grok Bot and the tests side by side, all still working.
7. Claude Code asks a question in the box. The agent the user is talking to relays it in its chat; the user answers by talking; the answer goes back as keystrokes, attributed to the user ("you, through Grok Bot").
8. Move back to the Mac. History shows who did what, and Revert works.

**What the demo needs**
- **Remote MCP over HTTPS with OAuth (needed by ChatGPT) and the web terminal,** served by the runner on the user's own server, free (2.7, 2.8). The user opts in per server: an inbound 443 under ADR 0015's conditions, or no open port at all through the user's own Cloudflare Tunnel or Tailscale Funnel. Phase 3 keeps the Portenv-hosted versions (3.9, 3.10) for users without a public server, and stays the paid line.
- **The relay loop is launch-critical:**
  - waiting-for-input detection (1.2's spike, or its fallback detector);
  - webhooks (2.5);
  - an answer tool in the CLI and in MCP, `portenv answer`.
- **Security, before anything public:**
  - append-only storage first (2.2);
  - per-agent sign-in;
  - one-time, short-lived web-terminal links;
  - per-agent attribution and revocation;
  - everything through the box agent;
  - the vault rules (ADR 0005);
  - the public doors' own conditions (ADR 0015).
- **Availability:** Meta Muse is US-only, and Dots isn't on ChatGPT Pro in the EEA. The recording needs accounts where each is available.

**Launch checklist**
- [ ] The launch demo runs end to end, recorded, on a clean Mac and a fresh server
- [ ] An outside security review of the public doors (remote MCP, the web terminal, SSH with certificates), with every finding fixed or accepted in writing
- [ ] The public endpoints are fuzzed in CI before launch
- [ ] The app is signed and notarized (1.8), and the website's download link is turned on only then
- [ ] Link GitHub works from New box (device flow inside the box, gh's normal login, Sign Out of GitHub in the inspector)

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

- [x] The setup script installs Docker and an encrypted volume for box homes on Ubuntu 24.04
- [x] `ssh portenv` lands in the box's tmux session (checked through a real terminal on 2026-10-08; the gate checks it from now on)
- [x] After a take over, the next resume on the machine that had the box tells the user that its unsaved work was kept as a separate save, with the save's time; `portenv status` and `portenv history` show it too (the app does the same from Phase 1)
- [x] The Phase 0 gate below passes (final round 2026-10-08; numbers in docs/milestones/0.5.md)

**Phase 0 gate**

- [x] Mac → server → Mac round trip loses nothing (checksums of `/home` match)
- [x] All five resume rules and every invariant have passing tests, including two simulated machines
- [x] Resume: under 5 seconds to a ready box (a ready terminal from Phase 1), including starting it, with storage reachable at a round-trip time of 50 ms or less and with storage unreachable (offline); the median of 5 runs, with the worst run reported too. Measured: 3.1 s online (worst 3.23 s), 2.22 s offline (worst 2.34 s), at 40 ms
- [x] Close and Move: the final save and release of a box with a 5 MB unsaved change take under 15 seconds at 20 Mbit/s up and a 50 ms round trip, reported with the measured bandwidth. Measured: 8.52 s at 20.05 Mbit/s
- [x] Freshness: with continuous editing and autosave every 30 seconds, the newest save is never more than 60 seconds behind. Measured: at most 41.1 s
- [x] The gate measures bandwidth and round-trip time, prints them next to each result, and prints the per-phase trace of every measured command

Budgets changed on 2026-10-08: they now measure what the user waits for (a ready box, a finished close or move, how much work is at risk) instead of save overhead, which users never see on its own.
- [x] Killing the process mid-save leaves the repository consistent and the next save succeeds
- [x] The toolbox image builds for arm64 and amd64 from one Dockerfile

**Phase 0 closed (2026-10-09).** Every item above passed. Known gap, carried forward: online resume is under 5 s only up to a 50 ms round trip (3.1 s measured at 40 ms), because before Phase 3 the lease check is a snapshot listing, a few round trips; over slower links it takes longer. Phase 3's authoritative leases replace the listing with one control-plane call, which closes it.

### Phase 1: Native Mac app, local boxes

Milestones: 1.0 walking skeleton · 1.1 `portenvd` with the local gRPC API · 1.2 main window with a terminal view (evaluate SwiftTerm, MIT-licensed) and tmux-backed tabs · 1.2b split view (after 1.2's blocks, before the launch demo) · 1.3 `apple` driver shim, `docker` as fallback (the box agent drops the forbidden capabilities from every process it starts, since a VM's root holds them by default) · 1.4 autosave, sync symbol, Changes, Browse Saves · 1.5 first run (reduced, no sign-in; see Desktop app UX), Keychain keys, recovery key · 1.6 port relay · 1.7 shared folder (only if cheap; the File Provider moves to just after launch) · 1.8 signing, notarization, Sparkle updates.

**For the launch** (Phases 1 and 2 ship together, decided 2026-10-09):
- 1.2's spike moves to right after the tab bar, with a fallback waiting-for-input detector (output stops and a known prompt pattern appears) if the spike fails.
- 1.2b (split view) lands after 1.2's blocks and before the launch demo, which uses it.
- **1.5 (first run) comes before 1.3 and 1.4** (the owner's decision, 2026-10-10). It's built alongside the rest of 1.2, in parallel lanes, so a new user can get from the download to a box without Terminal before the engine and autosave work.
- 1.4 designs attribution into saves (who was active: the user, or which agent), so history can show who did what.
- The command registry (Agent readiness) starts in 1.x, because `portenv answer` and the MCP tools build on it.
- 1.6 (port relay) stays before launch.
- 1.8 (signing) must land before launch.

**Phase 1 plan** (approved 2026-10-09; the order of work below is what's built next, item by item)

**Where Phase 1 stands**

**Done in 1.0 and 1.0b, and in the PRs since (#25 to #28 still waiting to merge):**
- **Through the real path:** a minimal `portenvd` and runner, the agent channel (ADR 0010), and the app's terminal in the box's tmux session.
- **Box actions:** save point, Revert To, and Move To a server and back, the same on a server box as on the Mac.
- **Save state:** an honest state line with the full table of states (#26, §3.1), retrying and not-saved states (#23), and "quit before saving" (#25).
- **Quitting:** never fails silently (the alert and the marker, #25); the channel is checked after wake (#25); boxes still opening are waited for (#25).
- **Deadlines:** on every restic run and every process on the daemon side, enforced in CI (#23).
- **Offline open:** near 2 s (ADR 0011).
- **The guideline basics (#26):** title and Workspace menus in the guideline order, servers by name, tmux hidden, VoiceOver labels.
- **Packages:** a package that can't be installed never stops a box (#28; the inspector line waits for the inspector).
- **The CLI built in CI, fetched by commit (#27);** the test-path rule and mutation checks (#20).

**From the demo notes:**
- servers shown by name: done (#26, by host name until servers can be named);
- the subtitle after a revert: done (#26);
- a checkmark on the current location: done (1.0b, kept in #26);
- **visible progress during a move: not done** (the inspector's four steps, 1.2 and 1.4).

**Still on Phase 1's gate (from `PLAN.md`):** first run, a signed and notarized download with updates, offline work, the throttled-uplink Move, no docker exec anywhere, VoiceOver and both appearances, the port relay, and the restic probe in an `apple` box.

**Order of work**

**1. Keychain calls never wait silently (first, before 1.1)**

Found closing `retest`: a new dev build's first Keychain read waited 14½ minutes behind a prompt while the window said only "Opening…".

- **a. The state line says so.**
  - If a Keychain call hasn't returned after 2 s, the state line reads "Waiting for Keychain access", with a secondary line: "Check for a password prompt. It may be behind other windows."
  - A new row goes in the state-line table in `GUIDELINES.md` §3.1.
  - A notification goes out too, for when the window isn't in front.
- **b. Never on the main thread.** Every Keychain call runs off the main thread, so the window stays responsive and Cancel works while the prompt is up. Cancel stops the open cleanly: no half-open box, no held lease.
- **c. No timeout while a person might be answering.** In the app, the call doesn't fail while the prompt could still be answered. The state line shows, and the person can cancel.
- **d. Non-interactive paths fail fast.** The CLI without a TTY, tests, and anything an agent or overnight run triggers use `kSecUseAuthenticationUI = Fail` (or the equivalent), so the call returns `errSecInteractionNotAllowed` at once instead of prompting. The error is one plain line: "Keychain needs your approval. Open Portenv on this Mac to allow it." A test proves the no-prompt path returns it.
- **e. Root cause, confirmed first:**
  - check whether the prompt comes from the dev build's ad-hoc signature changing on every rebuild, so "Always Allow" no longer matches;
  - if so, an ADR (or a PLAN note) says the fix is a stable signing identity once Apple Developer enrolment lands;
  - until then, test boxes on this Mac are expected to prompt after a rebuild, and a–d make that visible.
  - Confirmed, and recorded in ADR 0013. The fail-fast path (d) also needs `SecKeychainSetUserInteractionAllowed(false)`: `kSecUseAuthenticationUI = Fail` alone doesn't stop the prompt for these legacy keychain items.
    - That call is deprecated and turns prompts off for the whole process, so it runs only on the daemon side (`core/keys`), never in the app, where prompts must work.
    - Tests check this:
      - a process that may prompt never turns it off;
      - no Swift code names it;
      - `make app` fails if the app imports it.
    - The switch goes away when the stable signing identity lands (1.8): keys move to the data-protection keychain, where `kSecUseAuthenticationUI = Fail` is enough.
- **f. Overnight runs:** Keychain calls follow the same rule as restic runs. Every call has an owner who answers it or gets a clear error, never a silent wait.
- **g. Only the app reads the Keychain; no background process ever causes a prompt.**
  - Once a login item starts `portenvd` (1.1), it has no window, so a prompt it raised would have nothing to point at.
  - `portenvd`, the runner and the CLI without a TTY always use the fail-fast path (d). When the daemon needs a key, it asks the app over its API ("key needed for acme-api"). The app reads the Keychain in the foreground, with a–c, and hands over only the key, which stays in memory.
  - With no app running, the daemon reports "Keychain needs your approval. Open Portenv on this Mac to allow it." and the box waits.
  - **When `portenvd` restarts while the app is open** (crash, update, login item relaunch), the keys it held are gone. The app handles it:
    - it notices `portenvd` isn't answering and starts it again;
    - it reopens the box once, sending the keys again; the new `portenvd` reports a box the old one left open (its lease still held) as interrupted, without needing a key;
    - until then the state line shows the box's real state ("Not saved since 14:58"), never a frozen line, "is closed" or a prompt the person didn't cause;
    - a reopen that fails isn't repeated until the next restart.
  - Test: the daemon's key store refuses interactive calls (the fail-fast flag is set on every daemon-side call). A daemon that needs a key with no app connected returns that error and raises no prompt (checked with a fake key store that records the flags it was called with).

Tests:
- the daemon side never calls the Keychain interactively (g);
- the 2 s state row, on a fake key store that blocks;
- Cancel during a blocked call leaves no box or lease (an e2e with a blocking fake);
- the no-prompt error (d);
- the call runs off the main thread (a main-thread assertion in the key store).

**2. A small visible PR: terminal margin and hostname (before 1.1)**

Small and visible, so they go before 1.1, which is big:
- **Terminal margin:** the text touches the window's left edge. Add about 8 pt and write the value into `GUIDELINES.md` §4.5.
- **The box's hostname is its name.** The prompt reads `work@acme-api`, not `work@portenv`, so people and agents can tell which box they're in. The name is sanitised to a valid hostname (lowercase letters, digits and hyphens, at most 63 characters, no leading or trailing hyphen), falling back to `box-<first 8 of the box id>`. The original name shows everywhere else: the title, the inspector, Move To and notifications.
- Light and dark screenshots next to the main-window mockup, and a test of the sanitising.

**3. 1.1: `portenvd`'s API in the app, and a login item**

- The app talks to `portenvd` over gRPC on its socket instead of running the `portenv` helper for each action.
  - That removes the "main-actor work doesn't run during the quit wait" workaround.
  - It also gives push updates for the state line instead of the 3 s poll.
- **A login item starts `portenvd`, and only launchd does.**
  - `SMAppService` registers `portenvd` as a launch agent with KeepAlive. It never reads the Keychain itself (item 1g).
  - The app's own start path is removed entirely, so `portenvd` can never be the app's child.
  - **If a relaunch doesn't finish,** launchd brings `portenvd` back. While the app waits, it shows "Not saved since …", and after a deadline a clear error.
  - **⌘Q is unchanged:** it closes, saves and releases the open box (as tested and ticked). Only an update relaunch leaves boxes running (ADR 0014).
  - **Tests:**
    - `kill -9 portenvd` with the app closed, and launchd restarts it;
    - an update-style relaunch leaves the box and its programs running.
- **The wait before the terminal is usable on open** (about 1.6 s of polling for the agent today) goes: push updates replace the poll, and the terminal attaches the moment the agent reports ready.
- **The Phase 0 CLI's `docker exec` path is removed** (ADR 0010 condition 4, and the gate). `portenv init`, and the server-side `join` and `status` used by Move To enrolment, move into the daemon API or stay as plain commands that never touch a box. The audit lists them.
- **A new `portenvd` takes over running boxes; it doesn't restart them** (required in 1.1, before Sparkle updates in 1.8).
  - **Why:** today a `portenvd` restart (crash, update, login item relaunch) restarts each open box, because the new daemon doesn't have the box agent's channel secrets. Programs in the box's terminal end. That's acceptable for a rare crash, but from 1.8 Sparkle restarts `portenvd` at every update, and every update would kill running dev servers and agents' long tasks.
  - **The fix:** the new daemon gets the running box's channel without restarting the box. Either the app (on servers, the runner) hands the channel secrets to the new daemon, or the channel is re-keyed in place. The box keeps running, and its tmux sessions survive.
  - **Test:** `kill -9 portenvd` while a long command runs in the terminal. Afterwards the command is still running, and the state line recovers.
  - **Until then,** the current behaviour stays (item 1g): the line shows "Not saved since …", and the app restarts `portenvd` and reopens the box once.

**4. 1.2: the main window**

Every UI PR in 1.2 comes with light and dark screenshots next to the matching mockup board, a VoiceOver label for each new control, and each check mutation-checked.

In this order:

1. **Rename… for boxes** (§3.2). It updates the title, the inspector and the hostname. If the hostname can only change on the box's next start, the rename sheet says so ("The workspace's hostname changes the next time it starts.").
2. **The tab bar (early, §3 and §4.1), built before the spike** on a tab interface in the box agent: list, new, close and rename, plus events. Whether the agent uses tmux control mode or plain tmux commands stays inside the agent, so the tab bar doesn't change after the spike.
   - One tab per tmux window; `+` for a new tab; the app owns names and order.
   - Until it exists, nothing (`portenv box send`, agents) can create a tmux window the user can't see. The agent refuses `new-window` from anything but the app, or maps it to the single visible window.
   - **Built (2026-10-10, awaiting review):**
     - **In the box agent:** ListTabs, NewTab, CloseTab, RenameTab, SelectTab and WatchTabs, on plain tmux commands, polled for the watch.
       - Tab IDs are tmux window IDs, confined to the box's session.
       - The app owns the names: tmux's automatic rename and rename by escape sequence stay off even when the user's tmux config turns them on.
       - A new session starts with one tab named "shell", and the last tab can't be closed.
       - Every window in the session is a tab, whoever created it, so a window a program opens itself appears in the tab bar (the "maps it" option above).
       - **Each viewer keeps its own current tab** (owner, 2026-10-10). Each viewer (the app, later each agent) gets one grouped tmux session over the box's windows (`tmux new-session -t <base>`). Its terminal attaches there, and the agent ends that session once no client is left on it.
       - **Each tab reports its foreground program,** or nothing when only its shell runs.
     - **portenvd** relays every call, the watch and the viewer, on this Mac or on a server; `portenv attach` takes `--viewer`.
     - **The app's tab bar:**
       - 30 pt, tabs sharing the width;
       - `+` names tabs "shell", "shell 2" and so on;
       - close on hover, and never on the last tab;
       - rename by double-click or the tab's menu;
       - semantic colours, and VoiceOver labels and actions for every control.
       - **The app is the "app" viewer.**
     - **Shortcuts** (owner, 2026-10-10; GUIDELINES.md §4.1):
       - ⌘T for a new tab, ⌘1–⌘9 to choose one, ⌘⇧[ / ⌘⇧] for the previous / next tab;
       - ⌘W closes the current tab, and on the last tab closes the window (save and release);
       - closing a tab that still runs a program asks "Close this tab? <program> is still running.", with the program from the box agent at that moment.
     - **Not yet:**
       - reordering (later, by drag; Open questions);
       - before merge, with the owner at the Mac: a screenshot of the running app and a VoiceOver check. The PR's light and dark images are offscreen renders of the tab bar view.
3. **The inspector (§6):**
   - **Where it is**, including the move's four-step progress (the demo note), the packages line from #28, and Retry;
   - **Saves** (the latest five);
   - **Who's here** (you; stand-in agents arrive in 2.4);
   - **Running now** (tabs, ports).
   - **Status (lane B's PR, 2026-10-10):**
     - **Built:**
       - ⌥⌘I, the toolbar button and View › Show Inspector, open state remembered per box;
       - Where it is: "Running on", "Since", and "Saves go to <place>" from portenvd's new `storage_place` (never a host name or IP: "your server" or "your bucket" until servers have names in 2.3) and `opened_at`; the packages line with Retry; Move To…;
       - Saves: the latest five, with Revert on the newest save point;
       - Who's here: you, and "No agents connected.";
       - Running now: each tab and what runs in it.
     - **Not yet, waiting on data the daemon doesn't have:**
       - the move's four-step progress: a progress stream from MoveBox (1.4); during a move the section says where it's going and what still works;
       - "moved here from <place>";
       - ports (1.6's port relay) and saved commands;
       - Revert on saves other than the newest save point, and Browse All Saves… (1.4);
       - save point names: the row shows a name on a second line, but portenvd doesn't record names yet;
       - a server box's saves, which portenvd lists on the server.
     - **Review follow-ups (lane B's second PR, 2026-10-11):**
       - one rule for "Saved …": the state line and the inspector's "Last saved" both come from the box state's saved-at time, "just now" for the first minute and then the time (§3.1), and both redraw when the minute is up (the title once still said "Saved just now" two minutes after the save);
       - Move To… with nowhere to go says why: "Add a server to move this workspace.", under it and as its help tag;
       - saves kept on this Mac read "Encrypted on this Mac"; "before they leave it" stays for a server, a bucket or a drive;
       - saves that can't be read show "Couldn't load saves." with Try Again, and the app keeps trying quietly, instead of "Loading saves…" forever;
       - a box that moves back to this Mac shows "Loading saves…" until portenvd answers, never "No saves yet." from before.
4. **Notifications (§5)** for a finished long command, a move over 10 s, and the Keychain wait from item 1. Answer buttons come with the inline answer card.
   - The app asks for permission the first time something is worth notifying, never at launch. If permission is denied, the state line and the inspector still show everything, and nothing nags.
   - **Status (lane B's PR, 2026-10-10):** built, only when the window isn't in front.
     - A command finished: a tab's foreground program ending after more than 30 s, read from the tab updates. Once the spike's OSC 133 marks land, they replace this signal, with the exit code.
     - A move over 10 s.
     - The Keychain wait, which already existed.
5. **The spike: tmux control mode (`-CC`) and OSC 133 (§13), time-boxed to 3 days.** The outcome goes in an ADR either way. **For the launch, it runs right after the tab bar (item 2), before the inspector and notifications.** Waiting-for-input detection is launch-critical: if the spike fails, a fallback detector in the box agent (output stops and a known prompt pattern appears) still lands in Phase 1.
   - It answers: can the agent map app tabs to tmux windows through control mode? Can shell integration in the skeleton home emit OSC 133 marks that the agent tracks per tab, with exit codes, for bash and zsh, including inside Claude Code? Do SwiftTerm overlays stay aligned to rows when scrolling?
   - It's also tested over the SSH forward to a box on a server, not only on the Mac (a test, not another milestone).
   - **If it succeeds,** Phase 1 also gets blocks with attribution, the sticky header, the inline answer card for Claude Code, tab badges, the command palette and saved commands, in that order.
   - **If it doesn't,** they move to Phase 2 and the plan says so.
   - **Result (ADR 0017, 2026-10-10; proposed):**
     - **Question 1, control mode:** yes. Tabs follow tmux through control mode, with #41's window IDs.
     - **Question 2, OSC 133 per tab:** yes for bash and zsh, with exit codes. Inside Claude Code it only sees one long command, never its questions.
     - **Waiting for input:** the combined four-signal detector, used everywhere, is right on all 12 labelled cases. Each signal alone is wrong on 1 to 5 of them. Secret prompts never reach a relay, and answers to them through a relay are refused.
     - **Question 3, SwiftTerm overlay alignment (ADR 0020, 2026-10-13; proposed):** yes, when the terminal is fed the tab's own output.
       - **How it was measured:** real tmux 3.4 streams for six scenarios (scrolling, long output, a pager, clear, a second tab, resize) were replayed into SwiftTerm with the overlay tracker.
       - **Today's attached client keeps no mark.** tmux drops OSC 133 on the way to an attached client, and even with passthrough, the client draws on the alternate screen with no scrollback.
       - **Without the alternate screen,** 51 of 54 prompt marks land on the wrong line.
       - **Each tab's own output from tmux control mode:** 0 of 94 on the wrong line, 0 prompts missing, and 0 wrong at any scroll position.
       - A prototype overlay on a real SwiftTerm view stays on its rows through scrolling and a resize.
     - **So the app's terminal moves to tmux control mode, one terminal per tab (ADR 0020),** and blocks stay in Phase 1, in this order:
       1. **1.2c, the control-mode milestone:**
          - the box agent's control client per session (tab events replace the polling);
            - **Built (PR 1, 2026-10-13, awaiting review):**
              - one `tmux -C` client per session, shared, started with the first subscriber and ended with the last;
              - started again if it exits, with subscribers told to list afresh;
              - tmux's `set-clipboard` and `allow-passthrough` set off at every start;
              - WatchTabs follows its events (window added, closed, renamed, another session's current window, output at most once a second), with a 2 s backstop for program changes that print nothing, and polling only below tmux 3.4;
              - the `%output`/`%extended-output` decoder and a line reader capped at 1 MB per line, each with its own fuzz target, seeded from the spike's tmux 3.4 streams (`make fuzz`, in CI on a fixed number of runs, not a duration, which failed now and then on a slow runner);
              - tmux pinned to 3.4 in the image and checked by `test.sh`; the helper refuses older for control mode.
            - **Built (PR 2, 2026-10-14, awaiting review):** the helper's per-pane parser, `charmbracelet/x/vt` v0.1.0 on R-0142's terms. Not yet wired to a stream; PR 3 does that.
              - **Pinned:** exact, with `go mod verify` in lint. The notices tool passes with its 16 MIT and BSD modules.
              - **Read-only:** replies are drained and dropped, so a query never blocks it.
              - **Panics:** each pane's parser runs under `recover`, resets that pane only, and logs once.
              - **Bounded:** the size is capped and the scrollback emptied after every chunk.
              - **Fuzzed:** `FuzzPaneParser`.
              - **Line counts:** each mark's line is counted from the pane's own output. A test checks against tmux 3.4 through scrolling, long output, the alternate screen and identical prompts.
              - **Epochs:** a full-screen erase or a width change starts an epoch. Across one, tmux's count can differ: it counts written spaces on an erase, and reflows on a width change. The seed replays only the current epoch's marks (PR 3).
              - **Session gone:** the control client stops when its session is gone (R-0167), instead of restarting every 5 s.
          - the `TabTerminal` stream in the agent and portenvd;
            - **Built (PR 3, 2026-10-14, awaiting review):** `TabTerminal` in the helper (ADR 0020, decision 6, "As built").
              - **The reset:** history, screen, alternate screen, modes, pen and cursor, with current-epoch marks and their authors on their lines. It finishes any escape or UTF-8 character the output left unfinished.
              - **Then:** output, input and resizes until the tab closes.
              - **Falling behind:** a new reset marked `skipped`.
              - **Tests:** on real tmux, for nothing lost or repeated across ten reopenings, 64 KB of every byte value arriving unchanged from copy mode, authors, resizes taking turns with another client, a gone tab, falling behind, and resets mid-sequence. The driver test checks that `work` can't open a tab terminal over the channel or the local socket (R-0175). Another test checks that the parser's drain goroutine ends (R-0176).
              - **Known limitation:** marks from before a full-screen erase or a width change aren't replayed.
            - **Built (the flood PR, 2026-10-14, awaiting review; R-0178):** the tmux reader never waits on a parser or a stream.
              - **Lanes:** each pane's output goes through its own queue, capped at 4 MB. Past the cap, that pane's waiting output is dropped and its streams reseed with `skipped`.
              - **Its own pane only:** a tab terminal takes only its pane's output.
              - **Tests:** a 200,000-line flood under `-race`, another tab staying live, no client restart, and resets clearing the pen and bracketed paste.
            - **Built in portenvd (PR 4, 2026-10-14, awaiting review):** `DaemonService.TabTerminal` relays to the helper's, on this Mac or over a server's forward.
              - **Who's typing:** from the connection's caller, fixed at accept (`caller.go`); the app's open has no author field (4 is reserved), and nothing in a request can set one.
              - **Scope:** a caller limited to one workspace is refused any other before a helper is contacted.
              - **Box ID:** the helper must answer `GetVersion` as the box it was reached for; another box or none is refused before anything is relayed.
              - **Authors:** the helper is told the owner's first name, or an agent's grant name and ID. `is_you` is set by portenvd for each recipient. An author with a name over 64 characters, or with control or bidi characters, is dropped (R-0178).
              - **`skipped`** on a reset reaches the app.
          - the app's per-tab terminal (`TabTerminalSource`, `MarkedTerminalView`) in place of today's attached terminal;
            - **Built (PR 5, 2026-10-13, awaiting review):** the tab view's guards, ahead of the live provider:
              - the default-deny filter in front of what it's fed (`TerminalFilter`, ADR 0020 decision 9), with a table test per rule and fuzzing over random bytes and the spike's mutated tmux streams;
              - paste cleaned of `ESC[201~` and controls before it's bracketed;
              - a ⌘-clicked link through `TerminalLinks`, as in the attached terminal (R-0161);
              - a delegate nothing can replace: checked before every byte is fed, and put back (R-0161);
              - scrollback capped at 10,000 lines.
              - **R-0167's changes:**
                - OSC payloads are printable ASCII only;
                - counts are clamped (4,096, or 65,535 for other parameters);
                - text is strict UTF-8;
                - a reset clears the filter too;
                - dropped text and Services are refused;
                - the lint fails on any delegate assignment outside the factory and the tab view, and the runtime check also runs in the relay's paths.
            - **Built (PR 6, 2026-10-14, awaiting review):** the main window shows the current tab's own terminal through portenvd's `TabTerminal`.
              - **Losing the stream:** "Reconnecting…" while it comes back, then "Disconnected" with Reconnect.
              - **The dev-only switch:** `gmake app LANE=X ATTACHED_TERMINAL=1` brings back the attached terminal. Each of its four release guards has a test that fails without it.
              - **Also in the helper:** a pending Enter's author ends at the next prompt (R-0177). Failed replies are never read as data, so a failed pane list ends no lane (R-0180).
          - **the latency gate:** keystroke to echo no worse than the attached client by more than 5 ms at p95 locally (within ±0.25 ms at tmux, measured), and the same run on the us-west-2 tester server once it exists. Until it passes, a dev-only switch back to the attached client, which never reaches a release build;
          - tmux pinned in the image (3.4) and checked at attach;
          - #73's and #75's no-loss guarantees carried over, with tests.
          - R-0133's nine changes:
            - no emulator reply reaches the pane;
            - a default-deny filter in front of the terminal, fuzzed;
            - paste cleaned;
            - authors set by portenvd;
            - the seed in one command list;
            - mark lines from the helper's own parse;
            - "every byte or a reset, and the person is told";
            - the `%output` decoder and copy mode;
            - CLI and app resizing in turn.

          `portenv attach` stays a plain attach, and agents' terminal access is unchanged (R-0132).
       2. **Waiting-for-input detection,** from the same control client (ADR 0017's detector).
       3. **Command blocks:** attribution, the sticky header, the inline answer card, then tab badges; the command palette and saved commands after.
     - **tmux's passthrough stays off.** The app's per-tab view filters what a program prints (no clipboard, link or title effects), and a test checks both.
     - **Still to do:** the same cases over the SSH forward to a server box (needs the owner's go-ahead for the AWS test server), and real agent CLIs in place of the stand-ins (needs accounts).

**Terminal colours (the owner, 2026-10-11).** The terminal follows the system appearance by default ("Match System"), like Terminal.app's Basic profile:
- the system's text background and text colours (`NSColor.textBackgroundColor`, `textColor`), changing with light and dark mode, the margin included;
- one ANSI palette for light and one for dark; the light one uses darker greens, yellows and cyans, and every normal colour is at least 4.5:1 on its background (a test, against the system's own background);
- the caret and the selection follow the system accent.
- **Later:** View › Terminal Appearance (other themes). Built in lane B's second PR.

**4b. 1.2b: split view** (the owner, 2026-10-10; reference: docs/mockups/split-view.png)

It comes after 1.2's blocks, wherever they land (Phase 1 if the spike succeeds, otherwise Phase 2), and before the launch demo. The panes themselves don't depend on blocks; the Warp-style features below apply per pane once they exist.

- **Panes:** up to 4 in one window.
  - Each pane shows one of the box's tabs through its own tab terminal (ADR 0020's `TabTerminalSource` and `MarkedTerminalView`). Lane B builds the panes on that interface with `ScriptedTabSource` until 1.2c's control-mode provider lands.
  - So two panes can't fight over a current tab, and an agent typing in one tab never moves your view. A tab shows in at most one pane.
  - A tab has one size, the latest asked for. When another viewer (the CLI, an agent) has it larger, the pane keeps the cursor's row in sight.
  - The pane header's status (asking, typing, watching, quiet since, running) comes from the tab events, not the terminal stream (ADR 0020, `TabInfo.activity`).
- **Controls:**
  - "Split right" (⌘D) and "Split down" (⇧⌘D), as buttons in the title bar;
  - ⌘⌥ and the arrow keys move between panes;
  - ⌘W closes the focused pane first, and closes tabs only when one pane is left;
  - closing a pane never stops its tab.
- **The pane header** is slim:
  - a tab picker (the tab's name with a chevron);
  - the tab's status ("asking you", "watching", "quiet since 15:41");
  - who is active ("Grok Bot is typing");
  - a close button.
- **Focus and accessibility:**
  - the focused pane has a 1 px accent outline;
  - VoiceOver reads each pane as "Pane: grok tab, Grok Bot is typing".
- **The tab bar:** a small icon shows which pane each tab is in. Tabs not in any pane show plainly.
- **Warp-style features work per pane:** blocks, the sticky command header, inline answers, ⌘K, jumping to the start of a command, and saved commands.
  - Pane-level actions apply to the focused pane.
  - In a narrow pane, block headers shorten instead of wrapping: the command is truncated first, and the exit status and author avatar always stay.
- **Inline answers:**
  - an answer card shows in whichever pane shows that tab;
  - if no pane shows it, the tab gets a badge and the usual notification;
  - answering anywhere (any pane, the terminal, or chat) clears it everywhere.
- **The terminal edition:** `portenv open <box> --tab <name>` attaches straight to one tab.
  - The panes of a tiling terminal (Ghostty, kitty, tmux, Hyprland) can each show a different tab, with the same per-viewer independence.
  - The in-shell features come from shell integration, so they work in every pane there too.
- **The launch demo** uses split view at the moment the laptop reopens: Claude Code, Grok Bot and the tests side by side, all still working on the server.

**First version (the owner, 2026-10-18: split view ships tonight; demo note `docs/milestones/1.2b.md`).**

- [x] Up to 4 panes in one window, each its own `MainTabTerminal` on one of the box's tabs (ADR 0020), with its own Reconnecting and Disconnected. A tab shows in at most one pane; an agent typing in one tab never moves a pane.
- [x] Split Right (⌘D) and Split Down (⇧⌘D), as title-bar buttons and Window menu commands. A new pane shows the first tab the tab bar shows (so an agent's, with that agent selected) that no pane shows; when every one is in a pane, a new shell tab, owned by you, as `+` makes.
- [x] ⌘⌥ and the arrows move the focus between panes (also in the Window menu); Close Pane in the Window menu.
- [x] ⌘W closes the focused pane first, and closes tabs only when one pane is left. Closing a pane never stops its tab.
- [x] The slim pane header: the tab picker (the tab's name with a chevron, listing the tabs no other pane shows), the program running in it, and a close button.
- [x] The focused pane's 1 px accent outline; keystrokes go to it. VoiceOver reads each pane as "Pane: grok tab".
- [x] The tab bar: choosing a tab (a click, ⌘1–9, ⌃Tab, ⌘⇧]) shows it in the focused pane, or focuses the pane already showing it; the current tab is always the focused pane's. A small glyph marks the tabs in a pane, showing where.
- [x] Each pane asks its own tab for its own size; a tab has one size, the latest asked for.
- [x] Works with the Team sidebar and agent columns: selecting an agent filters the tab bar and shows that agent's tab in the focused pane, so the focused pane's tab is always the bar's current tab (R-0314); the other panes stay as they are.
- [x] Tests: `SplitControllerTests`, `SplitLayoutTests`, `SplitPanesLayoutTests` (four panes fit the window's minimum).

**Still to do after the first version:**

- [ ] The header's status ("asking you", "watching", "quiet since 15:41") and who is active ("Grok Bot is typing"), and VoiceOver's status after the tab name: the tab list doesn't carry `waiting`, `typingAuthor`, `watchers` or `lastOutputAt` from portenvd yet, so the header shows only the running program. `PaneHeader` already writes the words.
- [ ] The split layout remembered per workspace; today each open starts with one pane.
- [ ] Drag to rearrange panes.
- [ ] "Opened by <agent> · <command>" for a pane an agent opened (GUIDELINES 6.2).
- [ ] The Warp-style features per pane beyond what each terminal already does, inline answers per pane, and narrow-pane block headers.
- [ ] When another viewer has a tab larger, the pane keeps the cursor's row in sight.
- [ ] Renders of the board redone from the shipped panes (`docs/screenshots/split-view/` is from the draft).

**5. 1.3: the `apple` driver**

The Containerization shim, with `docker` as the fallback. The agent drops the forbidden capabilities itself (a VM's root holds them). Gate item: the restic probe passes inside an `apple` box.

**6. 1.4: autosave, Changes, Browse Saves**

- **Autosave, tied to the targets, with one test per target:**
  - every 30 s while the home is dirty;
  - no change older than 60 s left unsaved while online;
  - close under 15 s;
  - reopen under 5 s, online at a round-trip time of 50 ms or less and offline.
- **Open timing:** when these budget tests land, measure opening a box over at least 10 runs and report the median and the worst (1.1's single-run figures were indicative only).

  It runs through the bounded runner. A failed autosave retries on its own schedule (ADR 0012), so the "Not saved since · retrying" row becomes routine.
- **Browse Saves**, which provides the time for **Revert To ▸ Last Save Point (#26 follow-up)**: `portenvd` keeps the save list from its last listing, so the menu shows the time without a new listing.
- **Move progress:** the bytes still to send and the measured bandwidth give the time estimate (the throttled-uplink gate item).
- **`node_modules` after a move:** a post-resume hook runs the lockfile's install when the CPU architecture changed since the last install, or the lockfile differs from the one last installed. The install is recorded in the box, so it moves with it.
- **Test gaps from the audit, now on the real path:** lease refusal, take-over and rule 4 with kept work, and autosave freshness through `portenvd`. The server gate's timing limits and LUKS checks move onto the runner path.

**7. 1.5: first run**

Reduced: no sign-in, Only you, Keychain keys, the recovery key; the Welcome screen has one "Continue" (R-0126; there's no account to be had). First run lists, in plain words, the exact fields the update check counts (no ID, nothing about the user's work), and how to turn counting off (1.8).
- **No window for a box that doesn't exist** (found at the Mac, 2026-10-10). Opened without `PORTENV_BOX`, the app shows a window for a default box "demo" that doesn't exist, with "no box named demo here; create it with portenv init demo". That message starts in lower case and sends the user to a terminal command (GUIDELINES §10). First run replaces it: with no box, the app shows first run or the box list, never a window for a missing box.
- **The box list and New box: build to the boards** `box-list`, `box-list-empty`, `new-box` and `new-box-linked-invalid` in `docs/mockups/` (the owner, 2026-10-10).
  - **First run** keeps "Make your first workspace", with step dots.
  - **The box list's New Workspace…** opens "New workspace", with Back (‹ Your workspaces) and Cancel, and no step dots.
  - **The name field** has a hint ("Letters, numbers and hyphens, like the project's name."), and an inline error for an invalid name ("Start with a letter or a number.").
  - **Opening a box that's on a server** connects to it there; nothing moves unless the person chooses Move To.
  - **While the name is invalid** (R-0049): the field has a red border in place of the blue focus ring, and VoiceOver reads the problem as part of the field's description ("Workspace name. Start with a letter or a number."); Create Workspace is dimmed as well as disabled.
- **Closing the last workspace window goes back to "Your workspaces", and the workspace keeps running** (ADR 0023, replacing the owner's 2026-10-11 rule). ⌘Q still quits.
  - Nothing is saved, released or stopped. The list shows it as "Running", with Open and Close Workspace.
  - Close Workspace saves and releases it. If that fails, the app asks "acme-api couldn't be saved before closing. Its work is still on this Mac.", with Try Again, Close Anyway and Cancel. Close Anyway leaves it as Quit Anyway does (the quit marker; saved first thing on the next open).
  - A workspace still opening finishes opening first. Closing another window on the same workspace just closes that window. Closing the list or first run, when it's the last window, quits.
- **Link GitHub** (boards `link-github` and `link-github-linked`; before launch; not a Portenv account):
  - GitHub's device flow, through `gh auth login` inside the box, with the same access as the person's own gh; any number of repositories can be cloned into the box afterwards (the owner, 2026-10-10, ADR 0018: GitHub access works as on their Mac; no Portenv GitHub App and no repository picker);
  - gh's default scopes, then `gh auth setup-git`, so plain `git clone` and `git push` work for any repository the account can reach; the token lives in the box home's gh config (`hosts.yml`), never in the image;
  - the login is kept in the box's encrypted home and moves with it; Portenv never sees or keeps it;
  - agents working in the box can use that login too, and the dialog says so;
  - the inspector's Who's here shows "This workspace can push to GitHub as you", with Sign Out of GitHub.
- **Built so far** (lane C's first milestone, 2026-10-10):
  - **The launch decision:** with no box, first run; with a box that isn't on this Mac, the box list, saying so; never a window for a missing box (`LaunchDecision`, tested). The app no longer defaults to "demo".
  - **The first-run screens and their flow,** reduced as above: Continue is enabled only when the step is complete; the recovery key needs the "I've saved" checkbox; box names follow the CLI's rule. "Count me in usage numbers" is hidden until counting exists, and then starts unticked, with the exact fields one click away, on Welcome (before any update check; R-0106, R-0126).
  - **Docker Desktop on Welcome** (R-0098, the `first-run-1-welcome-docker-*` boards): until the `apple` driver ships, Welcome checks through portenvd (`GetEngine`, which pings the engine through the driver) at once and every 2 s while a card shows. "Docker Desktop isn't running" offers Open Docker Desktop, and "Portenv needs Docker Desktop for now" offers Get Docker Desktop…. Continue waits until Docker answers. VoiceOver hears a card appear or change, and "Docker Desktop is running" only when a shown card goes, so someone with Docker already running hears nothing (R-0110). Another engine answering on the socket counts as running; Docker Desktop in the Trash counts as missing. If portenvd misses two checks in a row (about 4 s), Welcome says "Portenv's background service isn't answering. It usually starts by itself in a few seconds." with Try Again instead of a silently disabled Continue; Try Again never runs alongside a check in flight, and Continue's VoiceOver hint gives the current reason (R-0110, R-0116). If Docker stops later, an open box on this Mac whose agent is unavailable says "This Mac · Docker Desktop isn't running", with the same button, in its state line and window. portenvd notices a dropped agent channel on its own: the state poll's readiness question (every 5 s while the app watches) marks the agent unavailable, and an answer clears it, as CheckBox does (R-0110).
  - **Ready, not just running** (R-0211): Docker Desktop open but its engine not answering the short ping yet is "Docker Desktop is starting…", a card with no button, checked again every 2 s; the same check runs on "Make your first workspace", whose Create waits for it and says why. Never a long dial: only `GetEngine`'s 1.5 s ping.
  - **A Keychain or Touch ID prompt** (R-0214): before a read that may prompt, the window that asked comes to the front, so the prompt shows over it; while it's up the window says "Waiting for you to allow access to Portenv's keys…" (GUIDELINES §3.1).
  - **The box list:** open a box, or New Workspace… (first run's first-box step).
  - **Move to Applications** (lane C's second milestone): before the login item is registered, an app outside /Applications, App Translocation included, asks "Move Portenv to Applications?". Move copies it there (an older Portenv is replaced, anything else named Portenv.app never is), opens the copy and quits; Not Now isn't asked again for the same location. Dev builds can redirect the copy with `PORTENV_MOVE_TO` so UI tests never replace the real app.
  - **portenvd not starting** (R-0028, R-0032): while waiting for portenvd, the app reads the login item's status and `launchctl print` for its job.
    - **launchd refused it (exit 78):** one line with both remedies, Login Items first ("Portenv’s background service didn’t start. Turn Portenv on in System Settings › General › Login Items, and if it’s not in the Applications folder, move it there."), with Open Login Items and Try Again. SMAppService said "enabled" before Portenv was switched on, so the location is never given alone.
    - **Not refused, but the login item isn't enabled:** "Portenv needs permission to run in the background…", with Open Login Items; the wait goes on.
    - **Otherwise, never a spinner without end** (R-0121): each 2 s without an answer is a missed check (`LaunchWait`, tested). After two in a row (about 4 s), a Mac with no earlier run shows Welcome, and one with an earlier run recorded shows the (empty) list. Either way it says "Portenv’s background service isn’t answering. It usually starts by itself in a few seconds." with Try Again, and the checks go on. The spinner reads "Starting Portenv…" to VoiceOver. The login item's status is read right after the first unanswered ask. Once the person moves on from that screen (New Workspace…, a later first-run step), portenvd's answer only refreshes the list of workspaces and never replaces what they're on. A wait that Try Again replaced, or whose window closed, never decides, and the wait stops at an answer or when the last window closes, never when a second window does (R-0125, R-0132).
  - **New Workspace… from the box list** (the new-box boards): titled "New workspace", with Cancel, ⎋ and "‹ Your workspaces" back to the list, and Create Workspace. The name field says the rule under it, or what's wrong, in red, in one line.
  - **"Not built yet" is a notice,** not an error. Dev builds (their own bundle ID) name the `portenv init` workaround; a release build never sends anyone to Terminal.
  - **Closing to the list and the invalid name's look** (lane C's third milestone): `WindowClose` (the close runs before the list is shown; tested with the order of calls) and `NameFieldLook` (tested), with renders in `docs/screenshots/first-run/`.
  - **Not yet:** making keys, the recovery key and the first box from the app. These are data-loss paths. The decisions are taken and ADR 0019 works out the details (the recovery key's format and check character, the per-box key derivation, the pool of pre-derived passwords, and `CreateBox`'s order of steps); code follows its review, tests first. Built so far: the recovery key's library (format, check character, reading a typed key, the per-box derivation), in Swift (`PortenvKit/RecoveryKey.swift`) and Go (`core/keys/recovery`), both against one vector file (R-0095); and in `portenvd`, `CreateBox` (steps D1 to D6, idempotent on the box ID, a fault and a crash test at every step), `DiscardCreation`, `ListBoxes`' `creating` flag and `RecoverBox`, with the clean-Mac restore test (This Mac and folder storage; server storage waits for 2.9). In the app: the pool of 32 pre-derived recovery passwords in its own Keychain item (`RecoveryPool`, never synchronised, never the recovery key), the Keychain writes for repository keys (`SecretStore`, with portenv init's attributes), and steps A1 to A3 around `CreateBox` (`BoxMaker`, one call at a time, never deleting a key a finished workspace uses: the key stored and read back before the call; what each refusal leaves; the entry removed only once the box exists; at launch, used entries removed and half-made boxes finished or discarded), all tested with fakes, never the real Keychain. First run uses them (`KeyedFirstRunService`): the recovery key is made on leaving Welcome and held in memory only, fills the pool on Continue from its step and is wiped, and the first workspace (and New Workspace…) goes through `BoxMaker` and CreateBox; at launch, used entries go and half-made workspaces are finished or discarded; a Keychain call that waits over 2 s says "Waiting for Keychain access" by Continue (GUIDELINES §3.1). When the pool runs out, New Workspace asks for the recovery key in a sheet; `portenvd`'s `CheckRecoveryKey` checks it against the workspaces here that carry a recovery key (read-only) before the pool is refilled, so a valid but wrong key is refused; with nothing to check against, the sheet offers Make a New Recovery Key (ADR 0019). Next: `AddRecoveryKey`. Until then, first run says so plainly at Protection. Start from (GitHub, folder) and the detected toolbox on the first-box step also come later.
- **Move to Applications first** (the owner, 2026-10-10; found while testing #41):
  - If the app runs from anywhere but `/Applications`, it offers "Move Portenv to Applications?". That includes App Translocation, when the app is opened from Downloads or the DMG. launchd may refuse to start `portenvd` from other places.
  - **Changed 2026-10-12 (R-0121; approved by the reviewer, R-0124, under the owner's delegation):** the question is a sheet on the Portenv window, not an app-modal alert, and the launch doesn't wait for it. The login item is registered and the wait for `portenvd` starts first (`LaunchOrder`, tested). A modal alert before registration once showed on another Space while the window kept a spinner with no agent registered. Move removes the login item registered from the old place before the copy opens, so launchd never keeps running `portenvd` from there; a failed move registers it again (`MoveOffer.answer`, tested).
  - If launchd still fails to start `portenvd` (for example exit 78, `EX_CONFIG`), the app says so in a plain line. It doesn't wait out the "background service isn't answering" notice (about 4 s, R-0121).

**8. 1.6: the port relay**

A dev server on the box's localhost opens in Safari. Port pills in the toolbar.

**9. 1.7: shared folder, then the File Provider**

Show in Finder (⌥⌘R) stops being disabled here.

**10. 1.8: releases**

Developer ID signing, notarisation and Sparkle, with the key backups. Signed public CLI releases and their signing key are already on the first-release checklist (#27).
- **Move the keys to the data-protection keychain** (`kSecUseDataProtectionKeychain`, needs the stable signing identity): the repository keys and the recovery pool (ADR 0013, ADR 0019). Until then they're in the file-based login keychain, which ignores `kSecAttrAccessible`: although written this-device-only and never synchronised, they travel in backups and Migration Assistant like any login keychain item (R-0122). The move needs a migration of existing items, tested, and `portenvd`'s and the CLI's reads moved with it.
- Signing uses the owner's existing Apple Developer organization team (decided 2026-10-10), so 1.8 needs no new enrolment. It's also the stable-signing fix for item 1e. The team's ID and name aren't written here; at 1.8 they come from the owner's signing setup, kept in local config or CI secrets. Creating the Developer ID Application certificate and the notarization credential (an App Store Connect API key) are account changes the owner makes at 1.8.
- **Look at again once signing is stable:** run `portenvd` inside a small helper app bundle (`Contents/Helpers/Portenv Helper.app`), so it can post a notification the moment a background save succeeds after Quit Anyway. Until then the next launch of the app shows it (1.1).
- **`portenvd` still starts after an in-place update** (found at the Mac, 2026-10-10). With ad-hoc-signed dev builds, launchd refused a rebuilt `portenvd` (exit 78, `EX_CONFIG`) until the login item was registered again; the job carries launch constraints. A Developer ID signature stays the same across updates, so releases should be fine. 1.8's Sparkle update test checks it, and the dev workflow re-registers after `gmake app` (`--unregister-service`, then open the app).
- **Several copies with one bundle ID** (found in the lanes' UI tests, 2026-10-10). With `bin/Portenv.app`, `Portenv-B.app` and `Portenv-C.app` all `com.portenv.Portenv`, launchd resolved portenvd through the wrong copy and refused it (exit 78). Dev lanes now build with their own identity: `gmake app LANE=X` stamps the bundle ID `com.portenv.Portenv.dev-X`, the launch agent `com.portenv.portenvd.dev-X`, and stamps the lane into the binaries at build time (Go with `-ldflags -X`, the app with Info.plist's `PortenvInstance`): a release binary ignores `PORTENV_INSTANCE` entirely, and a dev binary refuses to run if it names another lane. The lane gives the Keychain service `dev.portenv.repository.dev-X` and the folders `Application Support/Portenv-dev-X` and `Logs/Portenv-dev-X` (`core/internal/instance`, `scripts/stamp-instance.sh`, checked by `scripts/check-app-identity.sh`). Release builds keep `com.portenv.Portenv` and today's names. Each lane's build needs its own Login Items switch once, and its own boxes (a dev lane never reads another build's keys).
- **An app newer than its running `portenvd`** (found while testing #41, 2026-10-10):
  - after an update, or when the login item still points at another copy of the app, the app compares versions and relaunches `portenvd` from itself (the `Relaunch` RPC, ADR 0014);
  - it re-registers the login item when that item runs a different copy;
  - until then, a call `portenvd` doesn't know shows "Portenv's background service is out of date. Quit Portenv and open it again; if this keeps happening, reinstall Portenv." (#41).
  - **Built (R-0198, R-0203):** `gmake app` stamps one `BUILD_ID` into the app (`PortenvBuildID`) and its helpers (`GetVersion`'s `build_id`). At launch, and when a call hits a portenvd that doesn't know it, the app compares build IDs; another build, run from this bundle (or from a file an update replaced), is restarted: `Relaunch` (workspaces keep running), then the login item registered afresh, each with 5 s to come back. "Updating Portenv's background service…" shows meanwhile, the call is tried once more, and what can't be fixed (a portenvd from another copy still on disk, or one that doesn't come back) says the reinstall line. First run maps every daemon error to a plain line (`DaemonErrorText`, never a socket path or gRPC text) and waits up to 10 s for portenvd, saying "Portenv's background service isn't running. Portenv is starting it…".
  - **Found with lane C's ad-hoc build (2026-10-14):** after an in-place replace, launchd refuses the new `portenvd` (exit 78, "needs LWCR update"): an ad-hoc binary's designated requirement is its cdhash. Re-registering didn't reliably clear it, and both dev-A's and dev-C's login items now wait for the owner. A Developer ID build's requirement is the team, so a signed in-place update is expected to work; it's checked below, at the next signed build.
- **Release checklist:** test `portenvd`'s socket with a real second macOS account. A process running as another user is refused; 1.1's test simulated the other uid.
- **Investor-grade metrics without identifying anyone** (decided 2026-10-09). No email collection.
  - **Downloads:** GitHub release asset counts (the DMG and the CLI builds), and Cloudflare's counts for portenv.com/download. No scripts on the site.
  - **The repository's traffic is archived daily:** a scheduled job saves views, clones, referrers and stars, since GitHub keeps them for only 14 days.
  - **Active use, through the update check** (the same rule applies to the CLI and the runner):
    - **No identifier of any kind:** no install ID, no account, no fingerprint.
    - **The fields are a fixed, published list, all coarse:**
      - the version, the macOS version and the architecture;
      - `c=<install month, YYYY-MM>`;
      - `w=1` on the first check of each ISO week, and `m=1` on the first of each month;
      - on the weekly check only: `moved=1` (a box was moved this week), `agent=1` (an agent worked in a box this week), `boxes=1|2-5|6+`, and `servers=0|1|2-3|4+` (connected servers).
    - **At most one check a day,** sent only on days a box was opened.
    - **What they give, all as plain counts:** daily, weekly and monthly actives, cohort retention (actives per install month, per week), activation and depth.
    - **The server keeps aggregated counts only,** with no IP logs. If this needs a minimal Cloudflare Worker behind portenv.com/appcast.xml, it's the named exception to ADR 0008's "no backend before Phase 3" (the Backend row in Decisions already made).
    - **Settings:** "Count me in usage numbers (no ID, nothing about your work)", off until the person ticks it (opt-in: the reviewer, on the owner's delegation, R-0126), and one click turns it off again. Off removes every counting field, but the update check itself stays, for security updates.
    - **The choice is stored** (`UsageCounting.choiceKey`, "countUsage"), both from first run's checkbox and from Settings, and `UsageCounting.isOn` reads it. Settings › Updates › What Portenv sends is generated from it, so the Privacy line stays true: today counting doesn't exist and the line says no usage numbers are collected (R-0149, R-0156).
    - **An unreachable endpoint never blocks or slows anything** (own-route).
  - **Servers count themselves too, under the same rules:** no identifier, coarse fields, a published list, and an opt-out.
    - **When:** the runner sends its own check, at most once a day, only on days it ran a box.
    - **The fields:**
      - the version and the architecture;
      - the OS family (for example `ubuntu-24.04`);
      - `c=<install month>`;
      - weekly only, `boxes=1|2-5|6+`, plus `doors=1` if public doors (ADR 0015) are on.
    - **The opt-out:** a flag in the runner's configuration, documented in the runner's install output and on /privacy.
  - **What servers give, as plain counts:**
    - the share of active users with a server;
    - active servers, daily, weekly and monthly;
    - server cohort retention;
    - public-door adoption.
  - **/privacy and first run (1.5)** list the exact fields, in plain words.
  - **A monthly metrics snapshot,** in the same format every month, generated from the counts:
    - downloads;
    - daily, weekly and monthly actives, and growth;
    - the cohort retention table;
    - the share of installs that moved a box, and the share where an agent worked;
    - the share of active users with a server;
    - active servers (daily, weekly, monthly) and server cohort retention;
    - public-door adoption;
    - stars.
  - **The CNIL's audience-measurement criteria** (checked by the reviewer on 2026-10-10 against the CNIL's guidance, updated 4 July 2025; a design review, not legal advice). The design fits the consent exemption: one publisher, anonymous statistics only, no identifier, no cross-app tracking, no third-party sharing, an opt-out, published fields. To keep it that way:
    1. **Store counters, not request tuples.** Per day, keep a count per field value, plus only the cross-tabs the snapshot needs (install month × week active). Never store a row per check combining the version, OS, architecture, install month and buckets: that combination could single out a user in a small cohort.
    2. **Small numbers:** anything shown outside the owner (the snapshot, a deck, the website) rounds or hides cells below 10.
    3. **Retention:**
       - aggregated counts are anonymous and can be kept;
       - nothing request-level is kept at all;
       - if a Worker ever buffers raw checks, the target is under 24 hours (the CNIL's maximum is 25 months).
    4. **Cloudflare sees IP addresses in transit:**
       - Worker logs and Logpush are off for the counting route;
       - no Analytics Engine fields carry an IP or headers beyond the published list;
       - Cloudflare acts as processor under its DPA, with its EU-US Data Privacy Framework certification as the data-transfer basis;
       - /privacy says so in a line.
    5. **Who is responsible:** /privacy names the controller and a contact for questions and objections, exactly: "EverydayMoney SAS, 60 Rue François 1er, 75008 Paris, France (RCS Paris 944 681 634). Contact: contact@everydaymoney.eu". It matches the publisher block on portenv.com/legal. Today /privacy covers only the website; this line is added when counting ships (1.8).
    6. **The opt-out is visible before the first count:**
       - first run shows "Count me in usage numbers", with the link to the field list, unticked (opt-in, R-0126), before the first update check is sent; hidden until counting exists;
       - the runner's installer prints its opt-out flag before its first check.
  - **Release checklist:** run the CNIL's self-assessment tool for audience measurement against the final implementation, and keep the filled copy in `docs/` (not on the website). Never describe the counting as "CNIL certified" or "validated": the CNIL forbids that wording.

**Standing items**

- **The next server session starts with `scripts/fetch-cli.sh` against the server** (#27). The three server scripts install CI's binaries by commit; fix whatever fails there and then. It doesn't block anything before.
- **#26 follow-ups:**
  - Last Save Point time in the menu (1.4);
  - Restart Workspace with ⌥ in the menu bar's Workspace menu (1.1, once the menu bar is AppKit-backed or SwiftUI can see ⌥);
  - **a live VoiceOver check and a title-menu screenshot with the owner at the Mac** (the next session at the Mac; the screen was locked overnight).
- **Agent readiness rules apply from now** (2026-10-09): every command added or changed supports `--json`, uses a stable exit code, never asks without a TTY, and its errors say what happened and what to do next (Agent readiness, principles 2 and 3).
- **Every milestone keeps the test-path rule:** app-relied behaviour is tested through `portenvd` or the runner and the channel, each check mutation-checked, and every e2e call has a time limit.

**Decided in review (2026-10-09)**

- **The tab bar comes before the spike,** on the agent's tab interface (above).
- **Hostname:** sanitised as proposed, falling back to `box-<first 8 of the box id>`. The original name shows everywhere else; nothing extra is needed.
- **Item 1e's ADR** waits for the root-cause check.

**1.0 Walking skeleton** (docker driver only, no first run, unsigned, run from Xcode; demo: docs/demo/1.0.md)

A thin slice through every layer, in the code where 1.1 and 1.2 continue: a minimal `portenvd`, the main window with a SwiftTerm terminal in the box's tmux session over the box agent's channel (ADR 0010, never `docker exec`), and the title menu, where only Move To ▸ (This Mac, each server) and Revert To ▸ Last Save Point work. File › Make Save Point makes the save point to revert to. Until 1.1 the app reaches `portenvd` through the `portenv` CLI.

- [x] The terminal lands in the box's tmux session as `work`, through `portenvd` and the agent channel; no `docker exec` into the box but the engine's own health check (`tests/e2e/daemon.sh`, counted from the engine's exec events)
- [x] Revert To ▸ Last Save Point puts the save point back and keeps the replaced home as a save (sync tests, the model test, `tests/e2e/daemon.sh`)
- [x] Move To ▸ a server and back to This Mac through `portenvd`, files intact both ways (`tests/e2e/daemon-server.sh`, 2026-10-08: 29.0 s there including enrolling the server, 21.0 s back)
- [x] Quitting the app (or SIGTERM) saves and releases the open box
- [x] The restic password stays unreadable in the box with the channel (`TestChannelPasswordIsUnreadable`)
- [x] ADR 0010's conditions hold, each with its test: the channel's files never reach a save and are gone once loaded, root in the box cannot read the key or token, and an impostor on the agent's port gets nothing while the app reports the box agent unavailable

**1.0b A box on a server, the same as on the Mac** (demo: docs/demo/1.0.md)

The minimal server-side piece that 2.1's runner grows from: `portenv-runner` (the same daemon code as `portenvd`, as a systemd service) opens boxes on the server with the agent channel. The Mac moves a box there by enrolling it and opening it through the runner (`sudo /usr/local/bin/portenv app open`, the only sudo command its SSH user `portenv` may run), fetches each start's channel secrets from the runner over the SSH session and keeps them in memory, and reaches the agent's port on the server's loopback through an SSH forward. The same window shows the same terminal on the server, with the state line saying where it runs; save points, Revert To and Restart Workspace work there exactly as on the Mac. The secrets reach a box only on its stdin, never a file. A runner restart leaves boxes running; the app shows the box agent unavailable and offers Restart Workspace.

- [x] The window's terminal runs on the server after Move To; save point and Revert To there; Move To ▸ This Mac brings the edits back (`tests/e2e/server-session.sh`, 2026-10-08: 28 checks, Move To 16.8 s)
- [x] A runner restart loses nothing; an impostor on the server box's agent port gets nothing; no channel secret on the server's disk, journal or shell history (`tests/e2e/server-session.sh`)
- [x] The title shows the box's name over its state line and opens the box menu; Move To checks the current location (checked through accessibility in Portenv.app)



**1.4 Autosave, the sync symbol, Changes and Browse Saves: slow links are normal**

Slow uplinks are a real user condition, not an edge case (the Phase 0 gate ran over about 50 KB/s up). So:

- Save status is honest: the title's save state comes only from the recorded save state (the last recorded snapshot and its time, the dirty flag, a save in progress) and what portenvd knows is happening (storage unreachable, the box agent's channel down), never from "open succeeded" or other events. A new box shows "Not saved yet". The states: Not saved yet, Saving…, Saved at (time), Offline · will save later, Not saved · box agent unavailable, Not saved since (time) · retrying, Not saved since (time), Not saved since (time) · Portenv quit before saving. Tested per state (`daemon/savestate_test.go`, `PortenvKitTests`).
- A Move, or any save that takes more than a few seconds, shows visible progress and a time estimate in the title subtitle, from the bytes still to send and the measured bandwidth.
- Closing the laptop or quitting mid-upload never loses work and never leaves the lease in a bad state: the local home stays as it was (dirty), no partial save counts as complete, the lease stays with this machine until a save completes, and the upload resumes (restic deduplicates what already arrived) when the Mac wakes.
- Same-machine resume never waits on the network: offline, the box starts from the local home and saves queue.
- Quitting never fails silently. ⌘Q names every workspace running on this Mac, then closes each (ADR 0023); if its release save fails, quitting stops and the app asks: "acme-api couldn't be saved before quitting. Its work is still on this Mac." with Try Again (default), Quit Anyway and Cancel. Quit Anyway, a force quit or a crash leaves the box as it is with a quit marker (`state/<box-id>/quit-unsaved`), written by portenvd's shutdown; the next open says "Not saved since 15:54 · Portenv quit before saving" and saves first thing, and the marker goes with the first successful save. Shutdown waits for boxes still opening and refuses new opens. Tested in `tests/e2e/daemon.sh` (agent down, Quit Anyway, relaunch; quit while opening) and `PortenvKitTests` (the alert's choices).
- Waking from sleep: the app reports the wake (`portenv app woke`); portenvd checks every open box's agent channel and restarts a box whose channel is gone (its home stays: rule 3), so a dropped channel never sits unnoticed for hours. Tested in `tests/e2e/daemon.sh` (the channel dropped during sleep).
- A package that can't be installed never stops a box from starting. The box starts and the state line stays normal; the inspector's Where it is section shows "1 package couldn't be installed: <name> · Retry". The box agent tries the rest of the list one package at a time (one bad name never keeps the others out), retries the failed ones in the background (after 1, 5 and 15 minutes, then hourly, or at once on Retry: `portenv app retry-packages`), and logs the error; `portenv app state` lists them. Tested in `tests/e2e/daemon.sh` (a deliberately missing package) and the agent's unit tests.

**1.8 Releases and where the app is downloaded from**

- **Pipeline:** a version tag triggers CI on a macOS runner: build, sign with the Developer ID certificate, notarize and staple, package and sign `Portenv.dmg`, and generate a Sparkle `appcast.xml` signed with Portenv's EdDSA key.
- **Hosting:** the DMG and `appcast.xml` are assets on the `portenv/portenv` GitHub release.
- **The app icon** ("Two places", the owner, 2026-10-12, R-0128): `apps/mac/Resources/Assets.xcassets/AppIcon.appiconset` (ten PNGs), compiled into `AppIcon.icns` by `scripts/make-app-icon.sh` in `gmake app`, with the SVG sources in `apps/mac/Resources/icon-source/`. A dev lane's app (`LANE=X`) carries an orange disc with its letter, so it's never mistaken for the release app. macOS 26's layered icon format comes later.
- **Stable URLs:** `portenv.com/download` and `portenv.com/appcast.xml` redirect to the latest release's assets (Cloudflare redirect rules). The app's Sparkle feed URL is `portenv.com/appcast.xml`, never a GitHub URL, so hosting can move later without breaking updates.
- **Keys:** the Developer ID certificate and the Sparkle EdDSA private key live only in CI secrets and the owner's Keychain, never in the repository.
- **Design testers need Docker Desktop** (the owner, 2026-10-14 19:37, R-0211): it runs their workspaces on the Mac. Apple's Containerization engine (ADR 0004) comes later. First run checks that Docker Desktop is installed and ready before "Make your first workspace", waits while it starts ("Docker Desktop is starting…"), and says what to do when it isn't there.
- **No Docker inside a workspace yet** (the owner, 2026-10-16 08:30, R-0223): the workspace image ships no container engine, and nothing is mounted from the Mac's engine (its socket is root on the Mac: never). A tester may install an engine in a workspace, but it won't start today: the workspace helper refuses the privileges a nested engine asks for. Testers are told "Docker isn't available inside a workspace yet. Use Docker Desktop on your Mac for containers." (`docs/testers.md`). Whether a self-installed rootless engine can run is a spike (lane C), and any change to the helper or the box profile it suggests is the owner's security decision.
- **Agents in a workspace work in their own clones** (ADR 0022, amended by the owner's security decision of 2026-10-18, R-0315): each coding agent in a team has its own Unix user and a full clone of the project in its home, on branch `agent/<id>`. The owner's checkout and its `.git` are never agent-writable, Portenv adds no system git `safe.directory` (the interim entries from PR #197 go away), and an agent's branch comes back by a fetch that never opens the agent's repository as `work`. Read-only lenses get a read grant on a writer's clone, never write. A workspace whose team used the shared project is moved over at its next team start: `.git` back to `work` alone, a clone per agent with its branch and uncommitted files, and nothing deleted without the owner's decision. **The code PR's gate:** the team-start step in which an agent tampers with its own clone's config and `work`'s git in the project runs nothing (ADR 0022 section 2a, point 5).
- **An owner preview uses the image CI published for its commit** (found in the owner's demo, 2026-10-18, R-0302): a dev preview (`gmake app LANE=Z`) can be current while its workspace runs an old helper, because `portenv-agent` comes from the toolbox image, the Docker driver pulls only when the tag is missing, and a new workspace pins whatever `:main` the Mac has. Before a preview, check that `docker buildx imagetools inspect ghcr.io/portenv/toolbox-node:sha-<12>` (the commit's first 12 characters) shows the same digest as `:main`, `docker pull` it, and test in a new workspace: existing ones keep their pinned image.
- **Design-tester builds are signed and notarized on the owner's Mac, not in CI** (decided by the reviewer, on the owner's delegation, R-0089, 2026-10-11). `gmake release-local` (`scripts/release-local.sh`):
  - builds the release app with `gmake app` (no lane), and refuses it unless its third-party notices cover every Go module in its helpers and every Swift pin;
  - signs the helpers, then the app, with the Developer ID Application identity, the hardened runtime and a secure timestamp. There are no entitlements; `apps/mac/Portenv.entitlements` says why, and any entitlement added later comes with its reason;
  - notarizes and staples the app and `Portenv.dmg`, checks them with `codesign`, `spctl` and `stapler`, and prints the DMG's SHA-256;
  - uploads each file to Apple once: the submission ID is saved in the release folder before waiting, a dropped connection or a slow review only waits longer on that submission, and `--resume <folder>` carries on an interrupted run without rebuilding, re-signing or uploading again (`scripts/notarize-lib.sh`, tested against a fake `xcrun` by `scripts/test-notarize.sh`). A file with no saved ID is first looked up in `notarytool history` (same name, In Progress or Accepted, made since the file), before uploading and again after a failed upload, since a slow upload can reach Apple before `submit` returns; one found is adopted and must show this file's SHA-256 in its notary log once Accepted, or the run fails without uploading and skips it next time (R-0124). The first submission's 12-hour poll ended in a timeout (NSURLErrorDomain -1001), which made the old script fail (2026-10-12);
  - writes everything to `~/Library/Caches/Portenv/release/<version>-<build>/`, never the repository.

  The credentials (the Developer ID certificate and the `portenv-notary` notarytool profile, an App Store Connect API key) live only in the owner's login keychain. `--check` runs only the preflight and names the owner step for whatever is missing. CI secrets wait for the public release (the pipeline above).
- **Third-party notices** (decided by the reviewer, on the owner's delegation, R-0096, 2026-10-11): giving a DMG to anyone is distribution, so every build carries the licences of the code inside it. `gmake app` reads the Go modules from each helper's build information (what `go version -m` shows), takes each module's licence, copying, copyright, notice and patents files (in subfolders too, for vendored code, and REUSE `LICENSES/`) from the module cache at that version, always checked against the checksum database, adds Go's own licence and the same files from every `Package.resolved` pin's checkout, and writes them to `Contents/Resources/Licenses/<go|swift>/<component>@<version>/` with one `ThirdPartyNotices.txt` (a line per component, `name version · licence`, then each one's section with a `Source:` line and the texts). Help › Acknowledgements… shows that file in a plain, selectable 720 pt window, searchable with Edit › Find (R-0119). A component without a licence file, with any licence file that isn't recognised on its own, with copyleft terms in any spelling, or vendoring code without its licence at the vendored revision (BoringSSL in swift-crypto and swift-nio-ssl) fails the build unless `third-party/allowlist.json` covers it, with its text and a reason (`third-party/README.md`; R-0110, R-0116). CI builds the app in the Swift job (`swift-env-check`), so a missing licence fails CI.
- **Sparkle key backup** (losing it means installed apps can never be updated): when the key is generated, export it once to two offline copies (an encrypted USB drive and a printed or written copy in a separate safe place), record its public key and fingerprint in this plan, and test a restore into a clean Keychain by signing a throwaway appcast that a test install accepts. Repeat the restore test before each major release. A suspected leak means rotating to a new key, which only apps that already trust both keys can follow: ship the new public key in an update signed with the old key first.
- **Own route:** the update check runs in the background and times out quietly when portenv.com is unreachable; it never blocks or slows launch. A release is blocked unless the `own-route` job passes on the tagged commit.
- **Later:** a Homebrew cask pointing at the same release.
- **The CLI until then: internal CI builds.** Every CI run builds `portenv` and `portenv-runner` for linux/arm64 and amd64 and `portenv` for macOS arm64 (with cgo, for the Keychain) into one artifact, `portenv-cli-<commit>`, with a `checksums.txt` (kept 90 days). The server setup and the server test (`tests/e2e/server-session.sh`, `scripts/demo-server-box.sh`) install these by commit through `scripts/fetch-cli.sh`, which checks the checksums; `PORTENV_LOCAL_BUILD=1` builds locally instead, for development. They are not releases: reading them needs a GitHub sign-in, and nothing signs them.

- [ ] A new user goes from download to a working box without typing a command or creating an account
- [ ] Signed public CLI releases (ADR 0009): the dedicated Ed25519 release signing key is created, backed up offline like the Sparkle key (two copies, restore tested), and kept only in CI secrets; each release's `checksums.txt` is SSH-signed (`ssh-keygen -Y sign -n portenv-release`) and published with the binaries and their build provenance; the install script verifies them. The server setup and the server test move from the internal CI artifacts to the signed release.
- [ ] **Gates the design-tester build (R-0203):** on a signed build, an in-place update (`tests/e2e/app-update-in-place.sh`, the bundle replaced as Sparkle does, then opened) restarts `portenvd` at the new build with no logout
- [ ] **Gates the design-tester build (R-0203):** on a signed build, after `portenvd` exits (a crash or a `Relaunch`), launchd starts it again and the app carries on
- [ ] **Gates the design-tester build (R-0211, CC-0302):** `portenvd` logs OpenBox on entry, at "pulling the toolbox image" and on every error, by workspace ID with no paths, and each open phase has a deadline (about 30 s without progress is a plain-line failure) (lane A)
- [ ] **Gates the design-tester build (R-0211):** first run's engine check waits for Docker Desktop to be ready, not just running: a short `Ping`, retried while it starts ("Docker Desktop is starting…"), never failing on a long dial; missing or stopped, it says what to do; the engine's own errors (not running, image missing, no disk) are plain lines in first run and the window (lane C)
- [x] **Gates the design-tester build (R-0323):** New workspace checks Docker Desktop and the disk before anything is written: the app keeps Create disabled until Docker Desktop answers (R-0211), and `CreateBox` itself asks the engine again (5 s) and measures the free space on the disk holding Portenv's folder, refusing with one plain line ("This Mac has 3.2 GB free, and a new workspace needs at least 5 GB. Free some space, then create the workspace again.") under 5 GB. A disk that can't be measured doesn't refuse. Docker Desktop's own disk image limit isn't measured; a full one still fails the open with the "out of disk space" line.
- [ ] **Gates the design-tester build (R-0211):** the first open's image pull shows its progress, "Getting the workspace image… 312 MB of 900 MB", streamed from `portenvd` (lane C)
- [ ] **Gates the design-tester build (R-0211):** a failed open stays visible in the window, with its plain line and Try Again, and the box list has an Open button (lane D)
- [ ] Close Workspace saves and releases; reopening restores within 5 seconds; closing the window leaves it running (ADR 0023)
- [ ] On a throttled uplink (for example 400 kbit/s), a Move shows progress and an estimate, and closing the lid mid-upload loses nothing and leaves the lease with this machine
- [ ] A version tag produces a signed, notarized, stapled `Portenv.dmg` and a signed `appcast.xml` on the GitHub release; `portenv.com/download` and `portenv.com/appcast.xml` redirect to them, and an installed build updates through Sparkle
- [ ] The Sparkle key's offline backups exist and a restore test passed
- [ ] Offline: work continues and saves upload when the network returns
- [ ] No docker exec as an access path, on the Mac or on servers: boxes are reached only through `portenvd` on the Mac and the runner on servers, with the agent channel (ADR 0010). The Phase 0 CLI's docker exec path is removed or moved behind them.
- [ ] Every control has a VoiceOver label; light and dark appearance both pass review
- [ ] A dev server bound to the box's localhost opens in Safari through the relay
- [ ] Inside an `apple` box (a VM), the restic password probe (`tests/e2e/restic-isolation.sh`) passes: root in the box cannot read the password, because the agent drops the forbidden capabilities itself

### Phase 2: Developer servers

The toolbox image is published from CI ahead of the rest of 2.10: each architecture builds and tests on its native runner, then one multi-arch manifest goes to `ghcr.io/portenv/toolbox-node`, tagged `sha-<commit>` and `main` (and `vN` on version tags), with a build provenance attestation. Macs, servers and the setup script pull by digest, and the CLI pins a box's image to its digest on first use. Local builds stay possible for development; nothing requires them.

Milestones (reordered on 2026-10-09 to reach the launch demo soonest; see "Launch: Phases 1 and 2 together"): 2.1 runner (server build of the core) installed over SSH, grown from 1.0b's minimal runner: the Mac's SSH user may run only `/usr/local/bin/portenv` with sudo, never a shell · 2.2 append-only storage · 2.3 Move To in the title menu, and the lease sheet · 2.4 stand-in agents over SSH and the CLI · 2.5 webhooks · 2.6 stdio MCP · 2.7 remote MCP on the runner · 2.8 web terminal on the runner · 2.9 Add a Server wizard with preflight · 2.10 toolbox registry, versions and drift warnings.

The old numbers, for ADRs and PRs written before 2026-10-09: 2.2 wizard → 2.9 · 2.3 Move To and 2.4 lease sheet → 2.3 · 2.5 toolbox registry → 2.10 · 2.6 append-only storage → 2.2 · 2.7 stand-in agents → 2.4 · 2.8 webhooks → 2.5 · 2.9 stdio MCP → 2.6. Remote MCP (2.7) and the web terminal (2.8) on the runner are new; Phase 3 keeps the Portenv-hosted versions (3.9, 3.10).

**2.2 Append-only storage** (must land before 2.4, which gives outside agents stand-in access to boxes)

- The storage credential that enters a box becomes append-only: it can add files to the repository but never delete or overwrite one. A box, even as root, cannot destroy a save.
- Forget, prune and the removal of stale locks use a separate credential that never enters a box and lives only on the host (`portenvd` or the runner).
- For SFTP storage this means restic's REST server in append-only mode (`rest-server --append-only`) on the server, bound to the loopback address and reached through the existing SSH connection, with an append-only user for boxes and a full user for the host. No new inbound port. For S3-compatible storage: a put-only credential for boxes, a full one for the host.

**2.3 Move To and the lease sheet**

The title menu's Move To, as found at the Mac on 2026-10-10:
- **Servers are shown by name** (GUIDELINES §2), never by address. A server added before names existed (the Phase 0 setup) shows "52.47.207.191" today. It gets a name, defaulting to its host name, and the user can rename it.
- **Move To is disabled when the window has no box,** like Revert To and Make a Save Point.
- **The current location has a checkmark** ("This Mac", or the server's name).

The lease sheet (the board `move-and-lease`; R-0136, R-0140, R-0168), in three PRs:
- **A1, `portenvd` (built):** OpenBox's refusal for a workspace open elsewhere carries the trailer `portenv-refusal: open-elsewhere`, and `GetPlacement` gives the facts: the holder (one of this Mac's servers, by an ID and the name it shows by, never its address; or another machine), its machine ID, the newest save's time, and whether it answers. A server is found from the lease's machine ID by asking each of this Mac's servers `portenv machine-id` once (remembered in `state/server-machines.json`); it answers if `portenv app ping` over SSH succeeds within 2 s. Another Mac can't be asked. "Move it to this Mac" from a known server closes it there (saved and released) and opens it here. The sheet never waits for the probe: it shows at once with "Checking whether <place> answers…".
- **A2 (built):** each tab's running command and when it started, for the Move confirmation: read by the box helper from `/proc` (`Tab.command`, one line, at most 160 characters; `Tab.running_since`), shown on this Mac only, never logged; the app's tab description leaves the command out (tested with a planted `-pSECRET`).
- **B, the app's three sheets (built).** OpenBox's refusal opens the sheet at once, naming the place (`GetPlacement` without the probe), and the probe fills it in; Connect to it there is OpenBox's `connect_there`; Move it to this Mac is MoveBox; Move to This Mac takes over (rule 4); Keep Waiting tries the open again every 30 s and brings the sheet back, offering Connect, when the server answers. Move To asks first, with every running command. Defaults: Connect on the lease sheet, Move on the confirmation. When the place doesn't answer, Move to This Mac is the default only for a known server whose ping failed and whose newest save is under an hour old; a holder that can't be asked never makes it the default: Keep Waiting is, and Move to This Mac says "Another Mac may still be working in it. Anything it hasn't saved yet stays there, and is kept as a separate save when it next saves." (R-0168). "Look without changing anything" is hidden until 1.4's Browse Saves.

**2.4 Stand-in agents over SSH and the CLI** (no control plane, no lanes)

- Doors 1 and 2 for a box on the user's own server, in Continue my work mode. Grok Bot, or any terminal agent, reaches the box, runs Claude Code there and relays its questions back.
- Credentials (ADR 0007): enrolment is closed by default; Connect an agent opens it for 10 minutes or until one code is approved. The agent runs `portenv connect`; the user approves the code in the app, after seeing the agent's name, the box and the key fingerprint; the runner, as certificate authority for its boxes, issues a 30-minute SSH certificate scoped to that box and mode, renewed by proof of possession of the agent's key until the grant ends (default 8 hours, at most 24 hours until Phase 3). No new inbound port: everything goes through the server's existing SSH. The app lists every certificate with revoke.
- The core in the box agent: events (waiting for input, command finished, new output), `portenv events --follow`, `portenv screen <tab>`, `portenv box send <tab>`, `portenv wait`, and `docs/skill/SKILL.md`.
- `portenv ask` for the owner's input, and secret prompts never relayed (Agent readiness, principle 5).
- ADR 0016's foundations: the agent identity, pairing with `portenv connect`, the credential record, grants with their audit trail, the message type, the event log and the names.
- Agent readiness (see that section):
  - the command registry, and everything generated from it, with the CI drift check;
  - `portenv guide` and `--json`;
  - the skill, with its generated reference;
  - the box introducing itself (`PORTENV_BOX`, `PORTENV_GUIDE`, the login banner, and Portenv's own `AGENTS.md`, never touching the user's files; ADR at 2.4);
  - `tests/agent-evals/`.
- Attribution and session recording per agent, the save point on connect, Watch, Take Over and Revoke in the app.
- **Connect an Agent: the user chooses the door, per agent and per box** (the door table is in GUIDELINES.md §6.1):
  - Portenv never picks silently. The sheet states, in plain language, what each door allows before connecting.
  - **At launch the sheet offers stand-in** (over SSH, the CLI or the web terminal) **and MCP** (stdio or remote, with per-tool approvals). The lane row appears only once lanes exist (4.1), and is never shown as available before then.
  - **A default suggestion only:** MCP for agents the user hasn't used before (a lane too, once lanes exist), and stand-in when they trust the agent like themselves. The user can always choose otherwise.
  - **Changing an agent's door or mode** takes effect immediately and is recorded.
  - **The relay rule:** when one agent's command is refused (by its own safety checks or its permissions), the refusal goes to the user with the reason. Nothing automatically hands the command to another agent. A re-run happens only on the user's explicit instruction, and it's attributed as "you, through <agent>", next to the original refusal.
  - **SSH command rules are never described as a security boundary,** anywhere: the app, the docs, the website. A shell can rephrase any command. What holds is the recording, Revert, and the vault rules enforced by the operating system.
  - Each door appears as it lands: stand-in here (2.4), MCP in 2.6 and 2.7, the web terminal in 2.8, lanes in 4.1.
- **Before 2.4 (requirement, owner 2026-10-10):** each viewer keeps its own current tab. Every agent session attaches as its own viewer (a grouped tmux session over the box's windows), so an agent switching tabs never changes the owner's current tab. Built in 1.2 with the tab bar; 2.4 gives each agent its own viewer name.
- Not in 2.4: lanes (4.1), approvals (4.2), the gateway and its CA (Phase 3), phone notifications (4.6).
- **Open question:** a per-box "Keep running when Portenv quits" option, off by default, so a stand-in agent's work can go on after the app quits, with a menu bar item listing the boxes still running. Only worth building with Phase 3's lease hand-off, so a sleeping Mac never blocks resuming the box elsewhere. No ADR until then.

**How agents get the skill and the CLI** (built with 2.4)

- **The skill:** `skills/portenv/SKILL.md` in this repository, in the Agent Skills format (a folder with `SKILL.md` and frontmatter), versioned with the CLI. CI fails if a command the skill uses does not exist in that CLI version. It covers connecting with a device code, events, screen and send, waiting for input, relaying questions to the user, and revoking itself. It never contains credentials, and it tells the agent to install the CLI only from `portenv.com/cli` and to verify it. `portenv.com/skill` redirects to the `SKILL.md` attached to the latest release (the same pattern as the DMG and the appcast).
- **The CLI for agents' computers:** release assets are static `portenv` binaries for linux/arm64, linux/amd64 and macOS arm64, plus a signed `checksums.txt` (ADR 0009). `portenv.com/cli` serves a short install script that detects the platform, downloads the release binary, verifies the signature and checksum, and installs into the user's home without `sudo`.
- **Installing the skill:** `portenv skill install` writes the skill matching the CLI's own version to `~/.agents/skills/portenv/` (read by Cursor, and by Grok Bot through its support for Cursor's skills); `--path` covers other agents' folders; `portenv skill` prints it.
- **Connect an agent** in the app shows one message to copy and paste to the agent, for example: "Install the Portenv CLI from portenv.com/cli, run `portenv skill install`, then connect to my box acme-api with code K7F2-9QX4." Opening the sheet opens the enrolment window (ADR 0007: 10 minutes or one approved code); when the agent requests the code, the approval sheet follows.
- **Later, not in 2.4:** a plugin marketplace repository carrying the skill. The MCP door needs no skill: its tools describe themselves.

**2.5 Webhooks**: signed event pushes from the box agent or runner (never a Portenv server) to a URL the user registers for their agent. Payloads describe themselves: the event type, the box, a link to the event's docs, and the `portenv` command to act on it. Settings and the signing key live in `~/.portenv` in the box, so they are encrypted and move with it. 2.5 also delivers messages between the same owner's agents, locally through `portenvd` or the runner (ADR 0016, point 6). Before building it, confirm the target agents can be woken by an incoming webhook (Open questions).

**2.6 stdio MCP**: `portenv mcp` on the agent's computer exposes events, screen and send over the CLI's connection.
- Tool descriptions and schemas are generated from the registry, with `readOnlyHint` and `destructiveHint` per tool.
- The guide is an MCP resource.
- The server is listed in the official MCP Registry.
- It proves the tools without any public exposure, before 2.7.

**2.7 Remote MCP on the runner**: MCP over HTTPS with OAuth, served by the runner on the user's own server, free, opt-in per server (ADR 0015). It's ChatGPT's way in (Dots), through the ChatGPT app (Apps SDK). It uses the same registry and tools as 2.6, per-agent sign-in, attribution and revoke. ChatGPT's OAuth sign-in to the runner is the pairing (ADR 0016): the runner's authorization page shows the code and "Approve this in your Portenv app", and the owner's approval in the app is the OAuth consent.

**2.8 Web terminal on the runner**: the box's terminal in a browser, served by the runner, opt-in per server (ADR 0015). It's the way in for browser-only agents (Meta Muse). Each agent gets one-time links that expire within minutes, tied to one agent and one box. Attribution and revoke work as for the other doors.
- **Readable by browser agents:**
  - xterm.js's DOM renderer or its screen-reader mode, so the screen exists as page text;
  - larger-font and high-contrast options;
  - the box's state line as plain page text, outside the terminal.

**The relay loop across doors:** `portenv answer <tab> <text>` (and its MCP tool) sends the user's answer as keystrokes, attributed to the user through the relaying agent. Waiting-for-input events reach the agent through webhooks (2.5), `portenv events --follow`, or MCP.

- [ ] Move To works Mac → server → Mac from the title menu with no data loss
- [ ] Every lease sheet path behaves as specified, including a stale lease
- [ ] A server reboot leaves boxes recoverable without user intervention
- [ ] The wizard explains every preflight failure in plain language
- [ ] A box, even as root, cannot delete or overwrite any existing save: with the box's credential, deleting, renaming or rewriting any repository file (snapshots, index, data, keys, config) fails, and `restic check` passes afterwards (2.2)
- [ ] Forget, prune and unlocking run only with the host's credential, which no box ever receives (2.2)
- [ ] A stand-in agent connects from its own computer with a short-lived certificate, drives the user's Claude Code session by keystrokes in `/home/work/<project>`, and the user sees its activity attributed by name
- [ ] Relay loop: a tool in the box asks a y/n question; "waiting for input" fires; a test agent reads the screen, relays the question, sends "y"; the tool continues. Run once with Claude Code and once with a plain script, using only `portenv events`, `portenv screen` and `portenv box send`
- [ ] Revert To ▸ Last Save Point undoes everything a stand-in did since it connected
- [ ] Revoke ends the agent's session immediately and its certificate can no longer be renewed
- [ ] Every condition in ADR 0007 has its test passing (CA key never leaves the server, one box and one mode per certificate, no new inbound port, `portenv-enroll` closed by default, locked down and silent, device codes expire, are single use and rate-limited, renewal bound to the agent's key, revoke kills renewal and sessions)
- [ ] A webhook reaches the registered URL with a valid signature when a tab starts waiting for input (2.5)
- [ ] The stdio MCP server answers events, screen and send for a connected agent (2.6)
- [ ] On a clean Linux machine standing in for an agent's computer, the message pasted from Connect an agent alone gets an agent from nothing to connected and relaying a Claude Code question, with no other help
- [ ] The install script refuses a binary whose checksum or signature does not verify (ADR 0009), and CI fails if the skill uses a command the CLI does not have
- [ ] 2.4 agent readiness:
  - every command has `--json`, a stable exit code and an example in its help;
  - `portenv guide` exists;
  - the skill installs to `~/.agents/skills/portenv`;
  - inside a box, `PORTENV_BOX` is set, and the agent-facing intro reaches Claude Code and Codex without touching the user's own files;
  - the CI drift check passes.
- [ ] 2.4: the agent evals pass at least 8 of 10 tasks on Claude Code and on Codex, each task run 3 to 5 times; every safety task passes on every run, on every agent tested
- [ ] 2.4: with stdout not a TTY, no command prints a spinner, redraw, colour code or pager, or asks a question; `NO_COLOR` is respected; `--yes` confirms
- [ ] 2.4: `portenv ask` blocks until answered and exits non-zero with a clear message on timeout; `portenv wait` returns on a question, idle or done
- [ ] 2.4: a sudo password prompt and an ssh passphrase prompt are reported only as "A password is needed; answer it in the terminal."; nothing reaches a relay, webhook or agent, and an answer sent through any relay to a secret prompt is refused
- [ ] 2.4: `portenv connect` pairs an agent from a terminal with no browser and no localhost redirect; approving creates its identity, grant (with an audit entry) and credential
- [ ] 2.6: the MCP tools are generated from the registry, with annotations, and the server is listed in the MCP Registry
- [ ] Website: `llms.txt`, `llms-full.txt` and the `.md` pages are live and match the generated docs
- [ ] 2.7: remote MCP over HTTPS with OAuth, served by the runner, works for a ChatGPT connector, under every condition of ADR 0015
- [ ] 2.8: the web terminal served by the runner opens only with a valid one-time link, and every keystroke is attributed to its agent
- [ ] 2.8: the web terminal's screen and the box's state line are readable as page text by a browser agent
- [ ] `portenv answer`, in the CLI and in MCP, sends an answer attributed to the user through the relaying agent
- [ ] "Close public doors" in the app and the CLI closes both public doors at once, and they close by themselves after 14 days without use

### Phase 3: Control plane and gateway

Milestones: 3.1 accounts (Apple, GitHub, email) · 3.2 device enrollment and wrapped keys · 3.3 authoritative leases · 3.4 box agent tunnels and gateway routing by box name · 3.5 Portenv storage (Cloudflare R2, one prefix per box, short-lived prefix-scoped credentials minted per device, quotas by refusing credentials, retention and pruning on the user's devices; ADR 0008) · 3.6 push notifications · 3.7 full first run (sign-in, Portenv storage, recoverable mode) and the one-time account prompt for existing users · 3.8 SSH door through the gateway (Portenv CA certificates, ports 22 and 443, `portenv ssh` over HTTPS; needs 3.1, 3.2, 3.4) · 3.9 Portenv-hosted remote MCP with OAuth, for users without a public server; the runner serves it free from 2.7 (needs 3.1, 3.4 and the 2.4 core; not lanes, approvals or the vault) · 3.10 Portenv-hosted web terminal on portenv.com with passkeys, for users without a public server; the runner serves one free from 2.8 (needs 3.1, 3.4).

- [ ] A box is reachable by name wherever it runs, with no inbound ports anywhere
- [ ] Revoking a device ends its sessions within 5 seconds
- [ ] A control plane outage does not interrupt local work or saves to the user's own storage
- [ ] An existing Phase 1 or 2 user creates an account and links their devices without losing a box or re-entering keys
- [ ] An agent reaches a box by name through the gateway over SSH (22 and 443) and over remote MCP, with the same attribution and revoke as 2.4
- [ ] The runner CAs are retired: their trust removed and keys destroyed, and the migration test passes (ADR 0007)

### Phase 4: Agent access

Milestones: 4.1 lanes (users, tmux, clones, limits) everywhere a box runs, and stand-in mode on local and cloud boxes · 4.2 opt-in approval helper (push, delete, secrets, protected scripts, sudo in a lane) · 4.3 secrets vault · 4.4 full command audit (auditd or eBPF) on top of 2.4's session recording · 4.5 remaining doors, only if usage shows the need: GitHub bridge, chat or email bridges, A2A (ADR 0006) · 4.6 iPhone companion for approvals.

- [ ] An agent in a third-party sandbox connects with a device code and works in its lane
- [ ] A lane agent cannot read the user's home
- [ ] It cannot read other homes or ungranted secrets, verified by an adversarial test suite
- [ ] A `sudo` request reaches the iPhone and resumes on approval
- [ ] Revoke ends the agent's session immediately

### Phase 5: Portenv Cloud

Milestones: 5.1 microVM driver: Firecracker or Cloud Hypervisor, chosen by a spike (the first launch may use a rented provider behind the same driver) · 5.2 one microVM per box and one tenant per VM, a shared read-only toolbox image, `/home` restored onto local disk encrypted per box · 5.3 wake on demand (with end-to-end approval) and idle stop (save and stop after a set time; agent keystrokes count as activity) · 5.4 Duplicate for a Task (fast clones from a snapshot). Placement is a simple scheduler in the control-plane database that prefers the host with the box's `/home` cached; draining a host moves its boxes. Cloud-side work for this phase lives in `portenv/cloud`.

- [ ] A box starts on Portenv Cloud in seconds from its latest save
- [ ] Ten clones of one box run in parallel, isolated from each other
- [ ] Tenant isolation passes an external review
- [ ] A stopped box costs only storage, and a dead host loses at most the work since the last autosave
- [ ] Abuse controls are live: outbound port 25 blocked, sustained full-CPU detection

**Later:** teams and shared servers, Create a server for me through provider APIs, Linux and Windows apps, a self-hosted control plane for enterprises.

## Testing and quality

The save engine is tested hardest, because a bug there loses someone's work; everything else is tested to the level its failure would cost.

| Area | How | Gate |
| --- | --- | --- |
| Sync and leases | Table-driven tests over every resume rule and invariant; property tests with random sequences of edits, saves and machine switches | Every commit |
| Two-machine scenarios | e2e harness with two simulated machines against real restic repositories (local folder, MinIO for S3, SFTP container) | Every merge |
| Crash safety | Kill `portenvd` or restic at random points during save and restore; cut the network mid-upload | Nightly |
| Drivers | One conformance suite run against `docker`, `apple` and `microvm` | Every merge touching a driver |
| Agent channel secrets (ADR 0010) | `TestChannelConditions`: targeted checks of every place the channel token could land, each proved by a planted decoy; 20 runs under CPU, memory and disk load (`channel-stress`). `TestChannelTokenNotOnDisk`: the box's whole root file system; a timeout is a failure, and a failure opens the issue "Nightly channel scan failed" with a redacted excerpt, closed by the next passing run | Targeted: every PR touching the driver, the agent or the image. Full scan: nightly |
| Keys | Device add, revoke, recovery, re-encrypt, Delete Box (keys go only with the saves: no box is left with saves but no key); verify no key material on disk outside approved stores | Every merge touching keys |
| Agent isolation | Adversarial suite: an agent lane tries to read other homes and secrets, escalate privilege, reach blocked hosts, persist after revoke | Every merge from Phase 4 |
| Mac app | XCUITest for first run, title menu, lease sheet; accessibility audit; light and dark snapshots | Every merge touching the app |
| Performance | Budgets: resume under 5 s to a ready box (median of 5, worst reported), online at a round-trip time of 50 ms or less and offline; close or move with a 5 MB change under 15 s at 20 Mbit/s up and 50 ms; newest save at most 60 s behind with continuous editing; port relay adds under 5 ms locally. Bandwidth and round-trip time are measured and reported with every result | Nightly |
| Recovery drill | Restore a box from the recovery key alone on a clean Mac (ADR 0019) | Before each release |
| Own route | `own-route` job: the own-route journey with every Portenv-hosted endpoint blocked at DNS and the firewall, from locally built artifacts; from Phase 1 it also drives the app with the hosted options visible and untouched, and from Phase 3 completes first run through Continue without an account (ADR 0008, G3) | Every pull request and merge to main; blocks releases |

CI runs the Go suites on Linux arm64 and amd64 and the app suites on macOS arm64 runners. A release is blocked by any failing gate.

Behaviour the app relies on is tested through the path the app uses: `portenvd` (or the runner) and the box agent's channel. `docker exec` in a test only inspects or simulates a failure, and `tests/e2e/daemon.sh` fails on any other exec into the box. Each such check has been shown to fail when the code it covers is broken (a mutation check), and a test that can't fail is a bug in the test. Every e2e call has a time limit, so a hang fails a check.

## Open questions

These need an owner decision; Claude Code should add new ones here instead of guessing.

- [x] Debian 12 for Add a Server: ADR 0021 lists it, but `server/setup.sh` installs on Ubuntu only (Docker's Ubuntu repository, `dpkg`), so Check accepts Ubuntu 22.04 and 24.04 for now (2.9 PR 2a). Support Debian 12 in `setup.sh`, or drop it from the ADR? **Answered (the reviewer, on the owner's delegation, R-0212, 2026-10-14):** dropped for the tester build: Ubuntu 22.04 and 24.04 for now, Debian when a tester asks; Check's failure line names the supported list.

- [x] Default protection mode for new users: Only you (current default) or recoverable? **Answered (the reviewer, on the owner's delegation, R-0087, 2026-10-11):** Only you, with the recovery key required at first run (ADR 0019).
- [ ] Phase 2: install the runner as a service from the start, or plain SSH first? The plan assumes the runner.
- [ ] Before the public release: lower the app's deployment target from macOS 26 to macOS 15 (gRPC Swift 2's floor, ADR 0014), so older Macs can use the Docker fallback? Today the app targets macOS 26 and doesn't launch on them. Intel Macs would also need a universal build.
- [ ] Trademark and domain check for Portenv in the EU and US; confirm portenv.com is registrable.
- [ ] Docker inside the box (Phase 1): how to provide it without granting the box `CAP_SYS_ADMIN` or privileged mode (for example rootless Docker or a nested sandbox), or whether the setting ships with a plain warning that it weakens the repository-password protection (ADR 0005).
- [ ] Point-in-time autosaves while the box runs: worth a file system with snapshots (for example btrfs on the Apple disk image), or are file-consistent saves plus the pre-save hook enough? (ADR 0005)
- [ ] Before each door is built: confirm the target agents can actually reach it (for example outbound SSH from their computers for door 1).
- [ ] Does Grok Bot read `~/.agents/skills` and can it run the install script? (The owner is testing it.)
- [ ] tmux (3.4 and 3.7c) sometimes drops a pane's last output in control mode when the program exits at once: the pane is gone before tmux writes the queued output, about 2 times in 100 under load. The app's tab terminal then misses it, although the tab's own history is fine until the window closes. The helper delivers everything tmux sends (tested). A fix would set `remain-on-exit` on, then have the helper close a dead pane once its own control client reports it dead. A subscription to `#{pane_dead}` arrives after the pane's output. That changes when tabs close, so it needs a decision. (1.2c PR 3)
- [ ] Before 2.5: can the target agents be woken by an incoming webhook? If not, an agent keeping `portenv events --follow` running does the job and 2.5 drops in priority (ADR 0008).
- [x] Apple Developer Program enrolment, needed for Developer ID signing and notarization in 1.8. **Answered (2026-10-10):** Portenv is signed with the owner's existing organization team; no new enrolment is needed (see 1.8).
- [ ] Approval timeout default (30 minutes assumed) and what happens when it expires.
- [ ] A stale toolbox image (R-0302): the Docker driver pulls only when the tag is missing locally, so a Mac with an old `:main` silently creates workspaces with an old helper. Should the driver compare the local `:main` digest with the registry's before a create, or should the app build pin the image digest CI published for its commit?
- [ ] machine-id race (R-0299): `core/local` writes `machine-id` with a plain `os.WriteFile`, so two processes starting together on a fresh Mac can read it empty ("sync: invalid machine ID"). The fix is a temporary file and a rename; a small fix after the 2026-10-18 demo.
- [ ] `share_login` (ADR 0022 §2) isn't in the tester build (R-0323): the key is parsed and checked but shares nothing, and the agent's column and its `whoami` say so. Build it after the tester build as the ADR describes (one decision per agent, the tool's credential file copied 0600), or drop it in favour of each agent signing in itself? Copying a subscription login also depends on the next question.
- [ ] Agents' clones (ADR 0022 section 2a, R-0315): how the owner sees an agent's branch in the app. A Fetch button on the agent's row, a fetch Portenv makes on every `portenv share`, or both; and whether the app shows the branch's diff itself or leaves it to the owner's git tools.
- [ ] Agents' clones (R-0315): do read-only lenses (reviewer, design, trust-safety) need their own clone in the first build, to read a writer's history with the safe fetch, or is reading the writer's files through `read_clones` enough for now?
- [ ] Agents' clones (R-0315): agents keep read access to the owner's checkout, which they need to clone and fetch from it, so they can still read untracked files there (`.env`, local settings). Keep that, or seed clones from a bundle `work` makes, so agents need no read access to the project at all?
- [ ] Agents' clones (R-0315): a full clone counts toward the agent's home limit (2 GB by default). Raise the default for agents with `checkout: own`, give clones their own allowance, or keep one limit and let the start's plain line ask the owner to raise it?
- [ ] Anthropic's terms for agents driving Claude Code with a subscription login versus a Console API key.
- [ ] Open-source boundary in detail: this repository is Apache-2.0 and `portenv/cloud` is private (decided), but confirm before going public whether the Mac app, the File Provider and the iPhone companion stay in the public repository or move to a private one.
- [ ] Phase 3 control-plane milestones now live in `portenv/cloud`: how its CI pins and tests against `proto/gen/go` versions from this repository (tags, or a pseudo-version per merge).
- [ ] Scope of the iPhone companion: approvals only, or also status and a read-only terminal?
- [ ] Name of the CLI binary: `portenv` assumed.
- [ ] Linux desktop window, after the terminal edition ships on Linux: Tauri with xterm.js (shared with 3.10's web terminal), or native GTK?
- [x] Before Phase 2, telemetry: what opt-in, anonymous usage counts may Portenv collect, if any, and how is that stated on the website? **Answered for counting (2026-10-09):** download counts, repository traffic archived daily, and usage counts through the update check, from a fixed, published list of coarse fields with no identifier, on by default with one click off (see 1.8). Anything beyond these counts stays opt-in and undecided.
- [ ] For 2.4–2.6: publish the real CLI binary through npm (platform packages, esbuild-style) and PyPI, so `npx portenv` and `uvx portenv` work in agent sandboxes that only reach package registries, and so MCP clients can start it the way they usually start servers?
- [ ] Private agent network (Phase 3 and later): agents following and messaging each other across owners, on ADR 0016's identity, grants and messages. What it offers, and whether it's built at all.
- [x] CLI names (ADR 0016): keep short top-level forms as aliases of `portenv box …`? **Answered (2026-10-10):** yes, as the primary way in, for the everyday verbs only (open, status, save, ask, answer, wait, screen, events, connect, guide). Each is defined once in the command registry as an alias of its grouped form, so help and the generated docs show both. Everything else lives only under its group.
- [x] Remote MCP (2.7): how does the owner's pairing approval fit ChatGPT's OAuth sign-in? **Answered (2026-10-10):** the sign-in is the pairing. The runner's authorization page shows the code and "Approve this in your Portenv app", and the owner's approval in the app is the OAuth consent, creating the identity, grant and credential as `portenv connect` does. One pairing flow for every door.
- [ ] Which group holds the commands outside the everyday list that the plan names today (`portenv skill`, `portenv mcp`, `portenv event`, and the Phase 0 `portenv init`): `portenv box …`, `portenv agent …`, or a new group?
- [x] First run's recovery key (1.5): its format (the mockup shows 24 characters in six groups of four, from a 32-letter alphabet without look-alikes), and how it becomes each box's extra restic key (for example, a key derived per box from the recovery key with a slow KDF and the box's ID). A data-loss path: an ADR and tests before code. **Answered (the reviewer, on the owner's delegation, R-0087, 2026-10-11):** the mockup's format, from `SecRandomCopyBytes`, with a key derived per box ID. ADR 0019 (proposed) works out the rest: the alphabet without 0, 1, I and O; the last character a check (115 random bits); HKDF-SHA256, since restic's own scrypt already slows guesses; and a pool of 32 pre-derived passwords, so the app never keeps the recovery key.
- [x] Making boxes from the app (1.5): only the app may raise Keychain prompts (ADR 0013), but portenvd owns box configs and storage. Proposed: the app makes the keys and puts them in the Keychain, then hands them to portenvd (as ProvideKeys does) with a new CreateBox call that writes the config and sets up storage. Needs an ADR. **Answered (the reviewer, on the owner's delegation, R-0087, 2026-10-11):** as proposed. ADR 0019 (proposed) gives the call and its order: the Keychain first, then the repository, the recovery key added and checked, and the config last. So a box can never save without both keys, and a retry with the same box ID resumes.
- [x] "Save to Passwords" on the recovery key step: macOS has no public API for an app to add an item to the Passwords app. Keep Print and Copy only, or find another way? **Answered (the reviewer, on the owner's delegation, R-0087, 2026-10-11):** dropped. Print and Copy, plus the "I've saved" checkbox (ADR 0019).
- [x] Restoring with only the recovery key (ADR 0019): where the app offers it on a clean Mac. **Answered (the reviewer, on the owner's delegation, R-0092, 2026-10-11):** "Restore with a Recovery Key…" as a plain text button under Welcome's main actions, and in Settings › Protection and recovery key. Not needed for the design testers; the board comes before the UI. The `RecoverBox` call and its test come with `CreateBox`.
- [x] ~~S3-compatible storage from the app (first run's Storage step)~~. **Dropped (the owner, 2026-10-14, R-0196):** bringing your own bucket is too technical for the people Portenv is for. The app never offers it; the CLI and portenvd keep the internal `bucket` storage kind, unexposed. Off-Mac saves without a server come from Portenv's own storage later (Decisions already made, R-0197).
- [x] ADR 0020, decision 9: with control mode, escape sequences a program prints reach the app's terminal instead of stopping at tmux. Is filtering them in the app the same guarantee, or a change to a security default? **Answered (the reviewer, on the owner's delegation, R-0133, 2026-10-13):** the same or stronger, provided every one of these is in:
  - a default-deny filter in front of the terminal;
  - no reply from the emulator reaches the pane;
  - paste cleaned;
  - tmux's `allow-passthrough` and `set-clipboard` off and asserted;
  - fixed memory limits.

  Dropping any of them makes it the owner's decision. The check on main found that `set-clipboard external` let any program set the Mac's clipboard through `tmux set-buffer -w`, fixed in #94; links are limited to web pages on the person's click (R-0134).
- [ ] ADR 0020: should agents' terminal access (2.4) move to the `TabTerminal` stream as well, for per-author attribution of their keystrokes, or stay on today's attach?
- [ ] A per-box "Keep running when Portenv quits" option (off by default) with a menu bar item listing boxes still running? Only with Phase 3's lease hand-off (see 2.4).
- [x] Tab keyboard shortcuts (1.2)? **Answered (2026-10-10):**
  - ⌘T opens a new tab, ⌘1–⌘9 choose a tab, ⌘⇧[ / ⌘⇧] go to the previous / next tab.
  - ⌘W closes the current tab when there's more than one; on the last tab it closes the window (save and release), like Terminal and Safari.
  - Closing a tab with a running program asks first: "Close this tab? <program> is still running."
  - In GUIDELINES.md §4.1; built in 1.2.
- [x] Should each viewer keep its own current tab? **Answered (2026-10-10):** yes. One grouped tmux session per viewer (the app, each agent), all on the box's windows (`tmux new-session -t <base>`). An agent switching tabs must never change the owner's current tab. Built in 1.2, and a requirement before 2.4.
- [x] Reordering tabs? **Answered (2026-10-10):** later, by drag in the tab bar, kept as tmux's window order. Not built in 1.2.
