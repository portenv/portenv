# Portenv

Portenv (short for portable environment) is a native Mac app that gives each project an encrypted workspace, a "box", that can be saved, moved between a Mac, developer-owned servers and Portenv Cloud, and opened safely to AI agents.

**The full plan is in `docs/PLAN.md`. Read it completely before writing code.** It is the master copy and the source of truth: update it in the same change as the code. The "Decisions already made" section is settled; ask before deviating from it, and call out any change to it in your summary.

The approved app designs are in `docs/mockups/` (PNG plus static HTML per screen, with a README). Match them when building the Mac app; they are references, not code to port.

`docs/design/GUIDELINES.md` is the rulebook for all app UI (state line, menus, terminal, inspector, palette, notifications, writing, accessibility). If a mockup and GUIDELINES.md disagree, GUIDELINES.md wins.

## Current phase

Phase 1: Native Mac app, local boxes. Phase 0 is closed (`docs/PLAN.md`). Work through the Phase 1 plan's order of work in `docs/PLAN.md`, one item at a time, starting with item 1 (Keychain calls never wait silently). Do not start the next phase until Phase 1's acceptance checklist passes.

## Ground rules

- Only driver packages (`core/driver/`) may talk to Docker, containerd or Apple Containerization. From Phase 1 on, never use `docker exec` as an access path; go through the box agent.
- Never weaken a security default to make something work: no plaintext keys or secrets on disk, no public or inbound ports (the one exception: public doors on a user's own server, opt-in per server, under every condition of ADR 0015), no disabled encryption. Stop and ask.
- Save, resume, lease and key code are data-loss paths: write the tests first and keep the invariants in `docs/PLAN.md` (Save, resume and leases) true at every commit.
- Never pipe build or test output in a way that hides the exit code: use `set -o pipefail` (or `set -euo pipefail`), or check the exit status before trimming output. A command whose result you report must have its real exit status checked.
- Local results count only when run with GNU Make 4 or later (`gmake` on a Mac: `brew install make`). macOS's own make (3.81) ignores the Makefile's shell flags and once hid failing tests and lint; the Makefile now refuses to run under it.
- Builds run with a clean environment: Swift builds go through the Makefile's `$(SWIFT)` (`env -i` with only PATH, HOME, TMPDIR and LANG), because SwiftPM's plugin caches record the environment on disk. Never run a bare `swift build` with secrets or session tokens in the shell; `make swift-env-check` proves nothing leaks.
- Work one milestone at a time in small, reviewable changes. End each milestone with a demo note in `docs/milestones/<id>.md` and the milestone's checklist ticked in `docs/PLAN.md`.
- Add unknowns to "Open questions" in `docs/PLAN.md` instead of guessing.
- Ask before touching real production credentials, deleting snapshots or repositories, or publishing anything.
- Record any decision that changes the plan as an ADR in `docs/adr/`.
- In public product descriptions (README, repository description, app copy), call a box an encrypted workspace; don't name Linux. Technical docs may.
- This repository is public (Apache-2.0). Every source file starts with an SPDX header. Keep business content (pricing, plans, how the cloud is sold) and private or customer details out of it, including demo data.

## Fixed names and paths

- Product: Portenv (one word). CLI: `portenv`. Mac daemon: `portenvd`. Server daemon: `portenv-runner`. In-box agent: `portenv-agent`.
- Box home: `/home/work` for the main user `work` (uid 1000); agent lanes at `/home/<agent>`; the saved unit is all of `/home`.
- Per-box config lives in `/home/work/.portenv/` (`apt-packages.txt`, `excludes`, `hooks/`).

## Stack

- Swift / SwiftUI: `apps/mac`, `apps/ios`, `shims/containerization`.
- Go 1.26: `core/` (daemons, agent, CLI, drivers, sync). The control plane and gateway live in the private `portenv/cloud` repository, not here.
- Developer tools (buf, protoc plugins, golangci-lint, actionlint, gitleaks) are pinned in the `Makefile` and installed into `bin/` with `go install pkg@version` on first use.
- Toolbox images: `images/`. Shared gRPC definitions: `proto/` (generated Go in `proto/gen/go`, its own module, imported by `core` and `portenv/cloud`).
- Requirements for the native engine: Apple silicon, macOS 26, Xcode 26.

## Commands

- `make build`: build `portenvd`, `portenv-runner`, `portenv-agent` and `portenv` into `bin/`.
- `make test`: Go tests with the race detector (`go test -race ./core/...` for one module).
- `make lint`: gofmt, go vet, golangci-lint, buf lint and format, actionlint, SPDX headers.
- `make fmt`: format Go and proto sources.
- `make proto`: regenerate `proto/gen/go` after editing `proto/`; commit the result. `make proto-check` fails if it is stale.
- `make agent-linux`: static `portenv-agent` for linux/arm64 and amd64.
- `make swift-test`: build and test the Swift packages (macOS), with a clean environment.
- `make swift-env-check`: build with a fake secret in the environment and confirm it reaches no build folder.
- `make image` / `make image-test`: build the toolbox image for this Mac's architecture only (never emulate amd64) and run `images/toolbox-node/test.sh`.
- `make driver-test` / `make e2e`: driver conformance (with the capability-refusal test) and the end-to-end checks (restic isolation probe, two-machine round trip) against `IMAGE`.
- `make secrets`: gitleaks over the full history and the working tree.
- `make check`: everything above that runs on this machine.
