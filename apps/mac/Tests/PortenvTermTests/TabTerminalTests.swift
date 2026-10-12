// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// The per-tab terminal interface (ADR 0020) on the real view: a source's
/// events draw the tab, and what the view produces goes back to it.
@MainActor
struct TabTerminalTests {
    func view() -> MarkedTerminalView {
        _ = NSApplication.shared
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        v.getTerminal().resize(cols: 80, rows: 24)
        return v
    }

    func line(_ v: MarkedTerminalView, _ row: Int) -> String {
        v.getTerminal().getLine(row: row)?.translateToString(trimRight: true) ?? ""
    }

    /// Lets the view's reading task take the events played so far: waits
    /// until `done` holds, for at most 2 s.
    func settle(until done: () -> Bool) async {
        for _ in 0 ..< 200 where !done() {
            try? await Task.sleep(for: .milliseconds(10))
        }
    }

    @Test func aResetDrawsTheSeedWithItsMarksThenOutputFollows() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        #expect(s.sizes.last == TabTerminalSize(cols: 80, rows: 24))
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24),
                      seed: Array("\u{1b}]133;A\u{07}P1$ \u{1b}]133;B\u{07}ls\r\n\u{1b}]133;C\u{07}a b\r\n".utf8)))
        s.play(.output(Array("\u{1b}]133;D;0\u{07}\u{1b}]133;A\u{07}P2$ ".utf8)))
        await settle { line(v, 2) == "P2$" }
        #expect(line(v, 0) == "P1$ ls")
        #expect(line(v, 2) == "P2$")
        #expect(v.tracker.placed().filter { $0.mark.kind == .prompt }.map(\.screenRow) == [0, 2])
    }

    @Test func aSecondResetStartsOver() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: Array("\u{1b}]133;A\u{07}old$ ".utf8)))
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: Array("\u{1b}]133;A\u{07}new$ ".utf8)))
        await settle { line(v, 0) == "new$" }
        #expect(line(v, 0) == "new$")
        #expect(v.tracker.rows.marks.count == 1)
    }

    @Test func typingAndResizingGoToTheSource() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        v.send(txt: "ls\r")
        #expect(s.sent.flatMap { $0 } == Array("ls\r".utf8))
        v.setFrameSize(NSSize(width: 400, height: 240))
        #expect(s.sizes.count >= 2 && s.sizes.last != TabTerminalSize(cols: 80, rows: 24))
    }

    @Test func theTabSizeFromTheBoxWins() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: []))
        s.play(.resized(TabTerminalSize(cols: 100, rows: 30)))
        await settle { v.getTerminal().cols == 100 }
        #expect(v.getTerminal().cols == 100 && v.getTerminal().rows == 30)
    }

    @Test func switchingTabsClosesTheOldStreamOnly() async {
        let v = view(), a = ScriptedTabSource(tab: "@1"), b = ScriptedTabSource(tab: "@2")
        v.attach(a)
        v.attach(b)
        #expect(a.closed && !b.closed)
        var ended: TabTerminalEnd?
        v.onEnded = { ended = $0 }
        b.play(.ended(.tabClosed))
        await settle { ended != nil }
        #expect(ended == .tabClosed)
    }
}
