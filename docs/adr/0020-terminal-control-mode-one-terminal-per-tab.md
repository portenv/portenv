# 0020. The app's terminal uses tmux control mode, one terminal per tab

Date: 2026-10-13 · Status: accepted with the changes R-0133 asked for (the reviewer, on the owner's delegation, R-0132 ruling 1, R-0133, R-0134), which are in this text. The per-tab interface is agreed with lane B for split view. 1.2c follows this text.

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

   - **Reading control mode (R-0133 item 8):** `parseControlLine` and `decodeControlOutput` (`core/agent/controlmode.go`, from the spike) read every line.
     - **`%output %P <data>`:** the data is decoded from control mode's escaping (bytes below 32 and the backslash as `\ooo`, three octal digits).
     - **`%extended-output %P <age> … : <data>`:** with `pause-after` set, tmux writes this form instead. The decoder takes the data after ` : ` the same way.
     - **Raw bytes, not text:** each line's data is a byte string. A UTF-8 character or an escape sequence can be split across two `%output` lines, and the decoder never joins, trims or re-encodes. The byte stream goes on as it came, and SwiftTerm and the helper's parser keep their state across chunks.
     - **The tests:**
       - a multi-byte character split across two `%output` lines;
       - an escape split across two lines;
       - every octal escape from 000 to 037 and `\134`;
       - `%extended-output` with and without an age field.
   - **Copy mode can't swallow input.** A program in the workspace can put a pane into a tmux mode (`tmux copy-mode`). The pane's keys would then go to the mode instead of the program.
     - **Before sending input,** the helper checks `#{pane_in_mode}` and leaves any mode (`copy-mode -q`) in the same command list as the `send-keys -H`.
     - **The test:** a program enters copy mode, and the next bytes sent still reach the program.
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
     string author = 4;        // set by portenvd from the caller's identity, never by the client (decision 4)
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
   - **Only the person's input is sent (R-0133 item 1).** That's what they typed, pasted, clicked or entered through IME: SwiftTerm's `send(data:)`.
     - **The emulator's own replies never are.** These are replies to DA, DSR, DECRQSS, DECRQM, XTWINOPS, XTGETTCAP, XTVERSION, ENQ, and OSC 4, 10, 11 and 52 queries, which reach the view through `send(source: Terminal, data:)`.
     - **`MarkedTerminalView` overrides that path to drop them.** tmux emulates each pane and answers its program's queries itself. A second reply from the app would be extra input, typed as the person, which a program could use to make the terminal type for it.
     - **The test (in this PR):** each of those queries yields zero bytes to the source, and typed text still goes. Mutation-checked: without the override, SwiftTerm sent all of them.
   - **Paste is cleaned before it's framed (R-0133 item 3).**
     - **What's removed:** `ESC[201~` (the bracketed-paste end marker, which would end the paste early and run the rest as typed commands), and every C0 control except tab, CR and LF.
     - **Then** the text is wrapped in `ESC[200~`…`ESC[201~` when the program has turned bracketed paste on.
     - **The test:** a paste holding `ESC[201~rm -rf ~\r` and other controls reaches the pane as one bracketed paste of plain text.
   - **Attribution (R-0133 item 4):** the agent records which stream sent each Enter, for "author is whoever sent the Enter" (GUIDELINES §4.2).
     - **The author is set by portenvd** from the caller's identity: the app over its socket, or an agent's grant (ADR 0016). It's never set by the client.
     - **The proto:** the app's `TabTerminalOpen` to portenvd has no author field; portenvd fills `author` in the request it sends the agent.
     - **When:** this is in place before any agent can call `TabTerminal`.
     - **The test:** an agent's call that claims `author: "app"` is attributed to the agent.
     - Today, input through an attached client can't be told apart.
5. **Size.** A view's `resize` becomes `refresh-client -C @N:<cols>x<rows>` for that tab's window, with `window-size latest`, as today.
   - **In split view, a tab shows in at most one pane.** So two app views size one tab only through another viewer: a CLI attach or an agent.
   - **One tab has one size.** The latest view to resize a tab sets it, whether that's the app, another pane showing the same tab, or a CLI attach.
   - **The other views get `resized` and draw that size.**
     - **Smaller than the view:** the terminal's own background fills the rest.
     - **Larger than the view:** the view keeps the cursor's row in sight, rather than clipping to the top-left corner.
   - **On a `reset` at a size other than its own,** the view asks for its own size at once. The latest size asked for wins, so an app view that reconnects takes the tab back.
   - **The test (R-0133 item 9):** a CLI client and an app view of one tab take turns resizing, ten times. After each turn, the tab has the latest size, the other side gets `resized` or SIGWINCH, and a full-screen program in the tab redraws without garbling.
6. **Reset, and reconnecting.** Every stream starts with `reset`, built on the session's control client in this order: capture, then live output.
   - **The seed is one command list (R-0133 item 5).** The capture of the main screen, the capture of the alternate screen and the state read go in one control-mode command line, joined by `;`. tmux runs a command list without reading pane output in between, so no `%output` lands inside the seed. Its reply comes back in order with `%output`, so nothing is lost or repeated between the seed and what follows.
   - **What the seed holds:**
     - `capture-pane -p -e -S - -E -` of the main screen (history and visible lines);
     - when `#{alternate_on}`, `\e[?1049h` and `capture-pane -a -p -e` of the alternate screen;
     - **the state:**
       - the scroll region (`#{scroll_region_upper}`/`lower`), origin mode (`#{origin_flag}`), autowrap (`#{wrap_flag}`), insert mode;
       - cursor keys and keypad (`#{keypad_cursor_flag}`, `#{keypad_flag}`), mouse modes (`#{mouse_*_flag}`);
       - the cursor position and visibility;
       - bracketed paste, which the helper's parser tracks (tmux 3.4 has no format for it);
       - the current SGR: the pen the program left set, from the helper's parser, so the next output draws in the right colours.

       Each is written as the escape sequence that sets it, after the captured text.
   - **The view resets its parser too.** A `reset` starts with SwiftTerm's full reset, so a stream that ended mid-escape or mid-UTF-8 leaves nothing half-parsed.
     - **The tests:** a reset that lands mid-escape and one that lands mid-UTF-8 both draw the seed correctly.
     - **And on the seeding side:** a seed taken while the program has a scroll region, origin mode, autowrap off and a colour set gives the same screen as a fresh attach.
   - **Mark replay (R-0133 item 6):** the helper works out each mark's line from **its own parse of `%output`**, not from `history_size`/`cursor_y` read afterwards, which can race with more output.
     - **The parser:** the helper runs a terminal-state parser per pane on the same bytes. It tracks the cursor, scrolling, the scroll region, the alternate screen and the pen.
       - **The library:** `github.com/charmbracelet/x/vt` v0.1.0 (MIT). Approved by the reviewer, on the owner's delegation, in R-0142, on the terms below.
       - **The notices check:** #77's tool passes on a probe binary built with it. That's 17 modules, all MIT or BSD-3-Clause, with nothing copyleft and nothing needing the allowlist.
       - **Pinned exactly:** `go.sum` is checked in CI, and the notices file is regenerated by #77's tool in the PR that adds it.
       - **Untrusted input:** it reads bytes a workspace program wrote, so it's treated as untrusted.
         - **Fuzzing:** a Go fuzz target over its `Write`, seeded from `tests/spike/q3/fixtures`. It runs in CI on a short budget.
         - **Panics:** each pane's parser runs under `recover`. A panic resets that pane's parser, logs once, and never takes the helper down. The pane's marks then start again from its next prompt, as after a pause (decision 11).
         - **Scrollback:** a hard cap in lines and in bytes.
       - **It only reads.** Nothing it parses becomes a reply to the pane, and the app's default-deny filter stays the boundary (decision 9).
       - **Upgrades:** each one is its own PR, with the notices diff and a fuzz run, and never a range bump.
     - **How lines are counted:** each mark gets an absolute line number (lines scrolled into history plus the cursor row) when its bytes are parsed.
     - **Replaying:** in the seed, the mark's escape sequence goes at the start of that line. The line number is mapped to the capture through tmux's `history_size` and `history_limit`, read in the same command list.
     - **What's dropped:** a mark whose line has left the history (trimmed) or was cleared.
     - **The rule:** a replayed mark is never on the wrong line, and a missing one costs only a block header in old history.
     - **The test:** identical prompts (`$ ` twenty times, same text, which a text match can't tell apart). Every replayed mark lands on its own line.
   - **The app's side:** after a reconnect (portenvd or the agent restarting, a move, the Mac waking), the app opens the stream again and draws the new `reset`. The tab's history is the box's, so nothing depends on what the app had kept.
7. **The app's per-tab terminal interface** (`apps/mac/Sources/PortenvKit/TabTerminal.swift`, in this PR, so split view can build on it now). See "The interface" below.
8. **The CLI stays a plain attach.** `portenv attach` and `portenv open --tab` keep the `Terminal` RPC and #41's per-viewer grouped sessions, for a person in Terminal, Ghostty or a tiling terminal; their shells get the same OSC 133 marks directly. Agents' terminal access (2.4) is unchanged.
9. **Security: the app's terminal treats every byte from the workspace as untrusted** (R-0133 items 1–3, R-0134).
   - **What changes:** control mode carries the tab's bytes as the program wrote them. So the app's SwiftTerm receives escape sequences that tmux used to interpret or drop before they reached an attached client.
   - **The boundary is in the app.** tmux options are a second line only: a program in the workspace runs as the same user as tmux and can change them. That's how #94 found the live OSC 52 gap on today's path.
   - **A default-deny filter sits in front of `feed`.** `MarkedTerminalView.ingest` passes bytes through a small tokenizer before SwiftTerm sees them:
     - **Printable text** passes, as UTF-8, with partial characters held across chunks.
     - **C0:** BS, HT, LF, VT, FF, CR, BEL (not sounded; it shows the tab's badge), SO and SI pass. ENQ and the rest are dropped.
     - **CSI** passes only for an allowlist of final bytes:
       - cursor movement, erase, insert and delete, scroll, SGR, DECSTBM, tab stops, and mode set and reset;
       - DSR, DA, DECRQM and XTVERSION pass but get no reply (item 1);
       - **XTWINOPS (`CSI … t`) is dropped:** it can move, resize and iconify windows, and report titles.
     - **Private modes (DECSET/DECRST) are an allowlist:**
       - 1 (cursor keys), 6 (origin), 7 (autowrap), 12 and 25 (cursor);
       - 47, 1047, 1048 and 1049 (alternate screen);
       - 1000, 1002, 1003 and 1006 (mouse, SGR encoding only);
       - 1004 (focus), 2004 (bracketed paste), 2026 (synchronized output).

       Every other mode is dropped.
     - **ESC sequences:** the allowlist is DECSC/DECRC, IND/NEL/RI, charset designation, DECKPAM/DECKPNM and RIS (below).
     - **OSC:** only 133 (the tracker) and 8 pass.
       - **OSC 8 is shown as a link,** and opened only as R-0134 says: http or https only, on the person's own click, after the full URL shows as a tooltip and in the state line.
       - **Everything else is dropped:** 0, 1 and 2 (titles; the app owns tab names), 7, 52 (the clipboard), 1337 (iTerm2's File= and SetUserVar), 4, 10 and 11, and any other number.
     - **DCS, APC, PM and SOS strings are dropped whole,** sixel and kitty graphics included.
     - **Limits:**
       - one sequence is at most 4 KB (an OSC 8 URL at most 2 KB), after which the sequence is dropped and parsing resumes at the next byte that can start one;
       - scrollback is 10,000 lines per tab;
       - the parser's held partial input is at most 4 KB.
     - **RIS (`ESC c`)** clears the screen and resets modes and the pen, but **keeps the scrollback and its marks.** It becomes "erase screen, home, default modes", not SwiftTerm's full reset, so a program can't wipe the blocks above it.
   - **Fuzzed:** random and structured streams go through the filter and SwiftTerm, built from the spike's control-mode fixtures, mutated, and split at every byte. The invariants:
     - no crash;
     - zero bytes reach the source;
     - no clipboard, title or link callback;
     - memory stays under the limits;
     - marks stay on lines.

     It runs in CI on a fixed seed and nightly on random ones.
   - **No reply reaches the pane** (decision 4, item 1), and **paste is cleaned** (decision 4, item 3).
   - **One factory (R-0134):** every terminal view the app creates comes from `TerminalViews`, which installs `TerminalGuard` today and this filter and the reply drop for `MarkedTerminalView`. `scripts/check-terminal-views.sh`, in lint, fails on a terminal view constructed anywhere else.
   - **The second line:** tmux's `allow-passthrough off` and `set-clipboard off`.
     - **It isn't complete.** On tmux 3.4, `tmux set-buffer -w` still reaches a client as OSC 52 with `set-clipboard off`; copy mode's copies don't. Checked in the image (#94). With control mode, that OSC 52 is in the pane's own output, and the filter drops it.
     - The helper sets them on every new session and attach (#94), and again on every `TabTerminal` open.
     - A test asserts both.
     - Turning either on is the owner's decision.
   - **The ruling (R-0133):** with items 1–3 in place and tested, the second-line options asserted and the fixed memory limits, this is the same guarantee as today or stronger. It's not a security-default change for the owner. If any of it is dropped, it goes to the owner.

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
    - **When output can't keep up (R-0133 item 7), the guarantee is: every byte, or a `reset` that replaces it, and the person is told.**
      - **A pause:** with `pause-after`, tmux skips a paused pane's output for that client rather than queuing it. When the helper's client falls that far behind, tmux sends `%pause`; the helper continues the pane, and every open stream of that tab gets a fresh `reset`.
      - **The helper's own buffer:** each stream's buffer is bounded at 4 MB. A stream whose reader (portenvd, the app) is that far behind is dropped to a `reset` too, and only that stream.
      - **The person is told.** The view shows "Output was too fast to show in full. Showing the current screen." on the row where the reset lands. The block that was running is marked as skipped, never shown as complete.
      - **The waiting detector and marks during a pause:** marks in skipped output are lost, and the helper's parser starts again from the reset's state.
        - The tab's OSC 133 state becomes "unknown" until its next prompt mark.
        - ADR 0017's combined verdict already handles "unknown" through the other three signals (blocked on the terminal, quiet, screen asks), so waiting-for-input detection keeps working.
        - Blocks for the skipped stretch are shown as one skipped block.
        - The helper reads its control client promptly whatever the app does, so a pause means the helper itself fell behind, not the app.
    - Each guarantee gets a test written before the code (see Risks): a pause, and a buffer overflow, each end in a reset with the notice and no silent gap.
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
- **What goes back to the source:** the person's input and the size, through SwiftTerm's delegate. The emulator's replies never do (decision 4, item 1). The view ignores a program's clipboard, link and title requests, and the filter in front of `feed` drops them first (decision 9).
- **How it's made:** only through the factory (R-0134, R-0140): `TerminalViews.tab()` in PortenvTerm, which also makes today's attached terminal (`attached()`). `check-terminal-views.sh` has that one file as its only exception, and its planted-case test fails a direct construction in PortenvTerm, PortenvKit and the app. In 1.2c, the view gets the default-deny filter in front of its feed.
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
| The emulator's replies reach the pane (item 1) | In this PR: twenty queries (DA1–3, DSR, CPR, DECXCPR, DECRQSS, DECRQM, XTWINOPS, XTVERSION, XTGETTCAP, OSC 4/10/11/52, DECREQTPARM, ENQ) yield zero bytes to the source; typed text still goes. Mutation-checked. |
| A sequence a program prints gets past the filter (item 2) | A table test per rule (each allowed sequence passes, each other one is dropped, the limits hold), and the fuzzer in decision 9. A test that every `MarkedTerminalView` in the app comes from the factory (`check-terminal-views.sh`). |
| Paste ends early and runs the rest (item 3) | A paste holding `ESC[201~`, NUL, ESC and other C0 controls reaches the pane as one bracketed paste of plain text, with tab, CR and LF kept. |
| An agent claims to be the person (item 4) | An agent's `TabTerminal` call with `author: "app"` is attributed to the agent; the app's own call has no author field. |
| A reset lands mid-escape or mid-UTF-8, or misses the scroll region or pen (item 5) | A reset after half an escape and after half a character draws the seed correctly; a seed with a scroll region, origin mode, autowrap off and an SGR colour set matches a fresh attach. |
| Identical prompts confuse mark replay (item 6) | Twenty identical prompts: every replayed mark is on its own line. |
| A pause or an overflow loses output silently (item 7) | Each ends in a `reset` with the notice; the skipped block is never shown complete; the waiting detector still reports a question asked during the pause. |
| UTF-8 split across `%output` lines, `%extended-output`, copy mode (item 8) | A character and an escape split across two lines, every octal escape, `%extended-output` with and without an age; a program puts its pane in copy mode and the next input still reaches it. |
| A CLI client and the app resize one tab in turn (item 9) | Ten alternating resizes: the latest wins each time, the other side follows, a full-screen program redraws cleanly. |
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
| The helper's parser panics, loops or grows on hostile output | Its fuzz target (decision 6) in CI on a short budget, seeded from the spike's fixtures. A test that a panic in one pane's parser resets only that pane, logs once and leaves the helper running. A test that scrollback stays under its line and byte caps under `yes`. |
| An older tmux | Decision 10: the image pins `tmux=3.4-*` and `test.sh` checks it; the helper refuses a tmux older than 3.4 at attach, with the message in decision 10, and a test runs it with a fake `tmux -V`. |
| A terminal view without the guard (R-0135) | On the tmux we ship (3.4), the options can't stop `tmux set-buffer -w`: it reaches the client as OSC 52 with `set-clipboard off` (#94). So any terminal view made without the guard and filter is a live clipboard leak. That's why every view comes from the factory, and `check-terminal-views.sh` fails the build on one made anywhere else; its planted-case test keeps the check honest. |
| Sequences that used to stop at tmux reach the app | The filter's table test and fuzzer (decision 9); a test that `allow-passthrough` and `set-clipboard` are off in the helper's tmux options, re-asserted on each `TabTerminal` open. |
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
