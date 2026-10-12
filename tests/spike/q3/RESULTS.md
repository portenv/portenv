<!-- SPDX-License-Identifier: Apache-2.0 -->
# Spike question 3: results (for ADR 0017)

The streams are real tmux 3.4 output, recorded in the toolbox image with the repository's bash integration by `capture.py`. They're replayed into headless SwiftTerm 1.20.0 terminals with the overlay tracker by `apps/mac/Tests/PortenvTermTests/TmuxReplayTests.swift`.

There are three paths:
- **A:** today's attached tmux client. tmux's `allow-passthrough` is on and marks are wrapped, but only inside this capture, as a measured comparison; a bare OSC 133 is dropped by tmux.
- **A-main:** the same stream without tmux's alternate-screen switches.
- **C:** each pane's own output from a control-mode client.

**Prompt marks** (placed / misplaced, and prompt lines left without a mark):

| Scenario | A | A-main | C |
| --- | --- | --- | --- |
| scroll (30 commands) | 0 kept | 24 / 23 | 30 / 0, 0 unmarked |
| long output (300 lines) | 0 kept | 6 / 6 | 11 / 0, 0 unmarked |
| a pager (less) | 0 kept | 5 / 5 | 11 / 0, 0 unmarked |
| clear | 0 kept | 1 / 1 | 6 / 0, 0 unmarked |
| a second tab, then back | 0 kept | 5 / 4 | 16 / 0, 0 unmarked |
| resize (80×24 → 60×20 → 100×30) | 0 kept | 13 / 12 | 20 / 0, 0 unmarked |
| **Total** | **0** | **54 / 51** | **94 / 0, 0 unmarked** |

**Command-start marks** (placed / misplaced):
- **A-main:** 69 / 67.
- **C:** 93 / 0.

**Scroll positions:** at every position checked, C's placed prompt marks were all on prompt rows. A-main's weren't: 168 misplaced placements across the six scenarios.

**Why A fails:**
- An attached client draws on the alternate screen, which has no scrollback, so the tracker keeps no mark.
- Without the alternate screen, the outer scrollback holds only the single top line tmux scrolls off each time. tmux repaints by cursor addressing, and on every tab switch, resize and clear.
- A mark printed in a tab that isn't showing never reaches the client.

**The prototype** (`MarkedTerminalView`) runs on a real SwiftTerm view, off screen, fed C's recorded control-mode output (`OverlayPrototypeTests`).
- Bands are on prompt rows at the bottom and scrolled up through the scrollback, after a resize too.
- The rows match where SwiftTerm puts its own caret.
