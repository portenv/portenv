# 0002. Repository and module layout

Date: 2026-10-07 · Status: accepted

## Context

Milestone 0.1 sets up the repository. The plan described one monorepo including `cloud/` (control plane and gateway). Before the first push the owner decided that this repository is public under Apache-2.0 and that the hosted service stays private.

## Decision

- `github.com/portenv/portenv` is public, Apache-2.0, with an SPDX header in every source file (`scripts/check-spdx.sh`, run by `make lint`). Generated code is exempt.
- The control plane and gateway live in the private repository `portenv/cloud`. There is no `cloud/` directory here.
- Go modules, tied together by `go.work`:
  - `github.com/portenv/portenv/core`: daemons, agent, CLI, drivers, sync.
  - `github.com/portenv/portenv/proto/gen/go`: generated protocol code, its own module so `portenv/cloud` can import it without importing `core`.
- Protocols in `proto/`, managed with buf. Generated Go code is committed and CI fails when it is stale. `driver.v1` and `types.v1` are complete; `daemon.v1` and `agent.v1` are unstable skeletons. Breaking-change checks start in Phase 1.
- The Go `driver.Driver` interface mirrors `driver.v1.DriverService`; a test checks their method sets match. It uses plain Go types rather than generated ones, so drivers written in Go do not depend on protobuf.
- The engine boundary (only `core/driver/` may use engine SDKs or CLIs) is enforced by a test in `core/internal/boundary`, not by convention.
- Developer tools are pinned in the `Makefile` and installed with `go install pkg@version` into `bin/`. A shared `tools/go.mod` was tried and rejected: the tools need conflicting versions of common dependencies.
- Go 1.26 (`go 1.26.7` in each `go.mod`). CI: Go on Linux amd64 and arm64, Swift on macOS 26 arm64, protocol freshness, workflow lint and a full-history secret scan.

## Consequences

`docs/PLAN.md` (Decisions, tech stack and layout, roadmap) is updated to match. `portenv/cloud` must pin `proto/gen/go` by version; how is an open question in the plan.
