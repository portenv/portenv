// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
import PortenvTerm
import SwiftUI

/// Command blocks for `--render-screens` (R-0139, R-0174): the board's
/// session played through the real tab view with real OSC 133 marks, its
/// blocks read back as cards. Folded by default, and with every block open
/// (the long build then shows its first and last 20 lines).
@MainActor
enum BlockRenders {
    static let esc = "\u{1b}"
    static func prompt(_ cmd: String) -> String { "\(esc)]133;A\u{07}\(esc)[1m$\(esc)[0m \(esc)]133;B\u{07}\(cmd)\r\n\(esc)]133;C\u{07}" }
    static func end(_ code: Int) -> String { "\(esc)]133;D;\(code)\u{07}" }

    /// The board's left panel, timed as on the board (14:20 to 14:37).
    static func session(into view: MarkedTerminalView, day: Date) {
        func at(_ h: Int, _ m: Int, _ s: Double = 0) -> Date { day.addingTimeInterval(Double(h * 3600 + m * 60) + s) }
        func feed(_ s: String, _ time: Date) {
            view.tracker.now = { time }
            view.ingest(Array(s.utf8))
        }
        feed(prompt("git pull"), at(14, 19, 59.2))
        feed("Already up to date.\r\n" + end(0), at(14, 20))
        feed(prompt("npm test -- retries"), at(14, 30, 51.6))
        feed("  \(esc)[31m×\(esc)[0m retries a declined payment once (412 ms)\r\n      Expected attempts: 2   Received: 3\r\n\r\n"
            + "Tests: \(esc)[31m1 failed\(esc)[0m, \(esc)[32m23 passed\(esc)[0m, 24 total\r\n" + end(1), at(14, 31))
        var build = "vite v6.0.3 building for production\u{2026}\r\n"
        for i in 1 ... 2398 { build += "transforming (\(i)) src/module-\(i).ts\r\n" }
        build += "\(esc)[32m✓\(esc)[0m built in 40.71s\r\n"
        feed(prompt("npm run build"), at(14, 32, 19))
        feed(build + end(0), at(14, 33))
        feed(prompt("claude --continue"), at(14, 37))
        feed("● Ran the test 40 times: 8 failures, all on the second retry.\r\n", at(14, 37, 5))
    }

    static func render(to dir: URL, snapshot: (AnyView, String, NSSize) throws -> Void) throws {
        let day = Calendar.current.startOfDay(for: Date())
        let view = TerminalViews.tab()
        view.font = NSFont.monospacedSystemFont(ofSize: 12, weight: .regular)
        view.frame = NSRect(x: 0, y: 0, width: 980, height: 600)
        view.getTerminal().resize(cols: 110, rows: 30)
        session(into: view, day: day)
        let reader = BlockReader(view: view)
        let blocks = reader.blocks()
        let now = day.addingTimeInterval(14 * 3600 + 40 * 60)
        for (name, palette) in [("light", TerminalPalette.light), ("dark", TerminalPalette.dark)] {
            _ = name
            func cards(_ toggled: Set<Int>, showAll: Set<Int> = [], selected: Int? = nil, query: String? = nil, startAt: Int? = nil) -> AnyView {
                AnyView(BlocksView(blocks: blocks, reader: reader, palette: palette, now: now, toggled: toggled, showAll: showAll,
                                   selected: selected, query: query, startAt: startAt)
                    .frame(maxHeight: .infinity, alignment: .top)
                    .background(SwiftUI.Color(nsColor: .textBackgroundColor)))
            }
            // Folded by default: the successes show their headers only.
            try snapshot(cards([]), "blocks-default-\(palette.name)", NSSize(width: 980, height: 360))
            // Every success opened: the long build keeps 20 lines at each end.
            let successes = Set(blocks.filter { $0.state == .succeeded }.map(\.id))
            try snapshot(cards(successes), "blocks-expanded-\(palette.name)", NSSize(width: 980, height: 1380))
            // Slice 2 (R-0207): the npm test block selected (⌘↑/⌘↓), and Find
            // with "retr" found in a command and the output.
            try snapshot(cards([], selected: blocks.first { if case .failed = $0.state { return true }; return false }?.id),
                         "blocks-selected-\(palette.name)", NSSize(width: 980, height: 360))
            try snapshot(cards([], query: "retr"), "blocks-find-\(palette.name)", NSSize(width: 980, height: 400))
            // Scrolled into the long build with all its output: its header
            // stays at the top, with "↑ Top of block".
            if let build = blocks.first(where: { $0.outputLines > 100 }), let rows = build.output {
                try snapshot(cards([build.id], showAll: [build.id], startAt: rows.lowerBound + 600),
                             "blocks-sticky-\(palette.name)", NSSize(width: 980, height: 360))
            }
        }
    }
}
