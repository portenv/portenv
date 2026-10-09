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
| Leases | One machine holds a box open at a time; control plane is authoritative, snapshot tags are the offline fallback | Prevents silent overwrites |
| Encryption | Snapshots encrypted client-side; working copies on encrypted disks; per-device keys; recovery key | Homes hold secrets |
| Networking | Outbound tunnels from box agents and runners to a Portenv gateway; no inbound ports; Tailscale not used | Product-owned, multi-tenant, works behind NAT |
| Agents | One core in the box (events, screen, send, wait for input) behind thin doors: SSH, the CLI with a published skill, webhooks, MCP, a web terminal; every door shares login, short-lived per-box credentials, attribution and revoke. Two modes: stand-in (the user's access, no approval prompts by default) and lane. Approvals are opt-in per action (ADR 0006) | Any agent that can open a terminal or use a connector can work in a box |
| Finder | File Provider extension shows each box in Documents › Portenv › \<box> | Native, works when the box runs remotely |
| Developer servers | An open-source runner, installed by an in-app wizard | Full control for developers, same features |
| Ownership | The user owns every box; agents are guests the user invites. Any agent that can open a terminal or use a connector can join, and be connected and disconnected without moving work; nothing is specific to one agent. Work done by agents stays in the user's box, and history attributes every change to the agent that made it. Portenv ships no agent of its own (ADR 0006) | Users keep their work and choose their agents freely |
| Backend | No Portenv backend before Phase 3: Phases 0 to 2 run only on the user's Mac, servers and storage. Everything that runs on your own machines and storage (the app, local boxes, your servers, your storage, SSH and CLI access, webhooks from the box) works without Portenv's servers or an account. Hosted services (gateway, push, remote MCP, web terminal, Portenv storage, Portenv Cloud) are separate. (ADR 0008) | The product works without Portenv's servers |
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

**Product logic lives in `portenvd`, not in an app** (rule from 2026-10-09).
- **What it covers:** the state line's wording, the menu model, and which actions are available when.
- **Why:** the Mac app, the terminal edition and any later app then show the same thing.
- **To do:** move today's Swift `Guidelines.swift` logic (the state-line table, `BoxMenuModel`, server names) into `portenvd` when convenient, with its tests. The apps then only display what `portenvd` sends.

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
- Before Move To, before closing the window, before an approved privileged action, on ⌘S (Make Save Point), and as a save point when a stand-in agent connects (so Revert To ▸ Last Save Point undoes its work).
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

**Append-only storage credentials (milestone 2.6, before any outside agent gets access)**

The storage credential that enters a box (today the SFTP key served by the agent during a restic run) can delete and overwrite repository files, so root in a box during a run could destroy history. Proposal:

- The box's credential becomes append-only: it can add files but never delete or overwrite them. Forget, prune and unlocking stale locks use a separate credential that never enters a box and lives only on the host.
- For SFTP storage, plain `sshd` cannot enforce append-only. The clean way is restic's REST server (`rest-server --append-only`), which already runs on servers since 0.5 for the host's own calls: boxes move from SFTP to an append-only REST user, the host keeps a full user for forget and prune, and the boxes' SFTP access ends. No new inbound port: it is tunnelled over the existing SSH.
- For S3 (and Portenv storage on R2), the same split uses two credentials: put-only for boxes, full for the host.
- Cost: restic's lock removal must happen on the host (append-only clients cannot delete their own locks). The REST server process already exists (0.5).
- A box's SFTP upload is round-trip bound: in the 0.5 traces a 5 MB backup spent 5 to 6 s reading and uploading on a 31 to 33 Mbit/s link (about one 32 KB SFTP write per round trip), against about 1.3 s for the bytes. Moving box backups to REST here should remove most of it; measure it in 2.6.

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
6. Delete a box: Delete Box asks whether to delete its saves too. A box's keys (every device's repository key, its REST password, its storage SSH key) are removed only together with its saves. If the saves stay (for example in the user's own bucket), the keys stay as well, and the box remains restorable from Browse Saves or with the recovery key. Invariant, tested when Delete Box is built: no box ends up with saves but no key on any device or in the recovery key.

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
| Lane | Has its own user (`/home/<agent>`), tmux session, git worktree on branch `agent/<name>`, and its own limits | No access to the user's files or sessions. For agents the user wants kept separate |

In both modes every session is recorded and tagged with the agent's name, there is one writer per terminal with explicit Take Over, and Revoke ends access and live sessions immediately.

**No policing in stand-in mode**

Stand-in mode has no approval prompts by default. What it has is for the user's benefit:

- Attribution: who typed what.
- Save points: one is made when a stand-in connects, so Revert To ▸ Last Save Point undoes an agent's work.
- One-switch revoke.

Approvals are opt-in, per action (for example `git push`, deleting, production secrets), mainly to guard against prompt injection and misheard voice commands. Limits apply to lanes. Enforced for every agent, stand-in included: no agent can extract encryption keys or the restic password (ADR 0005), and no agent can reach any box but the one it was invited to.

**Lanes**

- One Linux user per agent (`/home/<agent>`), its own tmux session, and its own git worktree on branch `agent/<agent>`. A read-only view of the main checkout is mounted at `/home/<agent>/main`.
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
- Agent-facing commands: `portenv events --follow` (JSON lines), `portenv screen <tab>` (the current screen as text), `portenv send <tab> "<text>"`. `docs/skill/SKILL.md` teaches the loop to any agent and names no specific tool.
- How an agent asks its user (voice, chat) and what it answers on its own is between the user and their agent. Portenv does not enforce it.

**Doors: one core, thin doors**

Every door reaches the same core with the same login, short-lived per-box credentials, attribution and revoke; no door may bypass any of these. The HTTP API is the base the doors share and is built as part of doors 2 to 4. In build order:

1. SSH on ports 22 and 443 with short-lived certificates (2.7 through the runner on the user's server; 3.8 through the gateway).
2. The `portenv` CLI and a published skill: agents install the CLI on their own computer, sign in with a device code, and the skill teaches events, screen and send (2.7).
3. Webhooks, sent by the box's agent or runner (not a Portenv server): signed pushes of events (waiting for input, command finished) to a URL the user registers for their agent, so trigger-driven agents wake without polling (2.8).
4. MCP over the same core: stdio first, through the CLI on the agent's computer (2.9); remote with OAuth on the gateway (3.9), which becomes the ChatGPT plugin.
5. A web terminal on portenv.com for browser-only agents and for the user on any computer or phone, with passkeys (3.10).
6. Later, only if usage shows the need: a GitHub bridge (the box pushes a branch, Codex works, the box pulls), chat or email bridges (every reply carries a signed, single-use token), A2A (4.5).

First target agents:

- **Grok Bot**: terminal, through SSH and the CLI.
- **ChatGPT Dots**: through a plugin built on the remote MCP door.
- **Meta Muse**: watch; no way in yet.

**Connecting an agent**

1. Agents › Connect an agent: name, box, mode (stand-in or lane), opt-in approvals, expiry.
2. The agent runs `portenv login`, which prints a device code; the owner approves it on the Mac (or iPhone, from Phase 4). The agent receives a short-lived certificate for that box and mode: issued by the runner on the user's server in Phase 2, by the Portenv CA from Phase 3.
3. Portenv shows a ready-to-paste snippet: the published skill document (how to use a Portenv box) plus connection details.
4. Revoke ends access and live sessions immediately.

**Claude Code inside boxes**

Agents driving Claude Code headlessly use `claude -p` with `--resume`, `--output-format json` and `--allowedTools`. Boxes are logged in with the owner's account; heavy or multi-user automation should use a Console API key instead. Check Anthropic's terms for automated use before shipping this feature.

## Agent readiness

**The goal:** any agent can discover Portenv, learn its tools, and know where it is once inside a box. All of it comes from one source of truth, and real agents test it.

**Principles**
1. **One source.** Every command is defined once in `portenvd`: the proto, plus a command registry with each command's name, summary, arguments, an example, and whether it's read-only or destructive. Everything else is generated from the registry, and a CI check fails if any generated file drifts:
   - the CLI's help;
   - `portenv guide`;
   - the skill's reference section;
   - MCP tool descriptions;
   - `llms.txt`;
   - the docs.
2. **Machine-readable by default.** Every CLI command supports `--json`, uses stable exit codes (documented in one table), and never asks an interactive question without a TTY.
3. **Errors say what to do next:** what happened plus the next step, as one plain line, in the style of "Keychain needs your approval. Open Portenv on this Mac to allow it." (GUIDELINES.md §10).
4. **Agents read, they don't obey.** Everything Portenv shows an agent is written by Portenv. User files in a box can never change the guide. Docs never tell an agent to skip approvals or the vault rules (ADR 0005).

**Where each piece lands**
- **Now (Phase 1): rules only.** Principles 2 and 3 apply to every command added or changed from 2026-10-09. No other work.
- **2.7 (stand-in agents, CLI and skill):**
  - `portenv guide`, plus `portenv guide --json`: a compact guide written for agents, generated from the registry.
  - The skill (`skills/portenv/SKILL.md`): a short trigger description, common workflows, and reference files generated from the registry.
  - **The box introduces itself:**
    - the environment variables `PORTENV_BOX=<name>` and `PORTENV_GUIDE=<path>`;
    - a one-line SSH login banner for agent sessions, pointing to `portenv guide`;
    - an `AGENTS.md` written by Portenv, which agents pick up automatically. It must never overwrite or edit the user's own `AGENTS.md` or `CLAUDE.md`. It uses a location Portenv owns, or a clearly marked block added only with the user's consent, and the user can turn it off. The mechanism is proposed in an ADR at 2.7.
- **2.8 (webhooks):** payloads describe themselves: the event type, the box, a link to the event's docs, and the `portenv` command to act on it.
- **2.9 (stdio MCP):**
  - tool descriptions and schemas generated from the registry;
  - `readOnlyHint` and `destructiveHint` set per tool;
  - the guide exposed as an MCP resource;
  - the server listed in the official MCP Registry.
- **3.9 (remote MCP):** the ChatGPT app (Apps SDK) for Dots, submitted to the ChatGPT apps directory.
- **3.10 (web terminal):** the way in for browser-only agents (Meta Muse).
- **The website:**
  - `llms.txt` and `llms-full.txt`;
  - every docs page also served as `.md`;
  - stable short URLs `/cli`, `/skill` and `/mcp`.

  From 2.7, the docs pages are generated from this repository's generated files.
- **Registries:**
  - **Held as placeholders, 2026-10-09:** npm `portenv` and `@portenv/cli`, and PyPI `portenv-cli`.
  - **PyPI `portenv`** was refused as too similar to `port-env`; a request to PyPI support is drafted.
  - See Open questions for publishing the real CLI through them.

**Agent evals (from 2.7)**
- **`tests/agent-evals/`:** about 10 real tasks, for example:
  - open acme-api and run its tests;
  - make a save point before a risky change;
  - move acme-api to the test server and back;
  - answer a waiting Claude Code question through Portenv;
  - find out why a box isn't saving, and say so;
  - refuse a request that breaks the vault rules.
- **When they run:** before each release, against at least three agents (Claude Code, Codex, and one more, Grok Bot when possible). Each agent's success rate per task goes in the release notes. Not on every PR (cost), but nightly or by hand, with a spending cap.
- **A failure means fixing the docs, the guide or the error messages,** not prompting around it.

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

**Hosted features** (the own-route guarantee, ADR 0008 G3)

- Hosted options (Portenv Cloud in Move To, Portenv storage, Account) appear as ordinary choices once they exist. Choosing one opens a sheet that explains it and offers sign-in. They are never required, never block a flow, and never show banners or nags.
- If the service is unreachable, that shows only when the user picks the option: "Can't reach Portenv right now; your boxes keep working."
- Before a hosted feature exists, it does not appear at all: no "coming soon" items.

**Title menu** (Apple's document-menu pattern)

Rename… · Move To ▸ (this Mac, each server, Portenv Cloud once it exists, Add a Server…) · Duplicate for a Task… · Revert To ▸ (Last Save Point, shown with its time; Browse All Saves…) · Show in Finder ⌥⌘R

**Menu bar**

- File › Make Save Point ⌘S.
- Box › Box Settings… (also reachable from Settings).
- Changes are not a menu item: clicking the sync symbol next to the title shows what changed since the last save.

**Other views**

- Changes (click the sync symbol next to the title): files new, changed or deleted since the last save; a Never saved section with sizes; include or exclude per folder.
- Browse All Saves (Revert To ▸): a timeline of every save (autosaves, save points, closes and orphaned saves) per machine; open any save read-only, compare any save with now; restore one file or everything.
- Agent popover (click an avatar): identity, where it connected from, lane and branch, a live glimpse of its terminal, pending approval, Watch, Take Over, permissions in one sentence, Revoke Access.
- Sidebar (⌘0): Boxes, Machines, Agents.
- Settings window (⌘,): Account and devices, Protection and recovery key, Storage, Machines, Agents and permissions, Secrets, Box defaults and the open box's Box Settings, Notifications.

**Lease and move sheets**

- Open on another machine: Take over here, Connect to it there, Peek read-only, Cancel.
- Move To: progress shown in the title subtitle; a sheet only if something fails.
- Closing with agents connected: their sessions are saved and paused, and a notification says so. No dialog.

**First run (six screens, full version from Phase 3)**

This is the first run once the control plane exists (Phase 3). The mockups show this version.

1. Welcome: Continue with Apple, Continue with GitHub, Continue without an account (a button like the others: the own route needs no account; the remaining steps then skip sign-in and Portenv storage), and Use email instead.
2. Protection: Only you (default) or You, with Portenv's help.
3. Recovery key: Save to Passwords, Print, Copy; Continue enabled only after "I've saved my recovery key".
4. Storage: Portenv storage can be preselected, but "Use my own server or bucket" and "Keep on this Mac for now" are visible on the same screen, each one click, with no account needed.
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

## The terminal edition

A first-class way to use Portenv on every platform, not a stopgap. It talks to the same `portenvd` API as the Mac app, so both always agree.

- **`portenv open <box>`** puts your terminal inside the box, like ssh. Detach and the session keeps running; open again and you're back where you were.
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

**Minimum requirements**

Mac app: macOS 26. Local boxes need Apple silicon (the `apple` driver), or the Docker fallback. The app doesn't launch on older macOS, so older Macs can't use Portenv for now (see Open questions). Servers: Ubuntu 22.04/24.04 or Debian 12, arm64 or amd64.

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

Milestones: 1.0 walking skeleton · 1.1 `portenvd` with the local gRPC API · 1.2 main window with a terminal view (evaluate SwiftTerm, MIT-licensed) and tmux-backed tabs · 1.3 `apple` driver shim, `docker` as fallback (the box agent drops the forbidden capabilities from every process it starts, since a VM's root holds them by default) · 1.4 autosave, sync symbol, Changes, Browse Saves · 1.5 first run (reduced, no sign-in; see Desktop app UX), Keychain keys, recovery key · 1.6 port relay · 1.7 shared folder, then the File Provider · 1.8 signing, notarization, Sparkle updates.

**Phase 1 plan** (approved 2026-10-09; the order of work below is what's built next, item by item)

**Where Phase 1 stands**

**Done in 1.0 and 1.0b, and in the PRs since (#25 to #28 still waiting to merge):**
- **Through the real path:** a minimal `portenvd` and runner, the agent channel (ADR 0010), and the app's terminal in the box's tmux session.
- **Box actions:** save point, Revert To, and Move To a server and back, the same on a server box as on the Mac.
- **Save state:** an honest state line with the full table of states (#26, §3.1), retrying and not-saved states (#23), and "quit before saving" (#25).
- **Quitting:** never fails silently (the alert and the marker, #25); the channel is checked after wake (#25); boxes still opening are waited for (#25).
- **Deadlines:** on every restic run and every process on the daemon side, enforced in CI (#23).
- **Offline open:** near 2 s (ADR 0011).
- **The guideline basics (#26):** title and Box menus in the guideline order, servers by name, tmux hidden, VoiceOver labels.
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

1. **Rename… for boxes** (§3.2). It updates the title, the inspector and the hostname. If the hostname can only change on the box's next start, the rename sheet says so ("The box's hostname changes the next time it starts.").
2. **The tab bar (early, §3 and §4.1), built before the spike** on a tab interface in the box agent: list, new, close and rename, plus events. Whether the agent uses tmux control mode or plain tmux commands stays inside the agent, so the tab bar doesn't change after the spike.
   - One tab per tmux window; `+` for a new tab; the app owns names and order.
   - Until it exists, nothing (`portenv send`, agents) can create a tmux window the user can't see. The agent refuses `new-window` from anything but the app, or maps it to the single visible window.
3. **The inspector (§6):**
   - **Where it is**, including the move's four-step progress (the demo note), the packages line from #28, and Retry;
   - **Saves** (the latest five);
   - **Who's here** (you; stand-in agents arrive in 2.7);
   - **Running now** (tabs, ports).
4. **Notifications (§5)** for a finished long command, a move over 10 s, and the Keychain wait from item 1. Answer buttons come with the inline answer card.
   - The app asks for permission the first time something is worth notifying, never at launch. If permission is denied, the state line and the inspector still show everything, and nothing nags.
5. **The spike: tmux control mode (`-CC`) and OSC 133 (§13), time-boxed to 3 days.** The outcome goes in an ADR either way.
   - It answers: can the agent map app tabs to tmux windows through control mode? Can shell integration in the skeleton home emit OSC 133 marks that the agent tracks per tab, with exit codes, for bash and zsh, including inside Claude Code? Do SwiftTerm overlays stay aligned to rows when scrolling?
   - It's also tested over the SSH forward to a box on a server, not only on the Mac (a test, not another milestone).
   - **If it succeeds,** Phase 1 also gets blocks with attribution, the sticky header, the inline answer card for Claude Code, tab badges, the command palette and saved commands, in that order.
   - **If it doesn't,** they move to Phase 2 and the plan says so.

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

Reduced: no sign-in, Only you, Keychain keys, the recovery key; the Welcome screen has "Continue without an account" (#16).

**8. 1.6: the port relay**

A dev server on the box's localhost opens in Safari. Port pills in the toolbar.

**9. 1.7: shared folder, then the File Provider**

Show in Finder (⌥⌘R) stops being disabled here.

**10. 1.8: releases**

Developer ID signing, notarisation and Sparkle, with the key backups. Signed public CLI releases and their signing key are already on the first-release checklist (#27).
- Needs Apple Developer enrolment (open question), which is also the stable-signing fix for item 1e.
- **Look at again once signing is stable:** run `portenvd` inside a small helper app bundle (`Contents/Helpers/Portenv Helper.app`), so it can post a notification the moment a background save succeeds after Quit Anyway. Until then the next launch of the app shows it (1.1).
- **Release checklist:** test `portenvd`'s socket with a real second macOS account. A process running as another user is refused; 1.1's test simulated the other uid.

**Standing items**

- **The next server session starts with `scripts/fetch-cli.sh` against the server** (#27). The three server scripts install CI's binaries by commit; fix whatever fails there and then. It doesn't block anything before.
- **#26 follow-ups:**
  - Last Save Point time in the menu (1.4);
  - Restart Box with ⌥ in the menu bar's Box menu (1.1, once the menu bar is AppKit-backed or SwiftUI can see ⌥);
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

The minimal server-side piece that 2.1's runner grows from: `portenv-runner` (the same daemon code as `portenvd`, as a systemd service) opens boxes on the server with the agent channel. The Mac moves a box there by enrolling it and opening it through the runner (`sudo /usr/local/bin/portenv app open`, the only sudo command its SSH user `portenv` may run), fetches each start's channel secrets from the runner over the SSH session and keeps them in memory, and reaches the agent's port on the server's loopback through an SSH forward. The same window shows the same terminal on the server, with the state line saying where it runs; save points, Revert To and Restart Box work there exactly as on the Mac. The secrets reach a box only on its stdin, never a file. A runner restart leaves boxes running; the app shows the box agent unavailable and offers Restart Box.

- [x] The window's terminal runs on the server after Move To; save point and Revert To there; Move To ▸ This Mac brings the edits back (`tests/e2e/server-session.sh`, 2026-10-08: 28 checks, Move To 16.8 s)
- [x] A runner restart loses nothing; an impostor on the server box's agent port gets nothing; no channel secret on the server's disk, journal or shell history (`tests/e2e/server-session.sh`)
- [x] The title shows the box's name over its state line and opens the box menu; Move To checks the current location (checked through accessibility in Portenv.app)



**1.4 Autosave, the sync symbol, Changes and Browse Saves: slow links are normal**

Slow uplinks are a real user condition, not an edge case (the Phase 0 gate ran over about 50 KB/s up). So:

- Save status is honest: the title's save state comes only from the recorded save state (the last recorded snapshot and its time, the dirty flag, a save in progress) and what portenvd knows is happening (storage unreachable, the box agent's channel down), never from "open succeeded" or other events. A new box shows "Not saved yet". The states: Not saved yet, Saving…, Saved at (time), Offline · will save later, Not saved · box agent unavailable, Not saved since (time) · retrying, Not saved since (time). Tested per state (`daemon/savestate_test.go`, `PortenvKitTests`).
- A Move, or any save that takes more than a few seconds, shows visible progress and a time estimate in the title subtitle, from the bytes still to send and the measured bandwidth.
- Closing the laptop or quitting mid-upload never loses work and never leaves the lease in a bad state: the local home stays as it was (dirty), no partial save counts as complete, the lease stays with this machine until a save completes, and the upload resumes (restic deduplicates what already arrived) when the Mac wakes.
- Same-machine resume never waits on the network: offline, the box starts from the local home and saves queue.

**1.8 Releases and where the app is downloaded from**

- **Pipeline:** a version tag triggers CI on a macOS runner: build, sign with the Developer ID certificate, notarize and staple, package and sign `Portenv.dmg`, and generate a Sparkle `appcast.xml` signed with Portenv's EdDSA key.
- **Hosting:** the DMG and `appcast.xml` are assets on the `portenv/portenv` GitHub release.
- **Stable URLs:** `portenv.com/download` and `portenv.com/appcast.xml` redirect to the latest release's assets (Cloudflare redirect rules). The app's Sparkle feed URL is `portenv.com/appcast.xml`, never a GitHub URL, so hosting can move later without breaking updates.
- **Keys:** the Developer ID certificate and the Sparkle EdDSA private key live only in CI secrets and the owner's Keychain, never in the repository.
- **Sparkle key backup** (losing it means installed apps can never be updated): when the key is generated, export it once to two offline copies (an encrypted USB drive and a printed or written copy in a separate safe place), record its public key and fingerprint in this plan, and test a restore into a clean Keychain by signing a throwaway appcast that a test install accepts. Repeat the restore test before each major release. A suspected leak means rotating to a new key, which only apps that already trust both keys can follow: ship the new public key in an update signed with the old key first.
- **Own route:** the update check runs in the background and times out quietly when portenv.com is unreachable; it never blocks or slows launch. A release is blocked unless the `own-route` job passes on the tagged commit.
- **Later:** a Homebrew cask pointing at the same release.

- [ ] A new user goes from download to a working box without typing a command or creating an account
- [ ] Closing the window saves and releases; reopening restores within 5 seconds
- [ ] On a throttled uplink (for example 400 kbit/s), a Move shows progress and an estimate, and closing the lid mid-upload loses nothing and leaves the lease with this machine
- [ ] A version tag produces a signed, notarized, stapled `Portenv.dmg` and a signed `appcast.xml` on the GitHub release; `portenv.com/download` and `portenv.com/appcast.xml` redirect to them, and an installed build updates through Sparkle
- [ ] The Sparkle key's offline backups exist and a restore test passed
- [ ] Offline: work continues and saves upload when the network returns
- [ ] No docker exec as an access path, on the Mac or on servers: boxes are reached only through `portenvd` on the Mac and the runner on servers, with the agent channel (ADR 0010). The Phase 0 CLI's docker exec path is removed or moved behind them.
- [ ] Every control has a VoiceOver label; light and dark appearance both pass review
- [ ] A dev server bound to the box's localhost opens in Safari through the relay
- [ ] Inside an `apple` box (a VM), the restic password probe (`tests/e2e/restic-isolation.sh`) passes: root in the box cannot read the password, because the agent drops the forbidden capabilities itself

### Phase 2: Developer servers

The toolbox image is published from CI ahead of the rest of 2.5: each architecture builds and tests on its native runner, then one multi-arch manifest goes to `ghcr.io/portenv/toolbox-node`, tagged `sha-<commit>` and `main` (and `vN` on version tags), with a build provenance attestation. Macs, servers and the setup script pull by digest, and the CLI pins a box's image to its digest on first use. Local builds stay possible for development; nothing requires them.

Milestones: 2.1 runner (server build of the core) installed over SSH, grown from 1.0b's minimal runner: the Mac's SSH user may run only `/usr/local/bin/portenv` with sudo, never a shell · 2.2 Add a Server wizard with preflight · 2.3 Move To in the title menu · 2.4 lease sheet · 2.5 toolbox registry, versions and drift warnings · 2.6 append-only storage · 2.7 stand-in agents over SSH and the CLI · 2.8 webhooks · 2.9 stdio MCP.

**2.6 Append-only storage** (must land before 2.7, which gives outside agents stand-in access to boxes)

- The storage credential that enters a box becomes append-only: it can add files to the repository but never delete or overwrite one. A box, even as root, cannot destroy a save.
- Forget, prune and the removal of stale locks use a separate credential that never enters a box and lives only on the host (`portenvd` or the runner).
- For SFTP storage this means restic's REST server in append-only mode (`rest-server --append-only`) on the server, bound to the loopback address and reached through the existing SSH connection, with an append-only user for boxes and a full user for the host. No new inbound port. For S3-compatible storage: a put-only credential for boxes, a full one for the host.

**2.7 Stand-in agents over SSH and the CLI** (no control plane, no lanes)

- Doors 1 and 2 for a box on the user's own server, in Continue my work mode. Grok Bot, or any terminal agent, reaches the box, runs Claude Code there and relays its questions back.
- Credentials (ADR 0007): enrolment is closed by default; Connect an agent opens it for 10 minutes or until one code is approved. The agent runs `portenv login`; the user approves the device code in the app, after seeing the agent's name, the box and the key fingerprint; the runner, as certificate authority for its boxes, issues a 30-minute SSH certificate scoped to that box and mode, renewed by proof of possession of the agent's key until the grant ends (default 8 hours, at most 24 hours until Phase 3). No new inbound port: everything goes through the server's existing SSH. The app lists every certificate with revoke.
- The core in the box agent: events (waiting for input, command finished, new output), `portenv events --follow`, `portenv screen <tab>`, `portenv send <tab>`, and `docs/skill/SKILL.md`.
- Agent readiness (see that section):
  - the command registry, and everything generated from it, with the CI drift check;
  - `portenv guide` and `--json`;
  - the skill, with its generated reference;
  - the box introducing itself (`PORTENV_BOX`, `PORTENV_GUIDE`, the login banner, and Portenv's own `AGENTS.md`, never touching the user's files; ADR at 2.7);
  - `tests/agent-evals/`.
- Attribution and session recording per agent, the save point on connect, Watch, Take Over and Revoke in the app.
- Not in 2.7: lanes (4.1), approvals (4.2), the gateway and its CA (Phase 3), phone notifications (4.6).
- **Open question:** a per-box "Keep running when Portenv quits" option, off by default, so a stand-in agent's work can go on after the app quits, with a menu bar item listing the boxes still running. Only worth building with Phase 3's lease hand-off, so a sleeping Mac never blocks resuming the box elsewhere. No ADR until then.

**How agents get the skill and the CLI** (built with 2.7)

- **The skill:** `skills/portenv/SKILL.md` in this repository, in the Agent Skills format (a folder with `SKILL.md` and frontmatter), versioned with the CLI. CI fails if a command the skill uses does not exist in that CLI version. It covers connecting with a device code, events, screen and send, waiting for input, relaying questions to the user, and revoking itself. It never contains credentials, and it tells the agent to install the CLI only from `portenv.com/cli` and to verify it. `portenv.com/skill` redirects to the `SKILL.md` attached to the latest release (the same pattern as the DMG and the appcast).
- **The CLI for agents' computers:** release assets are static `portenv` binaries for linux/arm64, linux/amd64 and macOS arm64, plus a signed `checksums.txt` (ADR 0009). `portenv.com/cli` serves a short install script that detects the platform, downloads the release binary, verifies the signature and checksum, and installs into the user's home without `sudo`.
- **Installing the skill:** `portenv skill install` writes the skill matching the CLI's own version to `~/.agents/skills/portenv/` (read by Cursor, and by Grok Bot through its support for Cursor's skills); `--path` covers other agents' folders; `portenv skill` prints it.
- **Connect an agent** in the app shows one message to copy and paste to the agent, for example: "Install the Portenv CLI from portenv.com/cli, run `portenv skill install`, then connect to my box acme-api with code K7F2-9QX4." Opening the sheet opens the enrolment window (ADR 0007: 10 minutes or one approved code); when the agent requests the code, the approval sheet follows.
- **Later, not in 2.7:** a plugin marketplace repository carrying the skill. The MCP door needs no skill: its tools describe themselves.

**2.8 Webhooks**: signed event pushes from the box agent or runner (never a Portenv server) to a URL the user registers for their agent. Payloads describe themselves: the event type, the box, a link to the event's docs, and the `portenv` command to act on it. Settings and the signing key live in `~/.portenv` in the box, so they are encrypted and move with it. Before building it, confirm the target agents can be woken by an incoming webhook (Open questions).

**2.9 stdio MCP**: `portenv mcp` on the agent's computer exposes events, screen and send over the CLI's connection.
- Tool descriptions and schemas are generated from the registry, with `readOnlyHint` and `destructiveHint` per tool.
- The guide is an MCP resource.
- The server is listed in the official MCP Registry.

- [ ] Move To works Mac → server → Mac from the title menu with no data loss
- [ ] Every lease sheet path behaves as specified, including a stale lease
- [ ] A server reboot leaves boxes recoverable without user intervention
- [ ] The wizard explains every preflight failure in plain language
- [ ] A box, even as root, cannot delete or overwrite any existing save: with the box's credential, deleting, renaming or rewriting any repository file (snapshots, index, data, keys, config) fails, and `restic check` passes afterwards (2.6)
- [ ] Forget, prune and unlocking run only with the host's credential, which no box ever receives (2.6)
- [ ] A stand-in agent connects from its own computer with a short-lived certificate, drives the user's Claude Code session by keystrokes in `/home/work/<project>`, and the user sees its activity attributed by name
- [ ] Relay loop: a tool in the box asks a y/n question; "waiting for input" fires; a test agent reads the screen, relays the question, sends "y"; the tool continues. Run once with Claude Code and once with a plain script, using only `portenv events`, `portenv screen` and `portenv send`
- [ ] Revert To ▸ Last Save Point undoes everything a stand-in did since it connected
- [ ] Revoke ends the agent's session immediately and its certificate can no longer be renewed
- [ ] Every condition in ADR 0007 has its test passing (CA key never leaves the server, one box and one mode per certificate, no new inbound port, `portenv-enroll` closed by default, locked down and silent, device codes expire, are single use and rate-limited, renewal bound to the agent's key, revoke kills renewal and sessions)
- [ ] A webhook reaches the registered URL with a valid signature when a tab starts waiting for input (2.8)
- [ ] The stdio MCP server answers events, screen and send for a connected agent (2.9)
- [ ] On a clean Linux machine standing in for an agent's computer, the message pasted from Connect an agent alone gets an agent from nothing to connected and relaying a Claude Code question, with no other help
- [ ] The install script refuses a binary whose checksum or signature does not verify (ADR 0009), and CI fails if the skill uses a command the CLI does not have
- [ ] 2.7 agent readiness:
  - every command has `--json`, a stable exit code and an example in its help;
  - `portenv guide` exists;
  - the skill installs to `~/.agents/skills/portenv`;
  - inside a box, `PORTENV_BOX` is set, and the agent-facing intro reaches Claude Code and Codex without touching the user's own files;
  - the CI drift check passes.
- [ ] 2.7: the agent evals pass at least 8 of 10 tasks on Claude Code and on Codex
- [ ] 2.9: the MCP tools are generated from the registry, with annotations, and the server is listed in the MCP Registry
- [ ] Website: `llms.txt`, `llms-full.txt` and the `.md` pages are live and match the generated docs

### Phase 3: Control plane and gateway

Milestones: 3.1 accounts (Apple, GitHub, email) · 3.2 device enrollment and wrapped keys · 3.3 authoritative leases · 3.4 box agent tunnels and gateway routing by box name · 3.5 Portenv storage (Cloudflare R2, one prefix per box, short-lived prefix-scoped credentials minted per device, quotas by refusing credentials, retention and pruning on the user's devices; ADR 0008) · 3.6 push notifications · 3.7 full first run (sign-in, Portenv storage, recoverable mode) and the one-time account prompt for existing users · 3.8 SSH door through the gateway (Portenv CA certificates, ports 22 and 443, `portenv ssh` over HTTPS; needs 3.1, 3.2, 3.4) · 3.9 remote MCP door with OAuth and the ChatGPT app (Apps SDK) for Dots, submitted to the ChatGPT apps directory (needs 3.1, 3.4 and the 2.7 core; not lanes, approvals or the vault) · 3.10 web terminal on portenv.com with passkeys, the way in for browser-only agents such as Meta Muse (needs 3.1, 3.4).

- [ ] A box is reachable by name wherever it runs, with no inbound ports anywhere
- [ ] Revoking a device ends its sessions within 5 seconds
- [ ] A control plane outage does not interrupt local work or saves to the user's own storage
- [ ] An existing Phase 1 or 2 user creates an account and links their devices without losing a box or re-entering keys
- [ ] An agent reaches a box by name through the gateway over SSH (22 and 443) and over remote MCP, with the same attribution and revoke as 2.7
- [ ] The runner CAs are retired: their trust removed and keys destroyed, and the migration test passes (ADR 0007)

### Phase 4: Agent access

Milestones: 4.1 lanes (users, tmux, worktrees, limits) everywhere a box runs, and stand-in mode on local and cloud boxes · 4.2 opt-in approval helper (push, delete, secrets, protected scripts, sudo in a lane) · 4.3 secrets vault · 4.4 full command audit (auditd or eBPF) on top of 2.7's session recording · 4.5 remaining doors, only if usage shows the need: GitHub bridge, chat or email bridges, A2A (ADR 0006) · 4.6 iPhone companion for approvals.

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
| Keys | Device add, revoke, recovery, re-encrypt, Delete Box (keys go only with the saves: no box is left with saves but no key); verify no key material on disk outside approved stores | Every merge touching keys |
| Agent isolation | Adversarial suite: an agent lane tries to read other homes and secrets, escalate privilege, reach blocked hosts, persist after revoke | Every merge from Phase 4 |
| Mac app | XCUITest for first run, title menu, lease sheet; accessibility audit; light and dark snapshots | Every merge touching the app |
| Performance | Budgets: resume under 5 s to a ready box (median of 5, worst reported), online at a round-trip time of 50 ms or less and offline; close or move with a 5 MB change under 15 s at 20 Mbit/s up and 50 ms; newest save at most 60 s behind with continuous editing; port relay adds under 5 ms locally. Bandwidth and round-trip time are measured and reported with every result | Nightly |
| Recovery drill | Restore a box from the recovery phrase on a clean Mac | Before each release |
| Own route | `own-route` job: the own-route journey with every Portenv-hosted endpoint blocked at DNS and the firewall, from locally built artifacts; from Phase 1 it also drives the app with the hosted options visible and untouched, and from Phase 3 completes first run through Continue without an account (ADR 0008, G3) | Every pull request and merge to main; blocks releases |

CI runs the Go suites on Linux arm64 and amd64 and the app suites on macOS arm64 runners. A release is blocked by any failing gate.

Behaviour the app relies on is tested through the path the app uses: `portenvd` (or the runner) and the box agent's channel. `docker exec` in a test only inspects or simulates a failure, and `tests/e2e/daemon.sh` fails on any other exec into the box. Each such check has been shown to fail when the code it covers is broken (a mutation check), and a test that can't fail is a bug in the test. Every e2e call has a time limit, so a hang fails a check.

## Open questions

These need an owner decision; Claude Code should add new ones here instead of guessing.

- [ ] Default protection mode for new users: Only you (current default) or recoverable?
- [ ] Phase 2: install the runner as a service from the start, or plain SSH first? The plan assumes the runner.
- [ ] Before the public release: lower the app's deployment target from macOS 26 to macOS 15 (gRPC Swift 2's floor, ADR 0014), so older Macs can use the Docker fallback? Today the app targets macOS 26 and doesn't launch on them. Intel Macs would also need a universal build.
- [ ] Trademark and domain check for Portenv in the EU and US; confirm portenv.com is registrable.
- [ ] Docker inside the box (Phase 1): how to provide it without granting the box `CAP_SYS_ADMIN` or privileged mode (for example rootless Docker or a nested sandbox), or whether the setting ships with a plain warning that it weakens the repository-password protection (ADR 0005).
- [ ] Point-in-time autosaves while the box runs: worth a file system with snapshots (for example btrfs on the Apple disk image), or are file-consistent saves plus the pre-save hook enough? (ADR 0005)
- [ ] Before each door is built: confirm the target agents can actually reach it (for example outbound SSH from their computers for door 1).
- [ ] Does Grok Bot read `~/.agents/skills` and can it run the install script? (The owner is testing it.)
- [ ] Before 2.8: can the target agents be woken by an incoming webhook? If not, an agent keeping `portenv events --follow` running does the job and 2.8 drops in priority (ADR 0008).
- [ ] Apple Developer Program enrolment (the owner is handling it): needed for Developer ID signing and notarization in 1.8.
- [ ] Approval timeout default (30 minutes assumed) and what happens when it expires.
- [ ] Anthropic's terms for agents driving Claude Code with a subscription login versus a Console API key.
- [ ] Open-source boundary in detail: this repository is Apache-2.0 and `portenv/cloud` is private (decided), but confirm before going public whether the Mac app, the File Provider and the iPhone companion stay in the public repository or move to a private one.
- [ ] Phase 3 control-plane milestones now live in `portenv/cloud`: how its CI pins and tests against `proto/gen/go` versions from this repository (tags, or a pseudo-version per merge).
- [ ] Scope of the iPhone companion: approvals only, or also status and a read-only terminal?
- [ ] Name of the CLI binary: `portenv` assumed.
- [ ] Linux desktop window, after the terminal edition ships on Linux: Tauri with xterm.js (shared with 3.10's web terminal), or native GTK?
- [ ] For 2.7–2.9: publish the real CLI binary through npm (platform packages, esbuild-style) and PyPI, so `npx portenv` and `uvx portenv` work in agent sandboxes that only reach package registries, and so MCP clients can start it the way they usually start servers?
- [ ] A per-box "Keep running when Portenv quits" option (off by default) with a menu bar item listing boxes still running? Only with Phase 3's lease hand-off (see 2.7).
