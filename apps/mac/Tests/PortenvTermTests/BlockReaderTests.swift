// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// Blocks read back from the real terminal (slice 0's option B, R-0174).
@MainActor
struct BlockReaderTests {
    static let esc = "\u{1b}"
    static func prompt(_ cmd: String) -> String { "\(esc)]133;A\u{07}\(esc)[1m$\(esc)[0m \(esc)]133;B\u{07}\(cmd)\r\n\(esc)]133;C\u{07}" }
    static func end(_ code: Int) -> String { "\(esc)]133;D;\(code)\u{07}" }

    func view(cols: Int = 80, rows: Int = 24) -> MarkedTerminalView {
        _ = NSApplication.shared
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        v.getTerminal().resize(cols: cols, rows: rows)
        return v
    }

    func text(_ s: AttributedString?) -> String { s.map { String($0.characters) } ?? "" }

    /// The fastest of `runs` runs of `work`: the time the work takes, with
    /// a shared runner's stalls left out (one wall-clock reading isn't).
    func best(of runs: Int = 5, _ work: () -> Void) -> Duration {
        (0 ..< runs).map { _ in ContinuousClock().measure(work) }.min()!
    }

    @Test func blocksAndTheirRowsComeBackFromTheBuffer() {
        let v = view()
        let session = Self.prompt("git pull") + "Already up to date.\r\n" + Self.end(0)
            + Self.prompt("npm test -- retries") + "\(Self.esc)[31m×\(Self.esc)[0m retries a declined payment once\r\nTests: 1 failed\r\n" + Self.end(1)
            + Self.prompt("claude --continue") + "● Ran the test 40 times\r\n"
        v.ingest(Array(session.utf8))
        let reader = BlockReader(view: v)
        let blocks = reader.blocks()
        #expect(blocks.map(\.command) == ["git pull", "npm test -- retries", "claude --continue"])
        #expect(blocks.map(\.state) == [.succeeded, .failed(exit: 1), .running])
        let failed = blocks[1]
        let rows = failed.output.map { Array($0) } ?? []
        #expect(rows.map { text(reader.row($0, palette: .dark)) } == ["× retries a declined payment once", "Tests: 1 failed"])
        // The red × keeps its colour from the app's palette.
        let first = reader.row(rows[0], palette: .dark)!
        #expect(first.runs.first?.foregroundColor != nil)
        // Live marks carry their time; the running block has a start.
        #expect(blocks[2].startedAt != nil)
    }

    /// A seed's replayed marks get no times: the app never saw them arrive.
    @Test func seededBlocksHaveNoTimes() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: Array((Self.prompt("ls") + "a b\r\n" + Self.end(0)).utf8)))
        for _ in 0 ..< 200 where BlockReader(view: v).blocks().isEmpty { try? await Task.sleep(for: .milliseconds(10)) }
        let blocks = BlockReader(view: v).blocks()
        #expect(blocks.count == 1)
        #expect(blocks.first?.startedAt == nil && blocks.first?.endedAt == nil)
    }

    /// R-0174 item 2: a card shows only what the filter let through, as the
    /// terminal drew it. Output with planted OSC (title, clipboard, link,
    /// a private one), DCS and APC sequences reads back as its plain text,
    /// and nothing new reaches the view: no title, no clipboard write, no
    /// input sent to the tab.
    @Test func plantedSequencesReadBackAsPlainText() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        let e = Self.esc
        let planted = "a\(e)]0;evil title\u{07}b\(e)]52;c;ZXZpbA==\u{07}c\(e)]8;;file:///etc/hosts\u{07}d\(e)]8;;\u{07}"
            + "\(e)]1337;File=x\u{07}e\(e)Pq#0;2;0;0;0\(e)\\f\(e)_kitty\(e)\\g"
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: []))
        s.play(.output(Array((Self.prompt("cat notes") + planted + "\r\n" + Self.end(0)).utf8)))
        let reader = BlockReader(view: v)
        for _ in 0 ..< 200 where reader.blocks().isEmpty { try? await Task.sleep(for: .milliseconds(10)) }
        let block = reader.blocks()[0]
        let rows = block.output.map { Array($0) } ?? []
        let shown = rows.map { text(reader.row($0, palette: .light)) }.joined(separator: "\n")
        #expect(shown == "abcdefg")
        #expect(!shown.contains(e) && !shown.contains("evil") && !shown.contains("file:"))
        // Nothing reached the tab, and no link is in the card's text.
        #expect(s.sent.isEmpty)
        let attributed = reader.row(rows[0], palette: .light)!
        #expect(attributed.runs.allSatisfy { $0.link == nil })
    }

    /// R-0174 item 3: reading back a 10,000-line block, and resizing with a
    /// full scrollback, measured on the main thread. A card reads only the
    /// rows it shows (a folded block one, an open long one 41), so the
    /// whole-block read is what Copy Output and Show All Output cost.
    @Test func readingAndResizingAFullScrollbackIsTimed() {
        let v = view(cols: 120, rows: 40)
        var out = Self.prompt("npm run build")
        for i in 1 ... 10_000 { out += "\(Self.esc)[32m✓\(Self.esc)[0m transforming (\(i)) src/module-\(i).ts\r\n" }
        out += Self.end(0) + Self.prompt("ls")
        v.ingest(Array(out.utf8))
        let reader = BlockReader(view: v)
        let clock = ContinuousClock()
        var blocks: [CommandBlock] = []
        let listing = best { blocks = reader.blocks() }
        let rows = blocks[0].output!
        var chars = 0
        let readAll = clock.measure { for r in rows { chars += reader.row(r, palette: .dark)?.characters.count ?? 0 } }
        // Each run with a new reader, so none of them reads from the cache.
        let readShown = best {
            let fresh = BlockReader(view: v)
            for r in Array(rows.prefix(20)) + Array(rows.suffix(20)) { _ = fresh.row(r, palette: .dark) }
        }
        // Then the shown rows again from a reader that has read them: cached.
        for r in Array(rows.prefix(20)) + Array(rows.suffix(20)) { _ = reader.row(r, palette: .dark) }
        let rereadShown = clock.measure {
            for r in Array(rows.prefix(20)) + Array(rows.suffix(20)) { _ = reader.row(r, palette: .dark) }
        }
        // SwiftTerm's own reflow, which the attached terminal pays today
        // too, and then what blocks add: finding the marks again.
        let reflow = clock.measure { v.getTerminal().resize(cols: 80, rows: 40) }
        var after: [CommandBlock] = []
        let relist = best { after = reader.blocks() }
        print("BLOCK-TIMES list=\(listing) readAll(\(rows.count) rows)=\(readAll) readShown(40)=\(readShown) rereadShown(cached)=\(rereadShown) reflow(SwiftTerm)=\(reflow) relist=\(relist)")
        #expect(after.count == 2)
        #expect(chars > 0)
        // What a card does on the main thread stays under a frame: the
        // fastest of five runs, which these clear many times over (about
        // 2 ms, 0.4 ms and 0.1 ms on an M-series Mac).
        #expect(readShown < .milliseconds(16))
        #expect(listing < .milliseconds(16))
        #expect(relist < .milliseconds(16))
    }

    /// Find over a 10,000-line block reads plain text from the buffer:
    /// timed, and it finds rows a folded block hides.
    @Test func findOverAFullScrollbackIsTimed() {
        let v = view(cols: 120, rows: 40)
        var out = Self.prompt("npm run build")
        for i in 1 ... 10_000 { out += "transforming (\(i)) src/module-\(i).ts\r\n" }
        out += Self.end(0) + Self.prompt("ls")
        v.ingest(Array(out.utf8))
        let reader = BlockReader(view: v)
        let blocks = reader.blocks()
        var found: [BlockFind.Match] = []
        let took = ContinuousClock().measure { found = BlockFind.matches("module-5000.ts", in: blocks) { reader.text($0) } }
        // The app searches in chunks with a yield between them: one chunk
        // is what holds the main thread at a time.
        let rows = blocks[0].output!
        func search(_ n: Int) -> Duration {
            best {
                _ = BlockFind.matches("module-5000.ts", block: blocks[0].id,
                                      rows: rows.lowerBound ... rows.lowerBound + n - 1) { reader.text($0) }
            }
        }
        let chunk = search(BlockFind.chunkRows)
        let double = search(2 * BlockFind.chunkRows)
        let whole = best(of: 3) { _ = BlockFind.matches("module-5000.ts", in: blocks) { reader.text($0) } }
        print("BLOCK-TIMES find(10000 rows)=\(took) best=\(whole) findChunk(\(BlockFind.chunkRows) rows)=\(chunk) double=\(double)")
        #expect(found.count == 1)
        #expect(found.first?.block == blocks.first?.id)
        // What chunking is for: one chunk holds the main thread for a small
        // part of the whole search, and twice the rows cost about twice as
        // much (linear, never worse), however fast the Mac is.
        #expect(chunk * 10 < whole, "chunk \(chunk), whole \(whole)")
        #expect(double < chunk * 3, "chunk \(chunk), double \(double)")
        // And a chunk's fastest run fits in a few frames even on a slow
        // shared runner (about 5 ms on an M-series Mac).
        #expect(chunk < .milliseconds(50))
    }
}

