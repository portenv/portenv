# 0008. Infrastructure

Date: 2026-10-07 · Status: accepted · Amended 2026-10-08 (own-route hard rules under G3)

## Context

Portenv runs on the user's own machines first and adds hosted services later. This records what runs where and how Portenv storage and Portenv Cloud are built, so early phases do not grow servers they do not need.

## Decision

### G1. No Portenv backend before Phase 3

Phases 0 to 2 run only on the user's Mac, servers and storage: no Portenv database, accounts or dashboard. The Phase 1 and 2 first run never offers Portenv storage, which arrives in 3.5. (Images are published to GitHub's container registry, which is not a Portenv backend.)

### G2. Webhooks come from the box

Webhooks (2.8) are sent by the box agent or the runner, not by a Portenv server. Their settings and signing key live in `~/.portenv` inside the box, so they are encrypted with the box and move with it. Before 2.8 is built, check that the target agents can be woken by an incoming webhook; if they cannot, an agent keeping `portenv events --follow` running does the job and 2.8 drops in priority.

### G3. Your own machines need no Portenv servers

Everything that runs on your own machines and storage (the app, local boxes, your servers, your storage, SSH and CLI access, webhooks from the box) works without Portenv's servers or an account. Hosted services (gateway, push, remote MCP, web terminal, Portenv storage, Portenv Cloud) are separate.

**Hard rules (the own-route guarantee).** The developer-managed route (your Mac, your servers, your storage, agents over SSH and the CLI) never depends on Portenv's hosted services:

1. No sign-in is ever required for the own route. First run completes without an account, in every phase.
2. Entitlement and billing checks apply only to hosted features, never to own-route ones.
3. Update checks, the skill URL and any other fetch from portenv.com never block or slow the app: they run in the background and time out quietly.
4. Nothing on the own route waits on telemetry or crash reporting.

**Enforced by the `own-route` CI job** (`tests/e2e/own-route.sh`). It runs the full own-route journey with every Portenv-hosted endpoint unreachable: portenv.com, the gateway, the control plane and Portenv storage, blocked at DNS and the firewall for the runner and its boxes (the list is `tests/e2e/hosted-endpoints.txt`; source naming an unlisted portenv.com host fails the job). The journey: create a box, autosave, Move Mac → server → Mac with matching checksums, Revert To, offline resume, and from 2.7 an agent connecting over SSH and the CLI and relaying a question. Once the app exists, the job also drives the UI: every own-route flow completes with the hosted options visible and untouched. It uses locally built artifacts (CLI, image, skill), so no distribution URL is on its path. It runs on every pull request and every merge to main, and releases are blocked unless it passes. Steps that are not built yet are listed as pending and join the journey in their milestone.

### G4. Portenv storage (3.5)

- **Cloudflare R2**: free egress matters because every resume is a download. One bucket, one prefix per box.
- **No permanent storage keys on devices.** The control plane mints short-lived credentials scoped to the box's prefix (R2 temporary credentials: bucket, prefix, read or read-write, lifetime). Revoking a device means it gets no new credentials.
- **Quotas** are enforced by refusing credentials when an account is over its limit.
- **Pruning and retention run on the user's devices**, because only they hold the key. Default: the plan's retention (the last 20 saves, hourly for 24 hours, daily for 14 days, weekly for 8 weeks, monthly for 12 months, every orphaned save), applied weekly by the device that holds the lease, when it is idle and on power; pruning needs the repository's exclusive lock, so it never runs while another machine has the box open. When an account nears its quota, the app offers to prune further rather than failing saves.
- **Account closure** deletes the box prefixes without reading them. Session recordings and logs use the same storage, encrypted to the user.

### G5. Portenv Cloud (Phase 5): the save is the source of truth; hosts are disposable

- **5.1 microVM driver**: Firecracker or Cloud Hypervisor, chosen by a spike; the first launch may use a rented provider behind the same driver. Kata is dropped (Portenv does not plan to adopt Kubernetes).
- One microVM per box, one tenant per VM. A shared read-only toolbox image; `/home` restored from the save onto local disk, encrypted per box.
- "Only you" boxes need a device to release the key for each wake (5.3).
- Idle boxes save and stop after a set time; agent keystrokes count as activity. A stopped box costs only storage.
- Placement is a simple scheduler in the control-plane database, preferring the host that still has the box's `/home` cached.
- Draining a host means moving its boxes. A dead host loses at most the work since the last autosave.
- Abuse controls before launch: outbound port 25 blocked, sustained full-CPU detection.
- The control plane and gateway start in one EU region with managed PostgreSQL.

## Consequences

- Nothing in Phases 0 to 2 depends on Portenv being reachable.
- Portenv storage needs the control plane (3.1, 3.2) before 3.5: credentials are minted per device.
- The Decisions table's runtime row changes: microVMs are Firecracker or Cloud Hypervisor, not Kata.
