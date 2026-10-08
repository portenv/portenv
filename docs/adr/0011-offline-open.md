# 0011. Opening offline: the app's network report, a short probe, no restic

Date: 2026-10-08 · Status: accepted

## Context

Moving the SFTP end-to-end test onto `portenvd` (the path the app uses) showed that opening a box with its storage server down took 5.5 to 5.7 s there, over the 5 s budget, while the Phase 0 CLI took 2.2 s. Stage timings put 2.8 s between the box agent answering and the resume rule being decided: `restic version` ran on every open, and the daemon built the sync engine twice per open. The storage probe already ran alongside the box's start (agreed in the Phase 0 gate round), but it still had to finish, or time out after 2 s, before the rule could be decided.

## Options

| Option | No network at all | Network up, storage down | Risk |
| --- | --- | --- | --- |
| Probe only (2 s) | Up to 2 s | Up to 2 s | None, but slow when packets are dropped |
| **App's report, then a 1 s probe** | Instant | Up to 1 s | A stale "down" would open offline with storage reachable |
| Trust the system status alone | Instant | Wrong: opens online, then fails | Storage down is common with the network up |

## Decision

- **The app reports the system's network status** (NWPathMonitor) to `portenvd` through `SetNetworkPath` (`portenv app network up|down` until the app has its own gRPC client). A "down" skips the storage probe **only while the app that sent it still runs and for at most 30 s after it**; the app repeats it every 10 s while it lasts. A missing, stale or orphaned report, or "up", means probe.
- **The probe has a 1 s timeout** and runs alongside the box's start.
- **Opening never runs restic just to check it.** restic is pinned and hash-checked when installed, so its version is checked once per binary (path, size, modification time), recorded in the box's state directory, and again only when the binary changes.
- **One sync engine per open**: its executor follows the box's current agent channel across the restart in the middle of an open.

## Consequences

- Offline open measured 1.86 to 2.03 s on a Mac (network up and storage down, a fresh "down", and a "down" whose sender quit), all near the CLI's 2.2 s. What remains is the box's start and its agent answering.
- A stale "down" can't strand a box offline: it expires within 30 s and dies with the app that sent it.
- The runner and the CLI never send reports, so they always probe.
