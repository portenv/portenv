# Portenv.app

The native Mac app (SwiftUI). Designs: `docs/mockups/`.

Milestone 1.0 (walking skeleton): one window with a terminal in the box's tmux session (SwiftTerm, MIT) and the title menu, where Move To ▸ (This Mac, each server) and Revert To ▸ Last Save Point work; File › Make Save Point (⌘S) makes the save point to revert to. The app reaches boxes only through `portenvd` and the box agent's channel, never `docker exec`; until 1.1 it calls `portenvd` through the `portenv` CLI.

Run it from Xcode: `make build` (the binaries go to `bin/`), create a box with `bin/portenv init demo`, then open `apps/mac/Package.swift` in Xcode and run the Portenv scheme. `PORTENV_BOX` picks another box, `PORTENV_BIN_DIR` other binaries. See `docs/demo/1.0.md`.
