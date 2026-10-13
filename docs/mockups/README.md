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
| `first-run-3-recovery-key` | Recovery key (ADR 0019's test vector) with Print and Copy; the line says what the key protects; Continue enabled only after the confirmation checkbox. Save to Passwords dropped (R-0087). |
| `first-run-4-storage-this-mac` | What 1.5's design-tester build shows: "Keep on this Mac for now" selected, with "If this Mac is lost, they're lost too"; My own server and the S3 bucket dimmed and tagged LATER; no Portenv storage card (no account in 1.5). The Phase 3 storage board (Portenv storage) and the Protection step were removed (R-0136): first run has five steps. |
| `first-run-5-first-box` | Name, Start from (GitHub, folder, empty), detected toolbox, Show this box in Finder. |
| `first-run-6-getting-ready` | Progress through toolbox, encrypted home, clone, start. Add "Setting up Portenv" as the first step on the first run only. |
| `add-a-server` | Add a Server (2.9, R-0136), four steps with no terminal: **Where** (name, address, user; a key on this Mac or one made just for this server, its private key in the Keychain), **first contact** (the host key fingerprint, confirmed and then pinned), **Check** (changes nothing on the server; each failure says what to do; turning off password logins is an optional, recommended checkbox), **Set up** (Docker Engine, the runner, encrypted space, space for saves, a test workspace started and removed; carries on if the window closes) and **Ready** (Move a Workspace Here…, Show in Settings › Servers). Opens as a sheet from Settings › Servers and from Move To ▸ Add a Server…. When someone else sets up the server (a teammate or an admin), the person starts with "Someone is setting one up for me": Portenv makes a key just for that server and they send its public half; the address, user and fingerprint come back later (R-0140, R-0145). The fingerprint step asks the person to check it against the one their provider, or whoever set the server up, gives them. |
| `move-and-lease` | Three sheets on the workspace window (2.3, R-0136): the move confirmation (what stops, what comes along, agents disconnected); "open somewhere else" (Connect to it there, the default; Move it to this Mac; Look without changing anything, a row the app hides until it ships with 1.4's Browse Saves, while the frame stays in the board, R-0141); and "the other place isn't answering" (the newest save's time, later work kept as a separate save, Keep Waiting or Move to This Mac). The person never meets the word "lease". |
| `settings` | Portenv › Settings… (⌘,), a toolbar window with five panes (R-0137): **Servers** (state, … menu, Add a Server…), **Agents** (access with scope, expiry and Revoke; "Let agents connect over SSH" marked PREVIEW and off), **Recovery Key** (saved state, Check…, Make New Key…, where saves go), **Updates** (automatic check that asks before installing, the version, Check Now; what the update check sends; Send Feedback…). General (appearance, terminal font) isn't drawn. Every row is one VoiceOver element. |
| `agent-connect` | Letting an agent in (2.4, R-0138): Connect an Agent… shows the command to give the agent, the mode (On your behalf or In its own lane) and the duration; the approval ("Grok Bot wants to connect", the code in large type, Deny and Allow with no default button); and "access ends in 10 minutes" with Let It End or Extend 1 Hour, never past 24 hours. |
| `command-blocks` | Command blocks (R-0139): success folds once it scrolls away, failure stays open with its exit code and a red edge, running shows its time with a blue edge, long output keeps the first and last 20 lines; who ran it is portenvd's recorded author; Copy Output, Copy Command, Run Again; the sticky header; link tooltips; the update prompt's copy. |

## Design notes

- docs/design/GUIDELINES.md is the rulebook. If a mockup and the guidelines disagree, the guidelines win.
- Type: system font (SF Pro) for UI, SF Mono for the terminal. Title 13 pt semibold with an 11 pt secondary subtitle; first-run headings 24 pt bold.
- Colors (dark): window `#1A1A1C`, toolbar `#2B2B2E`, tab bar `#232325`, menus and popovers `#2C2C2E`, secondary text `#A1A1A6`, accent `#0A84FF` (buttons with white text use `#0A6EDB`), approval highlight `#FF9F0A`, destructive `#FF6961`.
- Agent identity colors: monogram circles, one hue per agent, consistent everywhere the agent appears (toolbar, tab, popover).
- Light appearance must be designed too; these renders only cover dark.
- Every control needs a VoiceOver label (icon-only buttons included).
