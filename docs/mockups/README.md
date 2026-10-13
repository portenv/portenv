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
| `box-list` | Your boxes: Box, Where it is (filled dot = running), Last saved, Agents; the "There’s no box called “demo”…" note; Find a box; New Box… and Open (default, ↩). Opening a box on a server connects to it there; nothing moves unless Move To. |
| `box-list-empty` | No boxes yet: one line on what a box is, and New Box… as the default. |
| `new-box` | New box from the list: ‹ Your boxes (Back), Name with hint, Start from (GitHub repository / Folder on this Mac / Empty), Link GitHub… when not linked (or paste a public URL), Toolbox (now with git and gh), Show this box in Finder, Cancel and Create Box. |
| `new-box-linked-invalid` | Linked to GitHub (the repository menu: which repository to clone; "Linked as …, same access as on your Mac · Switch Account…") and an invalid name: red field, "Start with a letter or a number.", Create Box disabled. |
| `link-github` | Link GitHub device flow, the same sign-in as `gh auth login` on the Mac: the code, Copy, Open github.com/login/device, waiting with expiry. Not a Portenv account. |
| `link-github-linked` | Linked: the box can reach every repository the GitHub account can, as on the Mac, and more can be cloned in later; the warning that agents in the box can use the login (inspector Who’s here, Sign Out of GitHub). |
| `notification` | The macOS notification when Claude Code is waiting and the window isn't in front, with Yes, push / Not yet / Show. |
| `first-run-1-welcome` | Continue with Apple, Continue with GitHub, Continue without an account (no sign-in is ever required for your own machines and storage; ADR 0008), or email. |
| `first-run-2-protection` | Only you (default) or You, with Portenv's help. |
| `first-run-3-recovery-key` | Recovery key (ADR 0019's test vector) with Print and Copy; the line says what the key protects; Continue enabled only after the confirmation checkbox. Save to Passwords dropped (R-0087). |
| `first-run-4-storage` | Portenv storage preselected. In the app, "Use my own server or bucket" and "Keep on this Mac for now" are visible on the same screen, each one click, with no account needed (docs/PLAN.md, Desktop app UX); this render shows the alternatives expanded. |
| `first-run-4-storage-this-mac` | What 1.5's design-tester build shows: "Keep on this Mac for now" selected, with "If this Mac is lost, they're lost too"; My own server and the S3 bucket dimmed and tagged LATER; no Portenv storage card (no account in 1.5). `first-run-4-storage` stays the Phase 3 design (R-0093). |
| `first-run-5-first-box` | Name, Start from (GitHub, folder, empty), detected toolbox, Show this box in Finder. |
| `first-run-6-getting-ready` | Progress through toolbox, encrypted home, clone, start. Add "Setting up Portenv" as the first step on the first run only. |

## Design notes

- docs/design/GUIDELINES.md is the rulebook. If a mockup and the guidelines disagree, the guidelines win.
- Type: system font (SF Pro) for UI, SF Mono for the terminal. Title 13 pt semibold with an 11 pt secondary subtitle; first-run headings 24 pt bold.
- Colors (dark): window `#1A1A1C`, toolbar `#2B2B2E`, tab bar `#232325`, menus and popovers `#2C2C2E`, secondary text `#A1A1A6`, accent `#0A84FF` (buttons with white text use `#0A6EDB`), approval highlight `#FF9F0A`, destructive `#FF6961`.
- Agent identity colors: monogram circles, one hue per agent, consistent everywhere the agent appears (toolbar, tab, popover).
- Light appearance must be designed too; these renders only cover dark.
- Every control needs a VoiceOver label (icon-only buttons included).
