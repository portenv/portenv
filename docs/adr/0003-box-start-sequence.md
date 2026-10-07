# 0003. Box start sequence and home initialisation

Date: 2026-10-07 · Status: accepted

## Context

Milestone 0.2 makes `portenv-agent` the box's init process. The plan says the agent reaps children, that `apt-packages.txt` is replayed before the box reports ready, and (resume rule 1) that a box with nothing saved starts from the image's skeleton home. It does not say who decides that a box is new.

A box whose home storage failed to attach looks exactly like a new box: `/home` is empty. If the agent filled it from the skeleton, the next save would record a near-empty home as the newest snapshot, on top of the user's real work.

## Decision

- **The caller decides a box is new.** The agent creates `/home/work` from the skeleton (`/usr/share/portenv/skel`) only when started with `PORTENV_INIT_HOME=1`. Without it, a missing or empty home fails the start sequence with "home storage is not attached or is empty". `portenvd` and the runner set the variable only under resume rule 1.
- **The image's `/home` is empty.** The `work` user (uid 1000, passwordless sudo) exists in the image, but its home lives only on the box's home storage. This also stops Docker from copying image content into a fresh named volume, which other engines would not do.
- **Start sequence:** ensure `work` has uid 1000, check the home and set it to 0700, then install any package in `~/.portenv/apt-packages.txt` that is missing. Entries are validated as Debian package names (optionally `:arch` and `=version`) and passed after `--`, so the file cannot inject apt options.
- **Readiness** is served over `portenv.agent.v1` on `/run/portenv/agent.sock` (root-only directory, 0600 socket) as STARTING, READY or FAILED with a detail. A failed box stays up so it can be inspected. `portenv-agent ready` wraps the call and backs the Docker `HEALTHCHECK`; other engines ask the agent directly.
- **Init duties:** reap orphans; on SIGTERM or SIGINT send SIGTERM to every process in the box, wait up to 8 seconds, then SIGKILL. While a start-sequence command runs, the reaper stands aside (shared lock, try-lock in the signal loop) so it never steals a child the agent is waiting for. Phase 1, which adds terminals, needs a reaper that hands exit statuses to their owners.
- **Pinned inputs:** the Ubuntu 24.04 and Go builder images by index digest, Node 22 by version and per-architecture SHA-256, Claude Code from Anthropic's stable apt repository with the signing key rejected unless its fingerprint is `31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`.
- **Builds:** locally only the native architecture (`make image`), never emulated. CI builds and tests each architecture on its native runner. A tag `toolbox-node/vN` pushes `ghcr.io/portenv/toolbox-node:vN-<arch>` and then the multi-arch manifest `:vN`.

## Consequences

- Recreating agent users from a manifest in `~/.portenv/` (plan: Users and privileges) is deferred to milestone 4.1; only `work` exists until then.
- The skeleton merges Ubuntu's `/etc/skel` shell files with `.portenv/`; `/etc/profile.d/portenv.sh` points npm globals and Playwright browsers into `~/.local` so they are saved.
- The first published package on ghcr.io is private by default and must be made public once.
