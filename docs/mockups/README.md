# Portenv mockups

Approved reference designs for the Mac app. Each screen comes as a PNG (what it should look like) and a static HTML file (exact copy, sizes and colors; open it in a browser and inspect).

These are design references, not code to port. Build the app natively in SwiftUI (see `docs/PLAN.md`, Desktop app UX) and match the layout, copy and hierarchy. Use system components and system colors where they exist; the hex values below are the dark-appearance targets.

The first-run screens show the full version, from Phase 3. Phases 1 and 2 ship a reduced first run with no sign-in, Only you protection and no Portenv storage; see `docs/PLAN.md`, Desktop app UX.

## Screens

| File | What it shows |
| --- | --- |
| `main-window` | The main window with the inspector open. Terminal as blocks with attribution (your `git diff --stat` between two Grok Bot commands), a sticky header for the block being scrolled, the inline answer card for Claude Code's question, tab badges (Grok Bot working in `claude`, finished build in `shell`, Meta Muse's lane in `muse`), and the inspector: Where it is, Saves, Who's here, Running now with saved commands. |
| `title-menu` | Clicking the title: Rename, Move To ▸, Duplicate for a Task, Revert To ▸ (Last Save Point, Browse All Saves), Make a Save Point ⌘S, Show in Finder. |
| `agent-presence` | Clicking an agent avatar: Meta Muse in its own lane, live terminal glimpse, a pending sudo request (opt-in approvals), Watch / Take Over, Revoke Access, and the matching notification. |
| `main-window-moving` | The same window during Move To ▸ Test server: the inspector's Where it is section becomes the move's four-step progress, the terminal dims, the state line says "Moving to Test server…". |
| `command-palette` | ⌘K: Waiting for you, Box, Tabs, Saved commands, Agents. |
| `split-view` | Split view (1.2b): three panes, each its own viewer of one tab. claude (focused, accent outline) with Claude Code's inline answer card; grok with Grok Bot typing; tests watching. Slim pane headers (tab picker, status, who's active, close), pane icons in the tab bar (dev is running but in no pane), Split right / Split down in the toolbar. |
| `notification` | The macOS notification when Claude Code is waiting and the window isn't in front, with Yes, push / Not yet / Show. |
| `first-run-1-welcome` | Continue with Apple, Continue with GitHub, Continue without an account (no sign-in is ever required for your own machines and storage; ADR 0008), or email. |
| `first-run-2-protection` | Only you (default) or You, with Portenv's help. |
| `first-run-3-recovery-key` | Recovery key with Save to Passwords, Print, Copy; Continue enabled only after the confirmation checkbox. |
| `first-run-4-storage` | Portenv storage preselected. In the app, "Use my own server or bucket" and "Keep on this Mac for now" are visible on the same screen, each one click, with no account needed (docs/PLAN.md, Desktop app UX); this render shows the alternatives expanded. |
| `first-run-5-first-box` | Name, Start from (GitHub, folder, empty), detected toolbox, Show this box in Finder. |
| `first-run-6-getting-ready` | Progress through toolbox, encrypted home, clone, start. Add "Setting up Portenv" as the first step on the first run only. |

## Design notes

- docs/design/GUIDELINES.md is the rulebook. If a mockup and the guidelines disagree, the guidelines win.
- Type: system font (SF Pro) for UI, SF Mono for the terminal. Title 13 pt semibold with an 11 pt secondary subtitle; first-run headings 24 pt bold.
- Colors (dark): window `#1A1A1C`, toolbar `#2B2B2E`, tab bar `#232325`, menus and popovers `#2C2C2E`, secondary text `#A1A1A6`, accent `#0A84FF` (buttons with white text use `#0A6EDB`), approval highlight `#FF9F0A`, destructive `#FF6961`.
- Agent identity colors: monogram circles, one hue per agent, consistent everywhere the agent appears (toolbar, tab, popover).
- Light appearance must be designed too; these renders only cover dark.
- Every control needs a VoiceOver label (icon-only buttons included).
