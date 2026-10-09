# Portenv design guidelines (Mac app)

These guidelines turn the approved mockups into rules. When a mockup and this file disagree, this file wins and the mockup gets updated. When neither covers a case, follow Apple's Human Interface Guidelines for macOS, then ask.

Reference frames (design canvas): Main window, Main window moving, Command palette, Notification, Title menu, Agents, First run 1–6. The mockups are drawn in dark mode; every rule here applies to light mode too.

---

## 1. Principles

1. **The terminal is the product.** Everything else (toolbar, inspector, palette) exists to explain the box or act on it, and gets out of the way when not needed.
2. **Apple conventions first.** Title menu, menu bar, inspector, ⌘ shortcuts, notifications and system controls behave the way they do in Pages, Finder and Xcode. Don't invent where macOS already has an answer.
3. **Explain, then offer the obvious action.** A surface that shows state also offers the one action people want next ("Saves go to Test server", then "Move To…"). Settings live only in Settings.
4. **State is never more confident than the truth.** Save, move and agent states come from recorded facts, never from "the last operation succeeded". If unsure, say less.
5. **Agents are visible guests.** Whatever an agent does is attributed, visible in the window, and revocable in one click. Nothing an agent does looks like something you did.
6. **Nothing forces the cloud.** Hosted features appear as ordinary choices once they exist, never block a flow, never nag, and never appear before they exist.
7. **Few buttons.** A new control has to replace something or earn its place against a menu item or a palette command.

---

## 2. Vocabulary

The UI uses plain words. Internal terms never appear in the app, its notifications or its error messages.

| Internal term | In the app |
| --- | --- |
| box | box (the user's project workspace) |
| snapshot, restic, repository | save |
| point snapshot | save point |
| orphaned snapshot | "kept when you reverted" or "unsaved work kept from <machine>" |
| lease, lease holder | "open on <machine>" |
| runner, host, `user@IP` | the server's name ("Test server") |
| storage repository | "Saves go to <place>" |
| container, image, toolbox | not shown (use "box", or the toolbox's name in Settings only) |
| agent channel, gRPC, socket | not shown ("the box agent" only in error recovery) |
| stand-in mode | "On your behalf" |
| lane | "Own lane" |

Servers are always shown by the name the user gave them (default: the host name without the user). Never `ubuntu@35.180.23.92` in menus or titles.

---

## 3. Window anatomy

From top to bottom, left to right:

| Area | Contents | Notes |
| --- | --- | --- |
| Toolbar (unified, 52 pt) | sidebar toggle · title button · flexible space · open-port pills · agent avatars · inspector toggle | Title button is the box's document menu |
| Tab bar (30 pt) | one tab per terminal session, `+` for a new tab | Tabs replace tmux's status bar, which is always hidden |
| Terminal | blocks, sticky header, inline answer card | Fills the remaining space |
| Inspector (trailing, 280–320 pt) | Where it is · Saves · Who's here · Running now | Hidden by default, ⌥⌘I or toolbar button |

Minimum window size: 720 × 480 pt. Below 960 pt wide, the inspector overlays the terminal instead of pushing it.

### 3.1 Title button and state line

The title shows the box name, a sync symbol and a chevron; the line under it is the **state line**. Grammar: `<Location> · <Save state>`.

| Situation | Symbol | State line |
| --- | --- | --- |
| New box, never saved | none | `This Mac · Not saved yet` |
| Saving | `arrow.triangle.2.circlepath` (animated, unless Reduce Motion) | `This Mac · Saving…` |
| Saved | `checkmark.circle` | `This Mac · Saved at 14:58` (`Saved just now` for the first minute) |
| Offline | `icloud.slash`-style "not synced" symbol | `This Mac · Offline · will save later` |
| Moving | `arrow.triangle.2.circlepath` | `Moving to Test server…` |
| Open on a server | `checkmark.circle` | `Test server · Saved at 15:42` |
| Box agent unavailable | `exclamationmark.triangle` | `This Mac · Not saved since 14:58` (and Restart Box offered) |
| Just reverted | `checkmark.circle` | `Reverted to 14:31 · your changes were kept` for 5 s, then the normal line |
| Waiting for Keychain access | `lock` | `Waiting for Keychain access`, with "Check for a password prompt. It may be behind other windows." under it and a Cancel button; shown when a Keychain read hasn't returned after 2 s, with a notification when the window isn't in front |
| Portenv quit before saving it | `exclamationmark.triangle` | `This Mac · Not saved since 15:54 · Portenv quit before saving` until the save made on opening completes |

Rules:
- Times are local wall-clock time in the user's 12/24-hour setting. Never UTC.
- The state line is derived only from recorded save state: the last recorded save ID and time, the dirty flag, an upload in progress, the lease holder. A test covers each row of this table.
- Transient messages (revert, move finished) last 5 s, then return to the normal line.

### 3.2 Title menu and Box menu

The title menu and the menu bar's **Box** menu contain the same items, in the same order:

1. Rename…
2. Move To ▸ (This Mac, then servers by name, then "Add a Server…"; current location shows a **checkmark**, the others stay enabled; the current location is not disabled)
3. Duplicate for a Task… (when it exists)
4. ———
5. Revert To ▸ (Last Save Point with its time; Browse All Saves…)
6. Make a Save Point ⌘S
7. ———
8. Restart Box (only shown when the box agent is unavailable, or with ⌥ held)
9. Show in Finder ⌥⌘R

Menu items that can't work right now are disabled with no explanation in the menu; the explanation appears in the inspector or as an alert when chosen from the palette.

---

## 4. Terminal

### 4.1 Sessions and tmux

- Each tab is a persistent session in the box, so work survives the window closing, the Mac sleeping and the box moving.
- tmux is plumbing. Its status bar is always off, its prefix key never surfaces, and nothing in the UI mentions it.
- The app, not tmux, owns tab names, order, badges and the new-tab button.

### 4.2 Blocks

A block is one command and its output, from prompt to the next prompt.

Anatomy, left to right on the block's header row: author avatar (16 pt) · `$` · command (monospace) · flexible space · status · duration · start time.

| Status | Shown as |
| --- | --- |
| Running | `● running` in the accent blue, with elapsed time |
| Succeeded | `✓` in green |
| Failed | `✗ exit <code>` in red |
| Waiting for input | orange dot and "waiting for you" |

Rules:
- **Author is whoever sent the Enter that started the command.** If someone else typed into a command started by another author, the block shows both avatars ("You and Grok Bot").
- Your own blocks use your initial on a neutral grey. Each agent gets a stable color, assigned on first connection and kept per box.
- Full-screen and interactive programs (Claude Code, vim, less, top) are one block whose body is the live program. No block decorations inside it.
- Block background is a subtle tint (one step lighter than the terminal ground in dark mode, one step darker in light mode), radius 8 pt. No coloured side borders.
- Actions on a block (context menu and hover toolbar): Copy Command, Copy Output, Copy Both, Collapse, and Jump to Start.

### 4.3 Sticky header

When the top of the viewport is inside a block's output, that block's header row pins to the top of the terminal with a soft shadow, plus a "Jump to start" button (`arrow.up.to.line`). It unpins when the block scrolls fully out.

### 4.4 Inline answer card

Shown when a program in the visible tab is waiting for an answer that Portenv can map to choices (Claude Code permission prompts through its hooks; plain y/n prompts by recognition).

- Sits inside the running block, below the program's latest output, max width 560 pt.
- Content: small label ("Claude Code is asking"), the question in one line, one primary and one secondary answer button, a trailing link "Answer in the terminal" that dismisses the card and focuses the terminal.
- If an agent relayed the same question elsewhere, a footer says so: "Grok Bot also sent this to your chat. Answer in either place." The first answer wins everywhere; the card disappears when the question is answered from anywhere.
- Answers are sent as keystrokes through the box agent, attributed to you.
- Portenv never answers on its own. If an agent answers, the card shows who answered for 3 s, then disappears.

### 4.5 Type and colour

- Monospace: SF Mono (user's choice in Settings), 13 pt default.
- The terminal has an 8 pt margin on every side, in the terminal's own background colour: its text never touches the window's edge (`TerminalLayout.margin`).
- Terminal colours come from the user's chosen theme; Portenv's own decorations (block tint, headers, card) use system semantic colours so they work in light and dark mode.

---

## 5. Tab badges and notifications

| Event | In a background tab | In the visible tab | Window not in front |
| --- | --- | --- | --- |
| Command finished | green dot on the tab | nothing extra | notification only if the command ran longer than 30 s |
| Waiting for input | orange dot on the tab | inline answer card | notification with answer buttons |
| Agent connected or revoked | — | avatar appears or leaves | notification |
| Move finished | — | state line | notification only if the move took longer than 10 s |

Notification rules:
- Title: `<box> · <who> is asking` or `<box> · <event>`. Body: the question or result in one line. Subtitle line: who started it and where.
- Answer buttons in the notification are the same two as the card, plus "Show".
- One notification per question; a newer state replaces the older notification, it doesn't stack.
- Notifications respect Focus modes and the user's per-app settings. No sound by default.
- Dots always come with a text label for VoiceOver and a tooltip; colour is never the only signal.

---

## 6. Inspector

Opened with ⌥⌘I, the toolbar button (`sidebar.right`) or View › Show Inspector. Remembers its open state per window. It **explains and offers the obvious action**; it never holds settings.

Sections, always in this order, each with a small grey heading:

1. **Where it is.** "Running on <place>" with how long and where it came from; "Saves go to <place>" with "Encrypted on this Mac before they leave it" (or the right equivalent); a Move To… button. When packages from `apt-packages.txt` couldn't be installed: "1 package couldn't be installed: <name> · Retry" ("2 packages couldn't be installed: <a>, <b> · Retry"). The state line stays normal; the box started all the same and Portenv retries in the background.
   - During a move, this section becomes the move's progress, in four steps with a check, current or pending marker each: Saved on <source> (time, size of changes) → Sending changes (progress bar, "x of y MB") → Starting on <destination> → Reconnecting this window. Above the steps: a time estimate once known. Below: "You can keep reading. Typing comes back when the box is running there. If the move stops, the box stays here, saved."
2. **Saves.** "Last saved <time>", then the five most recent saves, newest first: time, kind and optional name. Kinds: Autosave, Save point ("name"), Saved before moving, Arrived from <place>, Kept when you reverted (with "Nothing reverted away is lost"). Hovering a row shows Revert. "Browse All Saves…" link below.
3. **Who's here.** You first, then each connected agent: avatar, name, mode ("On your behalf" or "Own lane"), "since <time>", any waiting state in orange text, and a Revoke button (destructive style, confirmation alert).
4. **Running now.** Tabs, open ports with "Open in Safari", saved commands with Run buttons and the note "Kept in the box, so they move with it".

Empty states use one short sentence ("No agents connected. Connect an Agent…").

---

### 6.1 Connect an Agent sheet

The user chooses the door for each agent, per box, and the sheet says honestly what each door allows. Portenv never picks silently. The sheet shows this wording, in plain language, before connecting.

**At launch the sheet offers two doors:** stand-in (over SSH, the CLI or the web terminal) and MCP (stdio or remote, with per-tool approvals). **The lane row appears only once lanes exist (Phase 4, 4.1).** It is never shown as available before then.

| Door | What the agent can do | Where limits are enforced |
| --- | --- | --- |
| Stand-in over SSH or the CLI | Anything you can, as you, in your session | It's recorded and revertible, and the vault rules hold (enforced by the operating system). Command approvals are only a speed bump: a shell can rephrase any command. |
| Lane (SSH, its own user) | Only what the lane allows | The operating system: a separate user, its own files, network limits, the vault rules |
| MCP (stdio or remote) | Only the tools you enable | Every call is structured, so you can approve, deny or be asked, action by action |
| Web terminal | What the SSH mode it's tied to allows, through a one-time link | The same as that SSH mode (stand-in at launch; a lane once lanes exist) |

- **A default suggestion only:** MCP for an agent you haven't used before (or a lane, once lanes exist), and stand-in when you trust the agent like yourself. The user can always choose otherwise.
- **Changing an agent's door or mode** takes effect immediately and is recorded.
- **A command an agent refused** (by its own safety checks or its permissions) reaches the user with the reason. Nothing hands it to another agent automatically.
- **Never describe SSH command rules as a security boundary,** anywhere: in the app, the docs or the website.
- Each door appears once its milestone lands: stand-in in 2.4, MCP in 2.6 and 2.7, the web terminal in 2.8, lanes in 4.1 (PLAN.md). A door that doesn't exist yet is never listed.

## 7. Command palette

- ⌘K anywhere in the window. Escape closes it. Arrow keys move, Return runs.
- Groups in this order, each hidden when empty: **Waiting for you** (questions to answer), **Box** (Move To, Make a Save Point, Revert To, Show Inspector), **Tabs** (New Tab, Go to <tab> with its badge), **Saved commands**, **Agents** (Connect an Agent…).
- Every palette item also exists in a menu. The palette never offers an action that can't be found elsewhere.
- Items show their shortcut or a short detail on the trailing side.
- Typing filters with fuzzy matching across all groups; the top match is selected.

---

## 8. Saved commands

- Stored in the box at `~/.portenv/commands` (format defined in the plan), so they move with it.
- Each has a name and a command line. Running one opens a new tab named after it, unless the command is already running there, in which case it focuses that tab.
- Shown in the inspector's Running now and the palette. Edited through Settings › Box, or by editing the file.

---

## 9. Hosted features

From the own-route rule (ADR 0008):
- Hosted options appear as ordinary choices once they exist. Choosing one opens a sheet that explains it and offers sign-in.
- Never required, never blocking, no banners or badges asking for an account, no "coming soon" items.
- If the service is unreachable, say so only when the user picks that option: "Can't reach Portenv right now. Your boxes keep working."

---

## 10. Writing

- Say what happened, what's safe, and what to do, in that order: "The move stopped because the server couldn't be reached. Your box is still here, saved at 15:02. Try again when the server is back."
- Name things the user named (box and server names). Use "you" and "your".
- Sentence case for buttons and headings. Ellipsis on commands that open a sheet or ask more ("Move To…", "Connect an Agent…").
- No exclamation marks, no blame ("You entered…"), no internal terms (section 2).
- Numbers: "8.2 of 12 MB", "about 6 seconds left", "3 min".
- **Errors say what to do next,** everywhere: the app, the CLI, `portenvd`'s messages, the guide. Give what happened, then the next step, as one plain line: "Keychain needs your approval. Open Portenv on this Mac to allow it." Agents read these too (PLAN.md, Agent readiness).

---

## 11. Visual tokens

Use system semantic colours (label, secondaryLabel, separator, controlAccentColor, windowBackground) for all Portenv chrome, so light mode, dark mode and increased contrast work automatically. The mockup hex values are dark-mode samples, not tokens.

| Role | Dark sample | Use |
| --- | --- | --- |
| Accent | system accent (blue) | primary buttons, running state, progress |
| Success | system green | finished dot, ✓ |
| Attention | system orange | waiting for you |
| Failure | system red | ✗ exit, Revoke |
| Agent colours | indigo, teal, purple, pink, brown, mint | assigned in that order per box |

Type: SF Pro for UI (13 pt body, 11 pt section headings semibold secondary, 12 pt details), SF Mono for terminal and commands. Spacing on a 4 pt grid; 8, 12, 16 and 24 pt are the common steps. Radii: 6 pt buttons, 8 pt blocks, 10–12 pt cards and palette. Icons: SF Symbols only (`sidebar.left`, `sidebar.right`, `checkmark.circle`, `arrow.triangle.2.circlepath`, `laptopcomputer`, `server.rack`, `lock`, `eye`, `plus`, `magnifyingglass`, `arrow.up.to.line`).

---

## 12. Accessibility

- Every control has a VoiceOver label; icon-only buttons say what they do ("Show inspector", "Revoke Grok Bot").
- Blocks are announced as "Command by <author>: <command>, <status>". The inline card is announced when it appears, without stealing focus from the terminal.
- Everything in the palette, menus and inspector is reachable by keyboard; Full Keyboard Access works.
- Contrast at least 4.5:1 for text, including grey details.
- Reduce Motion: no spinning symbols, no animated progress shimmer.
- Colour never carries meaning alone: dots have labels, status has symbols and words.

---

## 13. How it's built

| Feature | Approach |
| --- | --- |
| Terminal rendering | SwiftTerm, with Portenv decorations (block tint, header rows, sticky header, inline card) drawn as native overlays aligned to the terminal's rows, not as text in the grid |
| Persistent sessions, tabs | tmux in the box, status bar off; each app tab is one tmux window. Spike: tmux control mode (`-CC`, as iTerm2 uses) for structured per-tab output and native tab mapping |
| Block boundaries and exit codes | Shell integration in the box's skeleton home emits OSC 133 prompt marks (prompt start, command start, output start, command end with exit code); the box agent tracks them per tab |
| Attribution | All input reaches tmux through the box agent (the app, `portenv send`, SSH sessions via the forced command), so the agent knows who sent each Enter and tags the block. Agents never get raw tmux access |
| Waiting for input | The agent's "waiting for input" signal (idle plus prompt on screen), sharpened by adapters such as Claude Code hooks, which also give the answer choices |
| Tab badges, inline card | Driven by agent events over the agent channel |
| Notifications | `UNUserNotificationCenter` with action buttons; answers go back through the agent channel |
| Inspector | SwiftUI `.inspector` |
| Command palette | Native SwiftUI panel over the window |
| Saved commands | `~/.portenv/commands` in the box, read and run through the agent |

Phasing: Phase 1 ships the state line rules, title and Box menus, tmux hidden, inspector, tab badges, notifications and the inline answer card for Claude Code. Blocks with attribution, the sticky header, the palette and saved commands follow within Phase 1 if the OSC 133 and tmux control-mode spike succeeds; otherwise they move to Phase 2 and the plan says so.

---

## 14. Not doing

- A custom input editor replacing the shell's line editing. It conflicts with tmux and with agents typing into the same session.
- A built-in AI assistant. Portenv hosts the user's agents; it doesn't compete with them.
- Required sign-in, dashboards, upgrade banners or "coming soon" items.
- Decorative gradients, coloured side borders on cards, emoji in the UI.
