# 0020. The app's terminal uses tmux control mode, one terminal per tab

Date: 2026-10-13 · Status: proposed (lane A, for the reviewer's ruling; follows spike question 3, ADR 0017)

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

## Decision

1. **The app's terminal shows each tab through its own stream of that tab's output.** It's read from tmux control mode, one SwiftTerm terminal per tab, and never through an attached tmux client.
   - Tabs stay tmux windows with the same IDs (#41). The tab bar, the per-tab programs, and the app owning tab names don't change.
2. **The box agent runs one control-mode client per tmux session** (`tmux -C attach-session -t <session>`, `LANG=C.UTF-8`, attached when the session starts). It serves:
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
4. **Input** goes to the tab's pane as `send-keys -t %P -H <hex>…` on the session's control client, in chunks of at most 512 bytes. Commands on one control client run in order, so the bytes arrive in the order typed.
   - The agent records which stream sent each Enter, for "author is whoever sent the Enter" (GUIDELINES §4.2). Today, input through an attached client can't be told apart.
5. **Size.** A view's `resize` becomes `refresh-client -C @N:<cols>x<rows>` for that tab's window, with `window-size latest`, as today.
   - **One tab has one size.** The latest view to resize a tab sets it, whether that's the app, another pane showing the same tab, or a CLI attach.
   - The other views get `resized` and draw that size: clipped if larger, with the terminal's own background around it if smaller. That's the rule split view uses when two panes show one tab.
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
9. **Security: tmux's passthrough stays off,** in the product, the image and the agent's tmux options. A test checks it, and turning it on is the owner's decision.
   - **What changes:** control mode carries the tab's bytes as the program wrote them. So the app's SwiftTerm now receives escape sequences that tmux used to interpret or drop before they reached an attached client.
   - **Whose job filtering becomes:** the app's terminal treats every byte from the box as untrusted. The per-tab view's delegate:
     - doesn't read or set the Mac's clipboard (OSC 52 read returns nothing, writes are ignored);
     - doesn't open links on a program's request;
     - ignores titles (the app owns tab names);
     - ignores iTerm2's inline-file sequences.
   - **The test:** a sequence suite (OSC 52 set and query, OSC 8, OSC 0/2, OSC 1337 File=, DCS and APC strings, DECRQSS) shows none of them reaches the clipboard, opens anything or renames a tab.
   - **The reviewer and the owner rule** on whether this counts as a change to a security guarantee. The intent is the same guarantee as today, enforced in the app instead of in tmux.

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

**The view:** `MarkedTerminalView` (PortenvTerm), the prototype that becomes the real per-tab view.
- `attach(_ source:)` shows a tab, closing the previous stream; `detach()` stops showing it.
- `onEnded` reports why a stream ended.
- `tracker` holds the marks, and `overlay` is the layer blocks draw on.
- Input and size go back to the source through SwiftTerm's delegate.

**Rules split view builds on:**
- A pane holds one `MarkedTerminalView`. Choosing another tab in the pane means `attach(provider.open(tab:size:))`.
- Closing a pane means `detach()`; the tab keeps running.
- Two panes may show the same tab: each opens its own source, and the size rule above applies.
- Until the control-mode provider lands (order of work, step 1), panes use `ScriptedTabSource` for renders and tests. The live provider plugs in without changing the panes.

## Risks, and how each is tested

| Risk | Test (written before the code) |
| --- | --- |
| Input loses or changes bytes: paste, IME, UTF-8, mouse, DEL, large pastes | Agent test on real tmux: bytes sent through `TabTerminal` reach a raw-mode reader unchanged, including a 64 KB bracketed paste, UTF-8, SGR mouse reports, and ordering across 512-byte chunks. App test: SwiftTerm's input reaches the source as typed. |
| Typing feels slower through send-keys | Keystroke echo time, control mode against today's attach, on this Mac (Docker) and over the SSH forward to a server box. Budget: p95 under 30 ms locally, and the network round trip plus 20 ms on a server. The numbers go in the milestone note. |
| Heavy output floods the app or the agent | `refresh-client -f pause-after=2` with `%pause`/`%continue` (tmux ≥ 3.2), and a bounded buffer per stream in the agent. Test: `yes \| head -c 200M` in one tab while typing in another. Memory stays under a fixed bound, the app stays responsive, and the final screen matches a fresh `reset`. |
| A reset loses or repeats output, or replays a mark on the wrong line | Real tmux: open, print, reconnect mid-output. The reset plus the following output gives the same screen and history as a fresh capture. Replayed marks: 0 on the wrong line, with the number dropped reported. The `TmuxReplayTests` fixtures stay as the tracker's regression set. |
| Two views of one tab, or a CLI attach, at different sizes | One tab at two sizes: the latest wins, the other view gets `resized` and draws without garbling, and a full-screen program redraws at the new size. |
| The control client, the agent or portenvd restarts | Kill the control client, then the agent's channel, then portenvd. Each open stream ends with `lost(retrying: true)` and comes back with a `reset`. Programs in the tabs keep running. |
| An older tmux | The agent checks `tmux -V` ≥ 3.2 (per-window sizes, pause-after) at start and refuses with a clear error. The image pins tmux 3.4, and `images/toolbox-node/test.sh` checks the version. |
| Sequences that used to stop at tmux reach the app | The sequence suite in decision 9, and a test that `allow-passthrough` is off in the agent's tmux options. |
| A window with more than one pane (a program ran `tmux split-window`) | The stream shows the window's active pane. The agent refuses `split-window` from the app's tabs as it does other tmux UI (§4.1). Test: a split window still streams one pane, and its tab shows it. |
| The CLI and the app see different things | The same commands in a tab, seen through `portenv attach` and through `TabTerminal`, give the same final screen text. |

## Order of work

1. **The control-mode milestone (1.2c):**
   - the agent's control client per session, with tab events replacing the polling;
   - `TabTerminal` in the agent and portenvd;
   - the app's control-mode provider;
   - `MarkedTerminalView` in the main window in place of `BoxTerminal`.

   It comes with the tests above and the reviewer's UI test on dev-A in light and dark: scrolling, resizing, tab switching, and reconnecting with the app quit and reopened.
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
