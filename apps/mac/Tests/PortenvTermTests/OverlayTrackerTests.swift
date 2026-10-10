// SPDX-License-Identifier: Apache-2.0

import Foundation
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// A headless SwiftTerm terminal: the same buffer, scrollback, trimming and
/// reflow as the app's terminal view, without a window.
private final class Headless: TerminalDelegate {
    func send(source _: Terminal, data _: ArraySlice<UInt8>) {}
}

private func makeTerminal(cols: Int = 80, rows: Int = 24, scrollback: Int = 1000) -> (Terminal, OverlayTracker, Headless) {
    let d = Headless()
    let t = Terminal(delegate: d, options: TerminalOptions(cols: cols, rows: rows, scrollback: scrollback))
    return (t, OverlayTracker(terminal: t), d)
}

/// A prompt as shell integration draws it: A, the prompt text, B.
private func prompt(_ t: Terminal, _ text: String) {
    t.feed(text: "\u{1b}]133;A\u{07}\(text) $ \u{1b}]133;B\u{07}")
}

/// The text on the screen row a placed mark is on.
private func textAt(_ t: Terminal, screenRow: Int) -> String {
    t.getLine(row: screenRow)?.translateToString(trimRight: true) ?? ""
}

struct OverlayTrackerTests {
    @Test func aMarkSitsOnItsPromptLine() {
        let (t, o, _) = makeTerminal()
        t.feed(text: "hello\r\n")
        prompt(t, "first")
        let placed = o.placed()
        #expect(placed.count == 1)
        #expect(textAt(t, screenRow: placed[0].screenRow).hasPrefix("first $"))
    }

    /// Fast output: 5,000 lines through a 1,000-line scrollback, with a
    /// prompt every 500 lines. Every mark still in the buffer stays on its
    /// own prompt's line, whatever the view is scrolled to.
    @Test func marksStayOnTheirLinesThroughFastOutputAndScrolling() {
        let (t, o, _) = makeTerminal()
        for i in 0 ..< 10 {
            prompt(t, "prompt-\(i)")
            t.feed(text: "\r\n")
            for n in 0 ..< 500 { t.feed(text: "y \(i).\(n)\r\n") }
        }
        prompt(t, "prompt-last")
        // The view at the bottom, then scrolled up through the scrollback.
        var seen = Set<String>()
        for yDisp in stride(from: t.buffer.yDisp, through: 0, by: -7) {
            t.buffer.yDisp = yDisp
            for p in o.placed() {
                let text = textAt(t, screenRow: p.screenRow)
                #expect(text.hasPrefix("prompt-"), "a mark on \"\(text)\" at yDisp \(yDisp)")
                seen.insert(String(text.prefix { $0 != " " }))
            }
        }
        // Only the prompts still in the 1,000-line buffer are left.
        #expect(seen == ["prompt-8", "prompt-9", "prompt-last"])
    }

    /// A resize reflows wrapped lines; marks follow their lines.
    @Test func marksFollowTheirLinesThroughAResize() {
        let (t, o, _) = makeTerminal(cols: 80, rows: 24)
        for i in 0 ..< 6 {
            prompt(t, "prompt-\(i)")
            // A long line (100 columns) that wraps differently per width.
            t.feed(text: String(repeating: "x", count: 100) + "\r\n")
        }
        let check = { (label: String) in
            let placed = o.placed()
            #expect(placed.count == 6, "\(label): \(placed.count) marks")
            for p in placed {
                let text = textAt(t, screenRow: p.screenRow)
                #expect(text.hasPrefix("prompt-"), "\(label): a mark on \"\(text)\"")
            }
        }
        check("80 columns")
        t.resize(cols: 40, rows: 24)
        check("40 columns")
        t.resize(cols: 120, rows: 30)
        check("120 columns, 30 rows")
        t.resize(cols: 80, rows: 24)
        check("back to 80")
    }

    /// A full-screen program's screen shows no marks; the main screen's
    /// come back when it exits.
    @Test func noMarksOnTheAlternateScreen() {
        let (t, o, _) = makeTerminal()
        prompt(t, "before")
        t.feed(text: "\u{1b}[?1049h")
        #expect(o.placed().isEmpty)
        t.feed(text: "\u{1b}[?1049l")
        #expect(o.placed().count == 1)
    }

    @Test func commandEndKeepsItsExitCode() {
        let (t, o, _) = makeTerminal()
        t.feed(text: "\u{1b}]133;D;3\u{07}")
        #expect(o.rows.marks.map(\.kind) == [.commandEnd(exit: 3)])
    }
}
