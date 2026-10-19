# Portenv design guidelines (Mac app)

These guidelines turn the approved mockups into rules. When a mockup and this file disagree, this file wins and the mockup gets updated. When neither covers a case, follow Apple's Human Interface Guidelines for macOS, then ask.

Reference frames (design canvas): Main window, Main window moving, Command palette, Notification, Title menu, Agents, First run 1–6; the Team frames are in `docs/mockups/team-*`. The mockups are drawn in dark mode; every rule here applies to light mode too.

---

## 1. Principles

1. **The terminal is the product.** Everything else (toolbar, inspector, palette) exists to explain the workspace or act on it, and gets out of the way when not needed.
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
| box | workspace (the user's project) |
| snapshot, restic, repository | save |
| point snapshot | save point |
| orphaned snapshot | "kept when you reverted" or "unsaved work kept from <machine>" |
| lease, lease holder | "open on <machine>" |
| runner, host, `user@IP` | the server's name ("Test server") |
| storage repository | "Saves go to <place>" |
| container, image, toolbox | not shown (use "workspace", or the toolbox's name in Settings only) |
| agent channel, gRPC, socket | not shown ("the workspace helper" only in error recovery) |
| stand-in mode | "On your behalf" |
| lane | "Own lane" |

**workspace**: what people see; called *box* in code, APIs and paths (R-0112). Write "workspace" in sentences and "Workspace" only where macOS capitalises: menu names, buttons and window titles ("New Workspace…", "Restart Workspace", the Workspace menu). Box IDs (`box-…`), proto names, paths, Keychain service names and log keys keep "box"; `scripts/check-workspace-word.py` (run by `make lint`) keeps the word out of the app's strings and portenvd's messages.

**the workspace helper**: what people read for `portenv-agent`, the helper inside each workspace (R-0132); only in error recovery. "Agent" means an AI agent and nothing else.

Servers are always shown by the name the user gave them (default: the host name without the user). Never `ubuntu@35.180.23.92` in menus or titles.

---

## 3. Window anatomy

From top to bottom, left to right:

| Area | Contents | Notes |
| --- | --- | --- |
| Toolbar (unified, 52 pt) | sidebar toggle · title button · flexible space · open-port pills · agent avatars · inspector toggle | Title button is the workspace's document menu |
| Sidebar (leading, 248 pt) | Needs you · Team (You, the agents, Add Agent…) | Shown by default when the workspace has a team, ⌘0 toggles it (6.2) |
| Tab bar (30 pt) | one tab per terminal session, `+` for a new tab | Tabs replace tmux's status bar, which is always hidden |
| Terminal | blocks, sticky header, inline answer card | Fills the remaining space |
| Inspector (trailing, 260–400 pt, 300 by default) | Where it is · Saves · Who's here · Running now | Hidden by default, ⌥⌘I or toolbar button |

Minimum window size: 640 × 440 pt. The inspector always sits beside the terminal and pushes it, never over it: only the window has a minimum width, so with the inspector open the terminal keeps at least 340 pt: the inspector stays at its 300 pt default (the owner, 2026-10-11; 340 pt measured, R-0086, R-0087).

### 3.1 Title button and state line

The title shows the workspace name, a sync symbol and a chevron; the line under it is the **state line**. Grammar: `<Location> · <Save state>`.

| Situation | Symbol | State line |
| --- | --- | --- |
| New workspace, never saved | none | `This Mac · Not saved yet` |
| Saving | `arrow.triangle.2.circlepath` (animated, unless Reduce Motion) | `This Mac · Saving…` |
| Saved | `checkmark.circle` | `This Mac · Saved at 14:58` (`Saved just now` for the first minute; from the next day `Saved yesterday at 23:43`, then `Saved on 9 Oct at 23:43`, with the year once it's another year; the words change at midnight with no new save) |
| Offline | `icloud.slash`-style "not synced" symbol | `This Mac · Offline · will save later` |
| Moving | `arrow.triangle.2.circlepath` | `Moving to Test server…` |
| Closing a workspace (Close Workspace, saving before it's closed) | `arrow.triangle.2.circlepath` | `Saving before closing…`, with the Workspace menu disabled, until the list shows; the normal line again if the person stays |
| Open on a server | `checkmark.circle` | `Test server · Saved at 15:42` |
| Workspace helper unavailable | `exclamationmark.triangle` | `This Mac · Not saved since 14:58` (and Restart Workspace offered) |
| Just reverted | `checkmark.circle` | `Reverted to 14:31 · your changes were kept` for 5 s, then the normal line |
| Waiting for Keychain access | `lock` | `Waiting for you to allow access to Portenv's keys…`, with "Allow it in the password or Touch ID prompt." under it (R-0214; the window that asked comes to the front first, so the prompt shows over it) and a Cancel button; shown when a Keychain read hasn't returned after 2 s, with a notification when the window isn't in front |
| Portenv quit before saving it | `exclamationmark.triangle` | `This Mac · Not saved since 15:54 · Portenv quit before saving` until the save made on opening completes |

Rules:
- Times are local wall-clock time in the user's 12/24-hour setting. Never UTC.
- The state line is derived only from recorded save state: the last recorded save ID and time, the dirty flag, an upload in progress, the lease holder. A test covers each row of this table.
- Transient messages (revert, move finished) last 5 s, then return to the normal line.

### 3.2 Title menu and Workspace menu

The title menu and the menu bar's **Workspace** menu contain the same items, in the same order:

While the workspace is closed, **Open** comes first, then a divider (R-0211). The closed window says "<name> is closed" with Open as its default button; after a failed open it shows portenvd's plain line and Try Again instead, never only an alert.

1. Rename…
2. Move To ▸ (This Mac, then servers by name, then "Add a Server…"; current location shows a **checkmark**, the others stay enabled; the current location is not disabled)
3. Duplicate for a Task… (when it exists)
4. ———
5. Revert To ▸ (Last Save Point with its time at the trailing edge, "11:52 today", disabled when the saves are known and none is a save point; Browse All Saves…)
6. Make a Save Point ⌘S
7. ———
8. Restart Workspace (only shown when the workspace helper is unavailable, or with ⌥ held)
9. Show in Finder ⌥⌘R

Menu items that can't work right now are disabled with no explanation in the menu; the explanation appears in the inspector or as an alert when chosen from the palette.

---

## 4. Terminal

### 4.1 Sessions and tmux

- Each tab is a persistent session in the workspace, so work survives the window closing, the Mac sleeping and the workspace moving.
- tmux is plumbing. Its status bar is always off, its prefix key never surfaces, and nothing in the UI mentions it.
- The app, not tmux, owns tab names, order, badges and the new-tab button.
- **macOS's own window tabs are off** (no Show Tab Bar, Merge All Windows or Move Tab to New Window in the Window menu): the app's tab bar is the only tabs.
- **The current tab is marked by more than a shade** (§12): its name in semibold, and an accent line along its bottom edge, on top of the mockup's background. (A shade alone was too faint at the Mac, 2026-10-10.)
- **Each viewer keeps its own current tab.** The app and each agent see the same tabs, but choosing a tab moves only that viewer's terminal: an agent switching tabs never changes the owner's.
- **Tab shortcuts:**

  | Shortcut | Action | Menu |
  | --- | --- | --- |
  | ⌘T | New tab | File › New Tab |
  | ⌘W | Close the current tab; on the last tab, close the window (which saves and releases the workspace), like Terminal and Safari | File › Close Tab |
  | ⌘1–⌘9 | Show the tab at that position; past the last tab, nothing | Window › Show Tab ▸ |
  | ⌘⇧[ / ⌘⇧] | Show the previous / next tab, wrapping around | Window › Show Previous Tab, Show Next Tab |
  | ⌃⇧Tab / ⌃Tab | The same, on any keyboard layout (⌘⇧[ needs a bracket key, which AZERTY and other layouts lack), as in Safari and Terminal | — |

- **What a program in the workspace can't make the terminal do** (R-0133, R-0134; `TerminalGuard`, which every terminal view gets from `TerminalViews`):
  - **The clipboard:** it can't read or set the Mac's clipboard (OSC 52).
  - **Links:** only web pages (http, https) open, and only on the person's own ⌘-click; a `file:` link, `javascript:` or another app's scheme never opens.
  - **Before anyone clicks:** hovering a link shows its full destination, as a tooltip and in the state line ("Opens https://…"), since a program can label a link anything. A link Portenv won't open says so there instead: "Portenv opens only web links: …".
  - **What's shown is the link that opens** (R-0135, R-0140): one decision serves the tooltip, the state line and the click.
    - **The host shows in punycode,** so a look-alike domain shows as `xn--…`.
    - **A link with any control or bidi character never opens,** since those could hide or reorder part of it. The state line says "Portenv won't open a link with hidden characters".
    - **A link with a user name before the address never opens** either (`https://apple.com@evil.com/` reads as apple.com). The state line names where it really goes, never the user name or password: "Portenv won't open a link with a user name before the address. It goes to evil.com."
- **Closing a tab that still runs a program asks first:** "Close this tab? <program> is still running.", with Close Tab and Cancel. The program is the tab's foreground process as the workspace helper reports it at that moment. A tab with only its shell closes without asking. The last tab has no close button; ⌘W closes the window instead.

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
- Your own blocks use your initial on a neutral grey. Each agent gets a stable color, assigned on first connection and kept per workspace.
- Full-screen and interactive programs (Claude Code, vim, less, top) are one block whose body is the live program. No block decorations inside it.
- Block background is a subtle tint (one step lighter than the terminal ground in dark mode, one step darker in light mode), radius 8 pt. No coloured side borders.
- Actions on a block: the hover toolbar (shown on hover and on the selected block) has Copy Output, Copy Command and Run Again, which VoiceOver offers as the block's actions; the context menu has those three plus Copy Both, Collapse and Jump to Start. ⌥⌘C copies the selected block's output. Copy Output copies the whole block and says first how many lines left the scrollback. Run Again asks first unless portenvd recorded that you ran the command (R-0224).

### 4.3 Sticky header

When the top of the viewport is inside a block's output, that block's header row pins to the top of the terminal with a soft shadow, plus a "Jump to start" button (`arrow.up.to.line`). It unpins when the block scrolls fully out.

### 4.4 Inline answer card

Shown when a program in the visible tab is waiting for an answer that Portenv can map to choices (Claude Code permission prompts through its hooks; plain y/n prompts by recognition).

- Sits inside the running block, below the program's latest output, max width 560 pt.
- Content: small label ("Claude Code is asking"), the question in one line, one primary and one secondary answer button, a trailing link "Answer in the terminal" that dismisses the card and focuses the terminal.
- If an agent relayed the same question elsewhere, a footer says so: "Grok Bot also sent this to your chat. Answer in either place." The first answer wins everywhere; the card disappears when the question is answered from anywhere.
- Answers are sent as keystrokes through the workspace helper, attributed to you.
- Portenv never answers on its own. If an agent answers, the card shows who answered for 3 s, then disappears.

### 4.5 Type and colour

- Monospace: SF Mono (user's choice in Settings), 13 pt default.
- The terminal has an 8 pt margin on every side, in the terminal's own background colour: its text never touches the window's edge (`TerminalLayout.margin`).
- Terminal colours match the system appearance by default (the owner, 2026-10-11): the system's text background and text colours, a light and a dark ANSI palette with every normal colour at least 4.5:1 on its background, and the accent for the caret and selection. A theme menu (View › Terminal Appearance) comes later. Portenv's own decorations (block tint, headers, card) use system semantic colours so they work in light and dark mode.

---

## 5. Tab badges and notifications

| Event | In a background tab | In the visible tab | Window not in front |
| --- | --- | --- | --- |
| Command finished | green dot on the tab | nothing extra | notification only if the command ran longer than 30 s |
| Waiting for input | orange dot on the tab | inline answer card | notification with answer buttons |
| Agent connected or revoked | — | avatar appears or leaves | notification |
| Move finished | — | state line | notification only if the move took longer than 10 s |

Notification rules:
- Title: `<workspace> · <who> is asking` or `<workspace> · <event>`. Body: the question or result in one line. Subtitle line: who started it and where.
- Answer buttons in the notification are the same two as the card, plus "Show".
- One notification per question; a newer state replaces the older notification, it doesn't stack.
- Notifications respect Focus modes and the user's per-app settings. No sound by default.
- Dots always come with a text label for VoiceOver and a tooltip; colour is never the only signal.

---

## 6. Inspector

Opened with ⌥⌘I, the toolbar button (`sidebar.right`) or View › Show Inspector. Remembers its open state per window. It **explains and offers the obvious action**; it never holds settings.

Sections, always in this order, each with a small grey heading:

1. **Where it is.** "Running on <place>" with how long and where it came from; "Saves go to <place>" with "Encrypted on this Mac before they leave it" (or the right equivalent); a Move To… button. When packages from `apt-packages.txt` couldn't be installed: "1 package couldn't be installed: <name> · Retry" ("2 packages couldn't be installed: <a>, <b> · Retry"). The state line stays normal; the workspace started all the same and Portenv retries in the background.
   - During a move, this section becomes the move's progress, in four steps with a check, current or pending marker each: Saved on <source> (time, size of changes) → Sending changes (progress bar, "x of y MB") → Starting on <destination> → Reconnecting this window. Above the steps: a time estimate once known. Below: "You can keep reading. Typing comes back when the workspace is running there. If the move stops, the workspace stays here, saved."
2. **Saves.** "Last saved <time>" (the state line's rule: "just now", "14:58", "yesterday at 23:43", "on 9 Oct at 23:43"), then the five most recent saves, newest first: time, kind and optional name. Kinds: Autosave, Save point ("name"), Saved before moving, Arrived from <place>, Kept when you reverted (with "Nothing reverted away is lost"); and, for kinds portenvd records today: Saved when the workspace closed (a close), Kept from <machine> (this Mac's copy kept when a newer save came from elsewhere). Hovering a row shows Revert. "Browse All Saves…" link below.
3. **Who's here** (only for a workspace without a team; the Team section replaces it, 6.2). You first, then each connected agent: avatar, name, mode ("On your behalf" or "Own lane"), "since <time>", any waiting state in orange text, and a Revoke button (destructive style, confirmation alert).
4. **Running now.** Tabs, each with what runs in it ("Idle" for just the shell), open ports with "Open in Safari", saved commands with Run buttons and the note "Kept in the workspace, so they move with it".

Empty states use one short sentence ("No agents connected. Connect an Agent…").

---

### 6.1 Connect an Agent sheet

The user chooses the door for each agent, per workspace, and the sheet says honestly what each door allows. Portenv never picks silently. The sheet shows this wording, in plain language, before connecting.

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
- **Pairing approval** (ADR 0016): when an agent runs `portenv connect`, the app asks "<Agent> wants to join <workspace>, code <code>", for example "Grok Bot wants to join acme-api, code 7F3-K2Q". The code shown is the one the agent printed, so the user can match them.

### 6.2 Team (the agent team, ADR 0022)

The Team section is the workspace's sidebar when the workspace has a team (agent files in `.portenv/agents/`). Mockups: `team-sidebar`, `team-agent-selected`, `team-decision`, `team-states`, `team-agent-files`.

**The sidebar** (R-0240, R-0241):
- Shown by default when the workspace has a team and hidden otherwise; ⌘0 (View › Show Sidebar) toggles it, remembered per workspace. Width 248 pt. Below 900 pt of window width it collapses to avatars and status symbols, with names in tooltips and VoiceOver.
- From the top:
  1. **Team activity:** the whole team's messages (below).
  2. **Needs you · <n>**, when a decision is pending: one row per decision, oldest first, with the question in one line and how long it has waited ("Waiting 6 min").
  3. **Team:** You first ("You", with your tab count), then one row per agent in agent-file order, then files that can't start, then **Add Agent…**.
- An agent row: avatar, name, status as a word and a symbol, and the agent's latest board line, one line, truncated. Statuses: ▶ running (accent), ◌ idle, ■ stopped, ● needs you (orange), ⚠ can't start (red, with portenvd's reason as the line), starting. Colour never carries the status alone.
- Remote agents (ADR 0022 §10) appear only once a remote way in exists, under **Connected from outside**, as connected or disconnected. Portenv never starts them.
- **The agent limit** (R-0347): **Settings › Team › Agents allowed** is how many agents can run at once in one workspace, 1 to 8, 4 by default, for every workspace on the Mac. `portenvd` keeps and enforces it; the app only shows it. Its line: "How many agents can run at once in one workspace, from 1 to 8. Lowering it stops no agent that's running." Below the agent rows, the Team section shows `portenvd`'s line about it:
  - one short of the limit or at it, a quiet secondary line: "3 of 4 agents allowed in this workspace.", "4 of 4 agents allowed: the team is at your limit.";
  - over it, a warning (orange ⚠ and primary text): "This team has 6 agents, and you allow 4 in a workspace: only 4 can run at once.", with **Raise to 6…**, which asks first: "Allow 6 agents in each workspace?", "Agents allowed in Settings › Team goes from 4 to 6, for every workspace on this Mac. You can lower it there.", **Allow 6** and Cancel. The raise is the owner's choice, never automatic;
  - after a lower setting: "6 agents are running, and you now allow 4. They keep running; no more start until fewer than 4 run." Lowering never stops an agent.
  - A Start or Approve past the limit is refused by `portenvd` with one line under the button that says what to do: "Builder can't start: 4 agents are running in this workspace, and you allow 4. Stop one, or raise Agents allowed in Portenv's Settings › Team."

**Selecting:**
- **An agent:** its tabs expand beneath its row, the tab bar shows only its tabs (named "Builder · tests"), with **All tabs** at its trailing end, and its column takes the inspector's place: who it runs as, its runner and its pane grant ("Runs as user builder · Claude Code · may open 4 panes"), its messages with you, its Team section, its Agent files, and a field to message it (R-0313, R-0314).
  - **Messages with you** come first: what it sent you, what you sent it, and Portenv's notices to you about it.
  - **Team · <n>**, below them, **collapsed by default**: every other message this agent sent or received, oldest first. Each entry is one line, "Lead → Builder", the time and the message's first line; clicking it shows the whole text. Portenv's own notices about the agent (a routine's note, a status request, a start that failed) are in the same list, greyed, from "Portenv".
  - **The message field** is the column's last row: "Message Builder", with Send, and "Return to send · Shift-Return for a new line" under it. Return sends, Shift-Return starts a new line. It sends exactly what you typed, as you, to this agent only, through portenvd (an idle agent wakes to read it). Plain text, at most 8 KB. A refusal shows under the field in red, as one line that says what to do ("A message can be at most 8 KB; this one is 9.2 KB. Shorten it and send again."), and keeps what you typed.
- **Team activity:** the whole team's messages in the column's place, oldest first, as the board has them: each one as in the Team section ("Lead → Builder", the time, the first line; click for the rest), Portenv's notices greyed. A menu at the top filters by agent ("Everyone", or one agent: what it sent, what it received and Portenv's notices about it). The tab bar shows all tabs and the terminal stays on the tab it showed. It's how the owner sees every message that involves their agents (ADR 0016 point 5).
- **Long messages:** an opened entry shows its first 12 lines or 600 characters, then **Show all** for the rest (a message is at most 8 KB).
- **Live:** the column, the Team section and Team activity update as messages arrive (WatchTeam), with no refresh.
- **You or All tabs:** the inspector comes back. All tabs shows every tab; You shows yours.
- **The terminal follows the selection** (R-0314): it always shows exactly the tab the tab bar marks current (in split view, the focused pane does). Selecting an agent shows its tab last shown, or its first; You or All tabs goes back to your tab. An agent with no tab open shows "<Agent> has no tab open", never another agent's pane. An agent starting never moves the view. The terminal's VoiceOver name says whose tab it is: "Terminal: Lead · lead", "Terminal: You · shell".
- A pane an agent opened is headed "Opened by <agent> · <command>".
- Who's here (section 6) is dropped from the inspector while the workspace has a team; the Team section replaces it.

**Decisions** (ADR 0022 §6):
- Every decision shows who asks, the question, its evidence (the detail, the diff or the package list), **Decline…** (asks for an optional reason) and **Approve**, with no default button, and the line "Answered only here, by you. Agents can ask but never approve."
- Answered only in the app: never from a notification, the CLI or an agent.
- An agent-file change shows its diff, and its `runner` line on its own, prominently: that's code that will run (R-0245).
- A package decision lists every package with its exact version and the repository it comes from, and says "Installing runs these packages' setup scripts as root."
- The first start of a team shows the residual-risk sentence from ADR 0022 §2.
- An expired decision is greyed, with no Approve; it never becomes an approval.
- A question with choices (`ask-owner --choice`, CC-0501) shows one button per choice, in order, full width and wrapping, in Approve's place, with no default button, then **Decline…**. Each label is plain text. Answered, it reads “Chose “<label>””. VoiceOver: “Choose “Option b: install deps”: <question>”.

**Agent files** (R-0234): below the agent's conversation, its private home (`/home/<agent>`) with each item's size, the quota ("212 MB of 500 MB · drop files here to add"), Add Files… and drag and drop (the drop target names what will be added and its size). Full: the bar turns red, with what wasn't added and what to do.

**Plain text:** everything an agent writes (its latest line, messages, questions, evidence, file names, portenvd's reasons) is shown as plain text: no links, Markdown, images or actions in it. Control characters, escape sequences and bidi overrides are removed before it's shown. Controls next to it, such as "Open review-42.md", are the app's own.

**VoiceOver:** each row is one element, read as its name, status and latest line ("Reviewer, needs you. Wants to merge #42"). Decision buttons say what they answer ("Approve: merge #42"). A message entry is one element, read as who to whom, the time and its first line ("Lead to Builder, 14:28: Take the retry half"); a notice starts "Notice from Portenv". The message field is read as "Message Builder".

### 6.3 New workspace: Project, Team, Review and approve, Creating (ADR 0025)

New workspace is one sheet with its steps named at the top: Name · Project · Team · Review and approve, then Creating. Back and Cancel on every step; Continue is the default button until Review and approve, which has none. First run's "Make your first workspace" uses the same steps. Mockup: `new-box-team` (the reviewer's four artboards, R-0322).

**Words:** "starter folders" and "starter files" for what agent files call `home_seed`; "team", "agent", "runner" and "model" as here; never "template file", "manifest", "SHA" or "pin" except the one hash line on Review and approve.

**Project:**
- Four choices as a segmented control: From GitHub, From a Git URL, From a folder on this Mac, Empty.
- From GitHub: Sign In to GitHub… until signed in; then a search field over the repositories, one row each (owner/name, Private when it is, last pushed), and a Branch menu with the default branch first. The sign-in screen (Portenv's device flow by default, or "Use My GitHub CLI Login" when gh is signed in on this Mac) says plainly: "Portenv can access your repositories; it only uses this for the repository in this workspace." Once signed in, one grey line under the list: "Signed in as <account>. Portenv can access your repositories; it only uses this for the repository in this workspace. The sign-in stays on this Mac; agents never get it."
- From a folder on this Mac: Choose Folder…, then its size and file count, and "Leave out installed dependencies (node_modules and similar). They're rebuilt in the workspace.", ticked.
- A size problem is said before anything is fetched, under the choice, in red with what to do: "acme-api needs about 7.4 GB with its team. This workspace has 5 GB free. Leave an agent out, or choose a smaller repository."
- **Starter folders:** "Add Starter Folder…" below the choices, then one row per folder with its size and a remove button ("Remove brand-assets"). Starter folders go to agents' homes, never into the project; the Team step assigns them.

**Team:**
- **Pick a team** as a list of choices: None ("Just you. Add agents later."), the built-in templates with their agents named ("Orchestrate · Lead, Builder, Reviewer, Design keeper"), the owner's saved teams with when they were saved, then Import Team….
- **Adjust**, one row per agent: include checkbox, avatar, name field (the id below it, monospace, grey), role, Runner menu, Model menu, Starter files menu. A left-out agent's row is dimmed and keeps its place. A runner or model that differs from the template says so in grey under the menu ("Changed from Sonnet").
- The Runner menu lists only runners the build offers (MVP: Claude Code, under the heading Anthropic); the Model menu lists that runner's models (Opus, Sonnet, Fable). A runner whose idle point Portenv doesn't know says, under its name in the menu and on the agent's row: "Routines and wakes are off for <runner>."
- Below the list: "Agents sign in themselves, in their own tabs. Templates never carry logins." and the checkbox **Start the team when it's ready**, unticked.

**Review and approve** (the rules of 6.2's decisions apply):
- A list of agents on the left, the project's summary at its top. Each agent row shows a state as a word and a symbol: Not opened (○), Opened (◐), Approved (✓, green), Changed (● orange, "changed, needs approval"). Colour never carries it alone.
- The selected agent on the right, in this order: the runner line, full and monospace; the change from the template on its own line, when there is one ("Changed from the template: runner claude (sonnet) → claude (opus)."); the grants in plain words, one line each; the `team_status` checkbox, unticked ("Let Lead see the team's status"); starter files with sizes; the instructions in full, as plain text; "Written to .portenv/agents/reviewer.md · SHA-256 3f9a…c21e".
- **Leave Out** and **Approve <agent>**, with no default button. Approve becomes "Approved ✓" and the next unopened agent is selected.
- **Approve All** under the list, no default button, enabled only once every agent has been opened; until then its line says "Open every agent first (1 left)."
- Create Workspace is disabled until every included agent is approved, and says why under it ("Approve Reviewer to create the workspace."). ADR 0022's first-start sentence sits above it: "Agents work in their own copies. Check their branches before you run them."

**Creating:**
- The steps with a check, current or pending marker: Workspace, Project ("Cloning acme-api… 58% · 1.2 of 2.1 GB, about 40 s left", a progress bar, and **Cancel**), Agent files, Agents' copies, Starter files, Ready.
- Under them, always: "Nothing starts until you choose Start." (or, when ticked, "The team starts when it's ready.") and "Each agent signs in to its runner in its own tab the first time it starts."
- Cancel says what it does before doing it: "Stop cloning? The new workspace is removed. Nothing else changes." with Keep Cloning and Stop.

**The first push** (ADR 0025 §6): a small sheet on the workspace window, "Allow pushes to acme/acme-api from acme-api?", the line "Pushes come only from your own tabs. Agents can never push.", and Don't Allow and Allow, with no default button. The inspector's Where it is then shows "This workspace can push to acme/acme-api" with Stop Allowing Pushes. The sheet repeats the sign-in's scope line: "Portenv can access your repositories; it only uses this for the repository in this workspace." A sign-in GitHub no longer accepts asks the owner to sign in again.

**Saved teams:** Save Team as Template… in the Team sidebar's menu and the Workspace menu opens a sheet: name, description, the agents (their approved files), and starter folders per agent, with "Logins and secrets are never saved." Export Team… and Import Team… use the save and open panels.

**VoiceOver:**
- each step is announced with its place ("Team, step 3 of 4");
- an agent row on Team reads as one element with its controls after it: "Reviewer, included, Claude Code, Opus, changed from Sonnet"; the menus are labelled with the agent ("Runner for Reviewer", "Model for Reviewer");
- an agent row on Review and approve reads its state ("Reviewer, changed, needs approval");
- Approve All's disabled reason is its hint; Approve buttons name the agent ("Approve Reviewer");
- the clone's progress is a progress element with its value ("Cloning acme-api, 58 percent"), announced at most every 10 %.

## 7. Command palette

- ⌘K anywhere in the window. Escape closes it. Arrow keys move, Return runs.
- Groups in this order, each hidden when empty: **Waiting for you** (questions to answer), **Workspace** (Move To, Make a Save Point, Revert To, Show Inspector), **Tabs** (New Tab, Go to <tab> with its badge), **Saved commands**, **Agents** (Connect an Agent…).
- Every palette item also exists in a menu. The palette never offers an action that can't be found elsewhere.
- Items show their shortcut or a short detail on the trailing side.
- Typing filters with fuzzy matching across all groups; the top match is selected.

---

## 8. Saved commands

- Stored in the workspace at `~/.portenv/commands` (format defined in the plan), so they move with it.
- Each has a name and a command line. Running one opens a new tab named after it, unless the command is already running there, in which case it focuses that tab.
- Shown in the inspector's Running now and the palette. Edited through Settings › Workspace, or by editing the file.

---

## 9. Hosted features

From the own-route rule (ADR 0008):
- Hosted options appear as ordinary choices once they exist. Choosing one opens a sheet that explains it and offers sign-in.
- Never required, never blocking, no banners or badges asking for an account, no "coming soon" items.
- If the service is unreachable, say so only when the user picks that option: "Can't reach Portenv right now. Your workspaces keep working."

---

## 10. Writing

- Say what happened, what's safe, and what to do, in that order: "The move stopped because the server couldn't be reached. Your workspace is still here, saved at 15:02. Try again when the server is back."
- Name things the user named (workspace and server names). Use "you" and "your".
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
| Agent colours | indigo, teal, purple, pink, brown, mint | assigned in that order per workspace |

Type: SF Pro for UI (13 pt body, 11 pt section headings semibold secondary, 12 pt details), SF Mono for terminal and commands. Spacing on a 4 pt grid; 8, 12, 16 and 24 pt are the common steps. Radii: 6 pt buttons, 8 pt blocks, 10–12 pt cards and palette. Icons: SF Symbols only (`sidebar.left`, `sidebar.right`, `checkmark.circle`, `arrow.triangle.2.circlepath`, `laptopcomputer`, `server.rack`, `lock`, `eye`, `plus`, `magnifyingglass`, `arrow.up.to.line`).

---

## 12. Accessibility

- Every control has a VoiceOver label; icon-only buttons say what they do ("Show inspector", "Revoke Grok Bot").
- Blocks are announced as "Command by <author>: <command>, <status>". The inline card is announced when it appears, without stealing focus from the terminal.
- Everything in the palette, menus and inspector is reachable by keyboard; Full Keyboard Access works.
- Contrast at least 4.5:1 for text, including grey details.
- Reduce Motion: no spinning symbols, no animated progress shimmer.
- Colour never carries meaning alone: dots have labels, status has symbols and words.
- The web terminal (PLAN.md 2.8) keeps its screen as page text (xterm.js's DOM renderer or screen-reader mode), offers larger-font and high-contrast options, and shows the state line as plain text outside the terminal, so browser agents and screen readers can read it.

---

## 13. How it's built

| Feature | Approach |
| --- | --- |
| Terminal rendering | SwiftTerm, with Portenv decorations (block tint, header rows, sticky header, inline card) drawn as native overlays aligned to the terminal's rows, not as text in the grid |
| Persistent sessions, tabs | tmux in the box, status bar off; each app tab is one tmux window. Spike: tmux control mode (`-CC`, as iTerm2 uses) for structured per-tab output and native tab mapping |
| Block boundaries and exit codes | Shell integration in the box's skeleton home emits OSC 133 prompt marks (prompt start, command start, output start, command end with exit code); the workspace helper tracks them per tab |
| Attribution | All input reaches tmux through the workspace helper (the app, `portenv box send`, SSH sessions via the forced command), so the helper knows who sent each Enter and tags the block. Agents never get raw tmux access |
| Waiting for input | The agent's "waiting for input" signal (idle plus prompt on screen), sharpened by adapters such as Claude Code hooks, which also give the answer choices |
| Tab badges, inline card | Driven by agent events over the agent channel |
| Notifications | `UNUserNotificationCenter` with action buttons; answers go back through the agent channel |
| Inspector | SwiftUI `.inspector` |
| Command palette | Native SwiftUI panel over the window |
| Saved commands | `~/.portenv/commands` in the box, read and run through the agent |

Phasing: Phase 1 ships the state line rules, title and Workspace menus, tmux hidden, inspector, tab badges, notifications and the inline answer card for Claude Code. Blocks with attribution, the sticky header, the palette and saved commands follow within Phase 1 if the OSC 133 and tmux control-mode spike succeeds; otherwise they move to Phase 2 and the plan says so.

---

## 14. Not doing

- A custom input editor replacing the shell's line editing. It conflicts with tmux and with agents typing into the same session.
- A built-in AI assistant. Portenv hosts the user's agents; it doesn't compete with them.
- Required sign-in, dashboards, upgrade banners or "coming soon" items.
- Decorative gradients, coloured side borders on cards, emoji in the UI.
