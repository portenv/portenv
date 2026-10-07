# Portenv

Portenv (short for portable environment) is a native Mac app that gives each project an encrypted workspace, a *box*, that can be saved, moved between a Mac, developer-owned servers and Portenv Cloud, and opened safely to AI agents.

**Status:** early development (Phase 0, foundations). Nothing here is usable yet.

## Repository

| Path | What it is |
| --- | --- |
| `apps/` | Mac app and iPhone companion (SwiftUI) |
| `core/` | Go: `portenvd`, `portenv-runner`, `portenv-agent`, the `portenv` CLI, box drivers, sync |
| `shims/containerization/` | Swift process implementing the box driver on Apple Containerization |
| `proto/` | gRPC protocols shared by the app, daemons, box agent and the hosted service |
| `images/` | Toolbox images |
| `docs/` | The plan (`docs/PLAN.md`), architecture decisions (`docs/adr/`), design mockups, milestone notes |

The control plane and gateway behind Portenv Cloud are developed separately.

## Building

Requires Go 1.26 and, for the Swift parts, Xcode 26 on an Apple silicon Mac.

```sh
make build        # build portenvd, portenv-runner, portenv-agent and portenv into bin/
make test         # Go tests with the race detector
make lint         # formatting, vet, golangci-lint, buf lint, workflow lint, SPDX headers
make proto        # regenerate Go code from proto/
make check        # everything CI runs that works on this machine
```

Developer tools (buf, protoc plugins, golangci-lint, actionlint, gitleaks) are pinned in the `Makefile` and installed into `bin/` on first use.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
