# Phase 1 plan: the rest of the native Mac app (1.1–1.8)

Status: **proposed, for the owner's review.** No work starts until it's approved. Once approved, it folds into `docs/PLAN.md` (Phase 1) and this file goes.

Inputs:
- `docs/PLAN.md` (Phase 1 and its checklist) and `docs/design/GUIDELINES.md`;
- the owner's demo notes and decisions (2026-10-08/09);
- the test-path audit (#20);
- the follow-ups from #26 and #27.

## Where Phase 1 stands

**Done in 1.0 and 1.0b, and in the PRs since:**
- **Through the real path:** a minimal `portenvd` and runner, the agent channel (ADR 0010), and the app's terminal in the box's tmux session.
- **Box actions:** save point, Revert To, and Move To a server and back, the same on a server box as on the Mac.
- **Save state:** an honest state line with the full table of states (#26, §3.1), retrying and not-saved states (#23), and "quit before saving" (#25).
- **Quitting:** never fails silently (the alert and the marker, #25); the channel is checked after wake (#25); boxes still opening are waited for (#25).
- **Deadlines:** on every restic run and every process on the daemon side, enforced in CI (#23).
- **Offline open:** near 2 s (ADR 0011).
- **The guideline basics (#26):** title and Box menus in the guideline order, servers by name, tmux hidden, VoiceOver labels.
- **Packages:** a package that can't be installed never stops a box (#28; the inspector line waits for the inspector).
- **The CLI built in CI, fetched by commit (#27);** the test-path rule and mutation checks (#20).

**From the demo notes:**
- servers shown by name: done (#26, by host name until servers can be named);
- the subtitle after a revert: done (#26);
- a checkmark on the current location: done (1.0b, kept in #26);
- **visible progress during a move: not done** (the inspector's four steps, 1.2 and 1.4).

**Still on Phase 1's gate (from `PLAN.md`):** first run, a signed and notarized download with updates, offline work, the throttled-uplink Move, no docker exec anywhere, VoiceOver and both appearances, the port relay, and the restic probe in an `apple` box.

## Order of work

### 1. Keychain calls never wait silently (first, before 1.1)

Found closing `retest`: a new dev build's first Keychain read waited 14½ minutes behind a prompt while the window said only "Opening…".

- **a. The state line says so.**
  - If a Keychain call hasn't returned after 2 s, the state line reads "Waiting for Keychain access", with a secondary line: "Check for a password prompt. It may be behind other windows."
  - A new row goes in the state-line table in `GUIDELINES.md` §3.1.
  - A notification goes out too, for when the window isn't in front.
- **b. Never on the main thread.** Every Keychain call runs off the main thread, so the window stays responsive and Cancel works while the prompt is up. Cancel stops the open cleanly: no half-open box, no held lease.
- **c. No timeout while a person might be answering.** In the app, the call doesn't fail while the prompt could still be answered. The state line shows, and the person can cancel.
- **d. Non-interactive paths fail fast.** The CLI without a TTY, tests, and anything an agent or overnight run triggers use `kSecUseAuthenticationUI = Fail` (or the equivalent), so the call returns `errSecInteractionNotAllowed` at once instead of prompting. The error is one plain line: "Keychain needs your approval. Open Portenv on this Mac to allow it." A test proves the no-prompt path returns it.
- **e. Root cause, confirmed first:**
  - check whether the prompt comes from the dev build's ad-hoc signature changing on every rebuild, so "Always Allow" no longer matches;
  - if so, an ADR (or a PLAN note) says the fix is a stable signing identity once Apple Developer enrolment lands;
  - until then, test boxes on this Mac are expected to prompt after a rebuild, and a–d make that visible.
- **f. Overnight runs:** Keychain calls follow the same rule as restic runs. Every call has an owner who answers it or gets a clear error, never a silent wait.

Tests:
- the 2 s state row, on a fake key store that blocks;
- Cancel during a blocked call leaves no box or lease (an e2e with a blocking fake);
- the no-prompt error (d);
- the call runs off the main thread (a main-thread assertion in the key store).

### 2. 1.1: `portenvd`'s API in the app, and a login item

- The app talks to `portenvd` over gRPC on its socket instead of running the `portenv` helper for each action.
  - That removes the "main-actor work doesn't run during the quit wait" workaround.
  - It also gives push updates for the state line instead of the 3 s poll.
- A login item (`SMAppService`) starts `portenvd`.
- **The Phase 0 CLI's `docker exec` path is removed** (ADR 0010 condition 4, and the gate). `portenv init`, and the server-side `join` and `status` used by Move To enrolment, move into the daemon API or stay as plain commands that never touch a box. The audit lists them.

### 3. 1.2: the main window

In this order, so the visible problems go first:

1. **Terminal margin:** the text touches the window's left edge. Add about 8 pt and write the value into `GUIDELINES.md` §4.5.
2. **The box's hostname is its name.** The prompt reads `work@acme-api`, not `work@portenv`, so people and agents can tell which box they're in. The name is sanitised to a valid hostname: lowercase letters, digits and hyphens, 63 characters at most, no leading or trailing hyphen, with a fallback.
3. **The tab bar (early, §3 and §4.1).** One tab per tmux window; `+` for a new tab; the app owns names and order.
   - Until it exists, nothing (`portenv send`, agents) can create a tmux window the user can't see. The agent refuses `new-window` from anything but the app, or maps it to the single visible window.
4. **The inspector (§6):**
   - **Where it is**, including the move's four-step progress (the demo note), the packages line from #28, and Retry;
   - **Saves** (the latest five);
   - **Who's here** (you; agents come in Phase 4);
   - **Running now** (tabs, ports).
5. **Notifications (§5)** for a finished long command and a move over 10 s, and the Keychain wait from item 1. Answer buttons come with the inline answer card.
6. **The spike: tmux control mode (`-CC`) and OSC 133 (§13), time-boxed to 3 days.**
   - It answers: can the agent map app tabs to tmux windows through control mode? Can shell integration in the skeleton home emit OSC 133 marks that the agent tracks per tab, with exit codes, for bash and zsh, including inside Claude Code? Do SwiftTerm overlays stay aligned to rows when scrolling?
   - **If it succeeds,** Phase 1 also gets blocks with attribution, the sticky header, the inline answer card for Claude Code, tab badges, the command palette and saved commands, in that order.
   - **If it doesn't,** they move to Phase 2 and the plan says so.

### 4. 1.3: the `apple` driver

The Containerization shim, with `docker` as the fallback. The agent drops the forbidden capabilities itself (a VM's root holds them). Gate item: the restic probe passes inside an `apple` box.

### 5. 1.4: autosave, Changes, Browse Saves

- **Autosave:** on a schedule and on idle, through the bounded runner. A failed autosave retries on its own schedule (ADR 0012), so the "Not saved since · retrying" row becomes routine.
- **Browse Saves**, which provides the time for **Revert To ▸ Last Save Point (#26 follow-up)**: `portenvd` keeps the save list from its last listing, so the menu shows the time without a new listing.
- **Move progress:** the bytes still to send and the measured bandwidth give the time estimate (the throttled-uplink gate item).
- **Test gaps from the audit, now on the real path:** lease refusal, take-over and rule 4 with kept work, and autosave freshness through `portenvd`. The server gate's timing limits and LUKS checks move onto the runner path.

### 6. 1.5: first run

Reduced: no sign-in, Only you, Keychain keys, the recovery key; the Welcome screen has "Continue without an account" (#16).

### 7. 1.6: the port relay

A dev server on the box's localhost opens in Safari. Port pills in the toolbar.

### 8. 1.7: shared folder, then the File Provider

Show in Finder (⌥⌘R) stops being disabled here.

### 9. 1.8: releases

Developer ID signing, notarisation and Sparkle, with the key backups. Signed public CLI releases and their signing key are already on the first-release checklist (#27).
- Needs Apple Developer enrolment (open question), which is also the stable-signing fix for item 1e.

## Standing items

- **The next server session starts with `scripts/fetch-cli.sh` against the server** (#27). The three server scripts install CI's binaries by commit; fix whatever fails there and then. It doesn't block anything before.
- **#26 follow-ups:**
  - Last Save Point time in the menu (1.4);
  - Restart Box with ⌥ in the menu bar's Box menu (1.1, once the menu bar is AppKit-backed or SwiftUI can see ⌥);
  - **a live VoiceOver check and a title-menu screenshot with the owner at the Mac** (the next session at the Mac; the screen was locked overnight).
- **Every milestone keeps the test-path rule:** app-relied behaviour is tested through `portenvd` or the runner and the channel, each check mutation-checked, and every e2e call has a time limit.

## Open questions this plan raises

- Item 1e's ADR depends on what the root-cause check finds.
- Tab bar before the spike: if control mode wins, the tab bar is rebuilt on it. Doing the tab bar first and adapting it is cheaper than waiting; flagging it in case the owner prefers waiting.
- The hostname for a box whose name isn't a valid hostname: the plan sanitises it; does the owner want the original name shown anywhere else (the window title already has it)?
