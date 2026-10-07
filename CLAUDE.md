# Portenv

Portenv (short for portable environment) is a native Mac app that gives each project an encrypted Linux workspace, a "box", that can be saved, moved between a Mac, developer-owned servers and Portenv Cloud, and opened safely to AI agents.

**The full plan is in `docs/PLAN.md`. Read it completely before writing code.** It is the master copy and the source of truth: update it in the same change as the code. The "Decisions already made" section is settled; ask before deviating from it, and call out any change to it in your summary.

The approved app designs are in `docs/mockups/` (PNG plus static HTML per screen, with a README). Match them when building the Mac app; they are references, not code to port.

## Current phase

Phase 0: Foundations (owner only). Start with milestone 0.1 (repository, CI and protocol scaffold). Do not start a phase until the previous phase's acceptance checklist in `docs/PLAN.md` passes.

## Ground rules

- Only driver packages (`core/driver/`) may talk to Docker, containerd or Apple Containerization. From Phase 1 on, never use `docker exec` as an access path; go through the box agent.
- Never weaken a security default to make something work: no plaintext keys or secrets on disk, no public or inbound ports, no disabled encryption. Stop and ask.
- Save, resume, lease and key code are data-loss paths: write the tests first and keep the invariants in `docs/PLAN.md` (Save, resume and leases) true at every commit.
- Work one milestone at a time in small, reviewable changes. End each milestone with a demo note in `docs/milestones/<id>.md` and the milestone's checklist ticked in `docs/PLAN.md`.
- Add unknowns to "Open questions" in `docs/PLAN.md` instead of guessing.
- Ask before touching real production credentials, deleting snapshots or repositories, or publishing anything.
- Record any decision that changes the plan as an ADR in `docs/adr/`.
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
- `make swift-test`: build and test the Swift shim (macOS).
- `make image` / `make image-test`: build the toolbox image for this Mac's architecture only (never emulate amd64) and run `images/toolbox-node/test.sh`.
- `make secrets`: gitleaks over the full history and the working tree.
- `make check`: everything above that runs on this machine.
