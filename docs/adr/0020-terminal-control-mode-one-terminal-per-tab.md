# 0020. The app's terminal uses tmux control mode, one terminal per tab

Date: 2026-10-13 · Status: proposed. The direction is approved (the reviewer, on the owner's delegation, R-0132 ruling 1); this ADR is the design that ruling asked for, and the per-tab interface is agreed with lane B for split view.

## Context

The owner wants command blocks, split view and mockup conformance in the signed tester build (R-0102, R-0131). Blocks draw on the terminal: a band and header on each prompt row, the command's status, the sticky header, and the answer card. They need two things:
- marks that say where each prompt and command is: OSC 133 from the shell integration (ADR 0017, question 2);
- an overlay that stays on those rows while the person scrolls, output streams, and the window resizes (question 3).

Today the app's terminal is a plain `tmux attach` client, running `portenv attach` in a SwiftTerm `LocalProcessTerminalView`. The bytes SwiftTerm gets are tmux's drawing of the current window, not the tab's own output. Spike question 3 measured what that means.

## The spike's result (question 3)

The streams below are real. tmux 3.4 in the toolbox image, with the repository's bash integration, ran six scenarios: 30 commands; a 300-line output; `less`; `clear`; a second tab, then back; and a resize from 80×24 to 60×20 to 100×30. They were recorded three ways (`tests/spike/q3/capture.py`) and replayed into headless SwiftTerm 1.20.0 terminals with the overlay tracker (`TmuxReplayTests`). `tests/spike/q3/RESULTS.md` has the per-scenario table.

| Path | Prompt marks placed / on the wrong line | Prompt lines left without a mark | Command-start marks placed / wrong | Wrong placements while scrolling |
| --- | --- | --- | --- | --- |
| **A:** an attached client, as today | **0 kept** | — | 0 | — |
| **A-main:** the same, without tmux's alternate screen | 54 / **51** | — | 69 / **67** | **159** |
| **C:** each tab's own output, from tmux control mode | 94 / **0** | **0** | 93 / **0** | **0** |

- **A keeps nothing.** tmux 3.4 drops a bare OSC 133 on its way to an attached client. With `allow-passthrough on` and every mark wrapped for passthrough, the marks arrive. Passthrough was on **only inside the capture**, as the measured comparison.
  - Even then, an attached client draws on the alternate screen, so SwiftTerm has no scrollback and the tracker ignores every mark.
  - Marks printed in a tab that isn't showing never reach the client at all.
- **A-main is wrong almost everywhere.** Without the alternate screen, SwiftTerm's scrollback holds only the single top line tmux scrolls off each time, never the tab's history. tmux redraws by cursor addressing, and repaints on every tab switch, resize and clear, so a mark pinned to a line ends up on other text.
- **C is right everywhere.** The tab's own bytes carry the marks with the text they belong to. Each tab has its own SwiftTerm buffer and scrollback, so tab switches don't disturb it, and a resize reflows SwiftTerm's own buffer.

**The prototype.** `MarkedTerminalView` (PortenvTerm) is a SwiftTerm `TerminalView` with an overlay: a tint band and a rule on each prompt row, and the previous command's status at the right of its end mark's row. It's redrawn on scroll, output and resize.
- `OverlayPrototypeTests` feeds it the recorded control-mode output, off screen. Every band is on a prompt row at the bottom and scrolled up through the scrollback, after the resize too, and the rows match where SwiftTerm draws its own caret.
- What remains is the reviewer's UI test on a live box, after decision 2 is built.

Also checked on tmux 3.4 in the image:
- `send-keys -H` delivers bracketed paste, UTF-8, an SGR mouse report and DEL unchanged to a program reading the terminal in raw mode.
- A control-mode client sets each window's size separately (`refresh-client -C @N:WxH`).
- `capture-pane -e -S -` returns a tab's history with its attributes.

**Typing latency, measured (R-0132).** The harness is `tests/spike/q3/latency.py`.
- **What it measures:** keystroke to echo. It types into bash at its prompt, one key at a time, each followed by a backspace, in tmux 3.4 in the toolbox image on this Mac's Docker Desktop.
- **The two paths:** an attached tmux client on a pty (today's path), against a control-mode client typing with `send-keys -H` and reading the echo from `%output`.

| Run | Attached client, p50 / p95 / p99 | Control mode, p50 / p95 / p99 | Control mode − attached, at p95 |
| --- | --- | --- | --- |
| idle, 500 keys | 0.44 / 1.09 / 2.36 ms | 0.62 / 1.34 / 3.11 ms | +0.25 ms |
| another tab printing nonstop, 500 keys | 0.16 / 0.51 / 0.89 ms | 0.13 / 0.45 / 0.75 ms | −0.07 ms |
| idle, 1,000 keys | 0.52 / 1.11 / 2.39 ms | 0.51 / 1.06 / 2.70 ms | −0.04 ms |
| idle, 1,000 keys | 0.53 / 1.08 / 3.37 ms | 0.53 / 0.94 / 2.01 ms | −0.14 ms |

- **At tmux, control mode is within ±0.25 ms of the attached client at p95.** The budget is +5 ms.
- **The relay is the same on both paths.** portenvd and the workspace helper's channel carry either one.
- **The gate** is measured end to end (decision 12).

## Decision

1. **The app's terminal shows each tab through its own stream of that tab's output.** It's read from tmux control mode, one SwiftTerm terminal per tab, and never through an attached tmux client.
   - Tabs stay tmux windows with the same IDs (#41). The tab bar, the per-tab programs, and the app owning tab names don't change.
2. **The workspace helper (the box agent) runs one control-mode client per tmux session** (`tmux -C attach-session -t <session>`, `LANG=C.UTF-8`, attached when the session starts). It serves:
   - **the app's tab terminals** (below);
   - **tab events.** `%window-add`, `%window-close` and `%window-renamed` replace #41's 300 ms polling in WatchTabs; ADR 0017 decided this;
   - **the waiting-for-input detector and OSC 133 tracking per tab** (ADR 0017), from the same `%output`.

   If the client exits, the agent starts it again and every open tab stream ends with `lost(retrying: true)`.
3. **The proto.** One new RPC on the agent, relayed by portenvd exactly as `Terminal` is (same socket checks, same box ownership, on this Mac or on a server):

   ```proto
   // One tab's terminal: its output from tmux control mode, input and size
   // to it. The first message from the client must be open.
   rpc TabTerminal(stream TabTerminalRequest) returns (stream TabTerminalResponse);

   message TabTerminalRequest {
     oneof msg {
       TabTerminalOpen open = 1;
       bytes input = 2;          // keystrokes, pastes, mouse reports, as the emulator produced them
       TerminalSize resize = 3;  // the size this view can show, in cells
     }
   }

   message TabTerminalOpen {
     string session = 1;       // the tmux session, for example "main"
     string tab = 2;           // a tab ID, "@N"
     TerminalSize size = 3;
     string author = 4;        // who types here ("app"; an agent's name from 2.4): for block attribution
   }

   message TabTerminalResponse {
     oneof msg {
       TabTerminalReset reset = 1;   // always first; again after the agent's control client restarts
       bytes output = 2;             // the tab's own output, in order
       TerminalSize resized = 3;     // the tab's size changed elsewhere
       TabTerminalEnd end = 4;       // nothing follows
     }
   }

   message TabTerminalReset {
     TerminalSize size = 1;
     bytes seed = 2;  // history + screen + cursor + modes, with known OSC 133 marks replayed in place
   }

   enum TabTerminalEnd {
     TAB_TERMINAL_END_UNSPECIFIED = 0;
     TAB_TERMINAL_END_TAB_CLOSED = 1;
     TAB_TERMINAL_END_BOX_STOPPING = 2;
   }
   ```

   The existing `Terminal` RPC stays for `portenv attach` (decision 8).

   - **Opening a tab that's gone** ends promptly with `TAB_CLOSED`: within one control-client round trip, and never by waiting for a timeout.
   - **Tab events** (`WatchTabs`, `portenv.types.v1.Tab`) gain what split view's pane header needs. The stream itself stays bytes only.
     - **`waiting`:** the question text from ADR 0017's detector, empty and `secret` for a password prompt. It feeds "asking you", the answer card and the badge.
     - **`last_output_at`:** feeds "quiet since 15:41".
     - **`typing_author`:** a name, and whether it's the person at this Mac. It feeds "Grok Bot is typing".
     - **`watchers`:** other viewers with the tab open and not typing (an agent, a CLI attach). It feeds "watching".
     - **`program` stays.**

     The app derives the header's one-word status from them (`TabInfo.activity`). The order is: asking, then typing, then watching, then quiet (a running program silent for 60 s), then running, then idle.
   - **`NewTab` never changes another view's tab.** A `TabTerminal` stream has no "current tab": it shows the tab it opened. `NewTab` and `SelectTab` change only the calling viewer's own current window, which exists only for attached clients (the CLI).
4. **Input** goes to the tab's pane as `send-keys -t %P -H <hex>…` on the session's control client, in chunks of at most 512 bytes. Commands on one control client run in order, so the bytes arrive in the order typed.
   - The agent records which stream sent each Enter, for "author is whoever sent the Enter" (GUIDELINES §4.2). Today, input through an attached client can't be told apart.
5. **Size.** A view's `resize` becomes `refresh-client -C @N:<cols>x<rows>` for that tab's window, with `window-size latest`, as today.
   - **In split view, a tab shows in at most one pane.** So two app views size one tab only through another viewer: a CLI attach or an agent.
   - **One tab has one size.** The latest view to resize a tab sets it, whether that's the app, another pane showing the same tab, or a CLI attach.
   - **The other views get `resized` and draw that size.**
     - **Smaller than the view:** the terminal's own background fills the rest.
     - **Larger than the view:** the view keeps the cursor's row in sight, rather than clipping to the top-left corner.
   - **On a `reset` at a size other than its own,** the view asks for its own size at once. The latest size asked for wins, so an app view that reconnects takes the tab back.
6. **Reset, and reconnecting.** Every stream starts with `reset`, built on the session's control client in this order: capture, then live output. Control mode returns a command's reply in its `%begin`…`%end` block in order with `%output`, so nothing is lost or repeated between the seed and what follows.
   - **The seed:**
     - `capture-pane -p -e -S - -E -` of the main screen (history and visible lines);
     - then, when `#{alternate_on}`, `\e[?1049h` and `capture-pane -a -p -e` of the alternate screen;
     - then the cursor position, and the modes tmux reports (cursor keys, keypad, bracketed paste, mouse) as their escape sequences.
   - **Mark replay:**
     - **Recording:** for each OSC 133 mark it sees, the agent records the mark's line. It reads `#{history_size}`/`#{cursor_y}` on the same client right after the `%output` holding the mark, and keeps the SHA-256 of that line's text.
     - **Replaying:** in the seed, the mark's escape sequence goes at the start of its line when the captured line still matches. A mark whose line no longer matches is dropped (trimmed history, a cleared screen).
     - **Rule:** a replayed mark is never on the wrong line, and a missing one costs only a block header in old history.
   - **The app's side:** after a reconnect (portenvd or the agent restarting, a move, the Mac waking), the app opens the stream again and draws the new `reset`. The tab's history is the box's, so nothing depends on what the app had kept.
7. **The app's per-tab terminal interface** (`apps/mac/Sources/PortenvKit/TabTerminal.swift`, in this PR, so split view can build on it now). See "The interface" below.
8. **The CLI stays a plain attach.** `portenv attach` and `portenv open --tab` keep the `Terminal` RPC and #41's per-viewer grouped sessions, for a person in Terminal, Ghostty or a tiling terminal; their shells get the same OSC 133 marks directly. Agents' terminal access (2.4) is unchanged.
9. **Security: tmux's `allow-passthrough` stays off,** in the product, the image and the agent's tmux options. A test checks it, and turning it on is the owner's decision.
   - **What changes:** control mode carries the tab's bytes as the program wrote them. So the app's SwiftTerm now receives escape sequences that tmux used to interpret or drop before they reached an attached client.
   - **Whose job filtering becomes:** the app's terminal treats every byte from the box as untrusted. The per-tab view's delegate:
     - doesn't read or set the Mac's clipboard (OSC 52 read returns nothing, writes are ignored);
     - doesn't open links on a program's request;
     - ignores titles (the app owns tab names);
     - ignores iTerm2's inline-file sequences.
   - **The test:** a sequence suite (OSC 52 set and query, OSC 8, OSC 0/2, OSC 1337 File=, DCS and APC strings, DECRQSS) shows none of them reaches the clipboard, opens anything or renames a tab.
   - **The reviewer and the owner rule** on whether this counts as a change to a security guarantee. The intent is the same guarantee as today, enforced in the app instead of in tmux.

10. **tmux is pinned in the toolbox image,** and its version is checked before control mode is used.
    - **The image:** the Dockerfile installs `tmux=3.4-*` (Ubuntu 24.04's, under the digest-pinned base), and `images/toolbox-node/test.sh` fails unless `tmux -V` says 3.4.
    - **At attach:** the helper reads `tmux -V` before starting its control client, and on every `TabTerminal` open. Below 3.4, the version this design was measured on, the open fails with "This workspace's tmux is too old for the app's terminal (needs 3.4 or later)". The app then shows that message instead of a terminal.
    - Raising the pin is a PR that reruns the spike's replay, latency and no-loss tests on the new version.
11. **No-loss guarantees carry over from #73 and #75.**
    - **The `Terminal` RPC** that `portenv attach` uses keeps its tests unchanged. They cover piped input that's never read ending in DataLoss, the count settling before it's read, the drain after the program exits, and `TestTerminalCountsWhatTheProgramLeftAtItsExit`.
    - **`TabTerminal` gets the equivalents:**
      - **Output:** every byte a tab's program wrote before it exits reaches the stream before `TAB_CLOSED`. Control mode delivers a pane's `%output` before its `%window-close`, and the helper drains the stream before ending it.
      - **Input:** every byte sent reaches the pane in order, or the stream ends with an error naming how many bytes weren't delivered (a `send-keys` refused because the pane has gone). It's never dropped silently.
      - **Ordering:** no output is lost or repeated between a `reset`'s seed and the output after it (decision 6).
    - Each guarantee gets a test written before the code (see Risks).
12. **The latency gate, and a dev-only switch back to the attached client.**
    - **The gate:** keystroke to echo drawn in SwiftTerm, on dev-A, through portenvd and the helper. Control mode must be no worse than the attached client by more than **5 ms at p95**, on a local workspace.
      - Measured by an app-side harness: a key event into the view, timed to the echo's first byte reaching the view.
      - The same measurement runs on the us-west-2 tester server once one exists (R-0117, R-0118), and its numbers go in the milestone note.
    - **The switch:** until the gate passes, a dev-lane build (`gmake app LANE=X ATTACHED_TERMINAL=1`) can show the old attached terminal instead.
      - It's a Swift compilation condition (`PORTENV_ATTACHED_TERMINAL`) set only for lane builds. The Makefile refuses it without `LANE`.
      - `scripts/release-local.sh` and `check-app-identity.sh` refuse an app built with it, read from a marker in its Info.plist.
      - It never reaches a release build, and it's deleted once the gate passes.
13. **Agents' terminal access is unchanged in what it permits.** Agents (2.4) keep today's paths and limits: the attached `Terminal` RPC through their own viewer, `portenv screen`/`send`/`wait`, and ADR 0016's grants.
    - `TabTerminal` is opened by the app over portenvd's socket, which only the Mac's user can reach (ADR 0014). This decision gives agents nothing new.
    - Whether agents later move to `TabTerminal`, for per-author attribution of their keystrokes, is an open question (PLAN.md). It would be a separate decision, with the same grants.

## The interface (for split view and blocks)

`PortenvKit/TabTerminal.swift`:

```swift
public struct TabTerminalSize: Equatable, Sendable { public var cols: Int; public var rows: Int }

public enum TabTerminalEnd: Equatable, Sendable {
    case tabClosed                 // closed by anyone
    case boxStopped                // stopped, or moved away from this Mac
    case lost(retrying: Bool)      // connection gone; retrying: a new .reset follows
}

public enum TabTerminalEvent: Equatable, Sendable {
    case reset(TabTerminalSize, seed: [UInt8])   // first, and after every reconnect: clear, size, draw
    case output([UInt8])                         // the tab's own output
    case resized(TabTerminalSize)                // the tab's size changed elsewhere
    case ended(TabTerminalEnd)                   // nothing follows
}

@MainActor public protocol TabTerminalSource: AnyObject {
    var tab: String { get }                          // "@N", as TabInfo.id
    var events: AsyncStream<TabTerminalEvent> { get } // one consumer
    func send(_ input: [UInt8])                      // as the emulator produced it
    func resize(_ size: TabTerminalSize)             // the latest size asked for wins
    func close()                                     // stops the stream; the tab keeps running
}

@MainActor public protocol TabTerminalProvider: AnyObject {
    func open(tab: String, size: TabTerminalSize) -> any TabTerminalSource
}

@MainActor public final class ScriptedTabSource: TabTerminalSource { … }  // play(_:), sent, sizes, closed
```

**`TabInfo`** (`PortenvKit/Tabs.swift`) gains the fields for the pane header (decision 3):
- `waiting: TabQuestion?` (`text`, `secret`; a secret question has no text);
- `lastOutputAt: Date?`;
- `typingAuthor: TabAuthor?` (`name`, `isYou`);
- `watchers: [String]`;
- `program` stays;
- `activity(now:)` returns a `TabActivity`: `.asking`, `.typing`, `.watching`, `.quiet(since:)`, `.running` or `.idle`, in that order.

**The view** is `MarkedTerminalView` (PortenvTerm), the spike's prototype that becomes the real per-tab view.
- **Its source:**
  - `attach(_ source:)` shows a tab, closing the previous stream; `detach()` stops showing it.
  - `onReset(size)` is called after each reset is drawn, and `onEnded(TabTerminalEnd)` when the stream ends.
  - On `.tabClosed` the last screen stays.
- **What blocks use:**
  - `tracker` holds the marks;
  - `overlay` is the layer blocks draw on;
  - `cards` is the slot for the answer card, which takes clicks only on its cards;
  - `rect(screenRow:)` places things on a row.
- **What goes back to the source:** input and size go through SwiftTerm's delegate. The view ignores a program's clipboard, link and title requests (decision 9).
- **What it never takes:** the menu's shortcuts (⌘D, ⇧⌘D, ⌘⌥ and the arrows, ⌘W, ⌘1–9). It doesn't override `performKeyEquivalent`, and a test keeps it that way.
- **Edit › Find (⌘F)** goes to the first responder, which is the focused pane's terminal. SwiftTerm handles `performTextFinderAction`.

**The pane** (PortenvTerm, for split view):
- **`TabTerminalHost`** is the NSView a pane holds.
  - `show(_ source:)` swaps the source in place (detach, then attach). The view, its scroll position and its selection belong to the pane, and survive focus changes and tab switches.
  - `focus()` makes the terminal first responder.
  - `onFocusChange(Bool)` follows the window's first responder, so a click in the terminal focuses the pane.
- **`TabTerminalPane(source:label:isFocused:onReset:onEnded:)`** is its SwiftUI wrapper.
  - A new `source` swaps in place without recreating the view.
  - `label` is what VoiceOver reads ("Pane: grok tab, Grok Bot is typing").
  - `isFocused` is a two-way binding.
  - `onReset` and `onEnded` drive the pane header: "Reconnecting…" while `.lost(retrying: true)` waits for a reset, and "Disconnected" with Reconnect on `.lost(retrying: false)`. Reconnect calls `provider.open` again.
  - `.boxStopped` is handled by the window.

**Rules split view builds on:**
- **The provider:** one `TabTerminalProvider` per workspace window, `controller.tabTerminals`.
- **A pane:**
  - holds one `TabTerminalPane`;
  - choosing another tab means a new source from `provider.open(tab:size:)`;
  - closing the pane closes its source, and the tab keeps running;
  - a tab shows in at most one pane.
- **Until 1.2c's control-mode provider lands,** panes run on `ScriptedTabSource`. The live provider plugs in without changing the panes.

## Risks, and how each is tested

| Risk | Test (written before the code) |
| --- | --- |
| Input loses or changes bytes: paste, IME, UTF-8, mouse, DEL, large pastes | Agent test on real tmux: bytes sent through `TabTerminal` reach a raw-mode reader unchanged, including a 64 KB bracketed paste, UTF-8, SGR mouse reports, and ordering across 512-byte chunks. App test: SwiftTerm's input reaches the source as typed. |
| Typing feels slower through send-keys | **The gate (decision 12):** keystroke to echo drawn in SwiftTerm on dev-A, control mode against the attached client, at most +5 ms at p95 on a local workspace. At tmux it's within ±0.25 ms (the table above). The same run on the us-west-2 tester server once it exists. The dev-only switch stays until the gate passes. |
| Paste | A 64 KB bracketed paste through the view: the pane gets the same bytes in order, framed in `\e[200~`…`\e[201~`, in 512-byte `send-keys` chunks with nothing lost or reordered. |
| IME (Japanese, Chinese, accented input) | SwiftTerm's marked text (`setMarkedText` … `insertText`) sends only the committed text, once, as UTF-8. A test drives the NSTextInputClient calls and checks the source got exactly the committed string. |
| Mouse reporting | A program that turns on SGR mouse mode (`\e[?1006h`, `\e[?1000h`): the view's clicks and wheel reach the pane as SGR reports, byte for byte. With the mode off, clicks select text and nothing is sent. |
| Heavy output floods the app or the agent | `refresh-client -f pause-after=2` with `%pause`/`%continue` (tmux ≥ 3.2), and a bounded buffer per stream in the agent. Test: `yes \| head -c 200M` in one tab while typing in another. Memory stays under a fixed bound, the app stays responsive, and the final screen matches a fresh `reset`. |
| #73 and #75's no-loss guarantees (decision 11) | `portenv attach` keeps its tests unchanged. `TabTerminal` gets three: a program that prints and exits immediately has every byte on the stream before `TAB_CLOSED` (the control-mode version of `TestTerminalCountsWhatTheProgramLeftAtItsExit`); input sent while the tab closes is either delivered or reported with its count; nothing is lost or repeated across a reset. |
| A tab that's gone | Opening a closed tab's ID ends with `TAB_CLOSED` within 1 s, with no output. |
| The dev-only switch reaches a release build | `release-local.sh` and `check-app-identity.sh` refuse an app with the marker. The Makefile refuses `ATTACHED_TERMINAL=1` without `LANE`. Each check is mutation-checked. |
| A reset loses or repeats output, or replays a mark on the wrong line | Real tmux: open, print, reconnect mid-output. The reset plus the following output gives the same screen and history as a fresh capture. Replayed marks: 0 on the wrong line, with the number dropped reported. The `TmuxReplayTests` fixtures stay as the tracker's regression set. |
| Two views of one tab, or a CLI attach, at different sizes | One tab at two sizes: the latest wins, the other view gets `resized` and draws without garbling, and a full-screen program redraws at the new size. |
| The control client, the agent or portenvd restarts | Kill the control client, then the agent's channel, then portenvd. Each open stream ends with `lost(retrying: true)` and comes back with a `reset`. Programs in the tabs keep running. |
| An older tmux | Decision 10: the image pins `tmux=3.4-*` and `test.sh` checks it; the helper refuses a tmux older than 3.4 at attach, with the message in decision 10, and a test runs it with a fake `tmux -V`. |
| Sequences that used to stop at tmux reach the app | The sequence suite in decision 9, and a test that `allow-passthrough` is off in the agent's tmux options. |
| A window with more than one pane (a program ran `tmux split-window`) | The stream shows the window's active pane. The agent refuses `split-window` from the app's tabs as it does other tmux UI (§4.1). Test: a split window still streams one pane, and its tab shows it. |
| The CLI and the app see different things | The same commands in a tab, seen through `portenv attach` and through `TabTerminal`, give the same final screen text. |

## Order of work

1. **The control-mode milestone (1.2c):**
   - the agent's control client per session, with tab events replacing the polling;
   - `TabTerminal` in the agent and portenvd;
   - the app's control-mode provider;
   - `MarkedTerminalView` in the main window in place of `BoxTerminal`.

   It comes with the tests above, the latency gate (decision 12), and the reviewer's UI test on dev-A in light and dark: scrolling, resizing, tab switching, and reconnecting with the app quit and reopened. The dev-only switch is removed once the gate passes.
2. **Waiting-for-input detection,** from the same control client: ADR 0017's four-signal detector per tab, and its events to the app and the relay.
3. **Command blocks:**
   - blocks with attribution (decision 4);
   - the sticky header;
   - the inline answer card;
   - tab badges.

**Split view (1.2b, lane B) is built in parallel on the interface.** It works with `ScriptedTabSource` until step 1 lands, then switches to the live provider.

**Not before the reviewer rules:** the agent and proto work in steps 1–3. This PR only adds the ADR, PLAN's changes, the spike's tests, the interface, and the prototype view it plugs into.

## Consequences

- The app's terminal gets real scrollback per tab, so overlays, Find, block actions (copy output, jump to start) and split view work on the tab's actual history.
- The terminal pipeline that #73 and #75 hardened (the `Terminal` RPC) stays for the CLI. The app moves to the new one, so there are two paths to keep.
- tmux remains the only process in the box that holds the terminals. The agent talks to it as one more client, so a box keeps running through any of this.
- Escape-sequence filtering for the app's terminal moves from tmux to the app's per-tab view (decision 9).
