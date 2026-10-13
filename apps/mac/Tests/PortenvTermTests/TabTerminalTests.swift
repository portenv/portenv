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

    /// A screen row's text, without trailing blanks (a prompt ends in a
    /// typed space, which SwiftTerm's trimRight keeps).
    func line(_ v: MarkedTerminalView, _ row: Int) -> String {
        let text = v.getTerminal().getLine(row: row)?.translateToString(trimRight: true) ?? ""
        return String(text.reversed().drop { $0 == " " }.reversed())
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

    @Test func aResetCallsOnResetAndAsksForTheViewsOwnSize() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        var resets: [TabTerminalSize] = []
        v.onReset = { resets.append($0) }
        v.attach(s)
        let own = TabTerminalSize(cols: 80, rows: 24)
        // Another viewer had the tab at 120x40.
        s.play(.reset(TabTerminalSize(cols: 120, rows: 40), seed: Array("x".utf8)))
        await settle { !resets.isEmpty }
        #expect(resets == [TabTerminalSize(cols: 120, rows: 40)])
        #expect(s.sizes.last == own)
        // A reset at the view's own size asks for nothing more.
        let before = s.sizes.count
        s.play(.reset(own, seed: []))
        await settle { resets.count == 2 }
        #expect(s.sizes.count == before)
    }

    @Test func aClosedTabKeepsItsLastScreen() async {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        var ended: TabTerminalEnd?
        v.onEnded = { ended = $0 }
        v.attach(s)
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: Array("last words".utf8)))
        s.play(.ended(.tabClosed))
        await settle { ended != nil }
        #expect(ended == .tabClosed && line(v, 0) == "last words")
    }

    /// The menu's shortcuts (⌘D, ⇧⌘D, ⌘⌥ arrows, ⌘W, ⌘1–9) are never taken
    /// by the terminal.
    @Test func menuShortcutsPassThrough() throws {
        let v = view()
        let keys: [(String, NSEvent.ModifierFlags)] = [("d", .command), ("d", [.command, .shift]), ("w", .command),
                                                       ("1", .command), ("\u{F702}", [.command, .option])]
        for (key, mods) in keys {
            let e = try #require(NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: mods, timestamp: 0,
                                                  windowNumber: 0, context: nil, characters: key,
                                                  charactersIgnoringModifiers: key, isARepeat: false, keyCode: 0))
            #expect(!v.performKeyEquivalent(with: e), "\(mods) \(key) was taken by the terminal")
        }
    }

    /// Clicks over the card layer reach its cards, and the terminal
    /// everywhere else.
    @Test func theCardLayerTakesOnlyItsCards() {
        let v = view()
        v.frame = NSRect(x: 0, y: 0, width: 400, height: 300)
        let card = NSView(frame: NSRect(x: 10, y: 10, width: 100, height: 40))
        v.cards.addSubview(card)
        #expect(v.cards.hitTest(NSPoint(x: 20, y: 20)) === card)
        #expect(v.cards.hitTest(NSPoint(x: 300, y: 200)) == nil)
    }
}

/// The pane's host (ADR 0020, for split view): swapping the source keeps the
/// view, and focus follows the first responder both ways.
@MainActor
struct TabTerminalHostTests {
    func host() -> (TabTerminalHost, NSWindow) {
        _ = NSApplication.shared
        let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 400), styleMask: [.titled],
                         backing: .buffered, defer: false)
        w.isReleasedWhenClosed = false
        let container = NSView(frame: w.contentLayoutRect)
        w.contentView = container
        let h = TabTerminalHost(frame: container.bounds)
        container.addSubview(h)
        return (h, w)
    }

    @Test func aNewSourceSwapsInPlace() {
        let (h, _) = host()
        let view = h.terminal, a = ScriptedTabSource(tab: "@1"), b = ScriptedTabSource(tab: "@2")
        h.show(a)
        h.show(a)
        #expect(!a.closed)
        h.show(b)
        #expect(h.terminal === view && a.closed && h.terminal.source === b)
    }

    @Test func focusFollowsTheFirstResponder() {
        let (h, w) = host()
        var changes: [Bool] = []
        h.onFocusChange = { changes.append($0) }
        h.focus()
        #expect(w.firstResponder === h.terminal)
        w.makeFirstResponder(nil)
        #expect(changes == [true, false])
    }
}

/// The pane header's status (split view), most important first.
struct TabActivityTests {
    @Test func theMostImportantWins() {
        let now = Date(timeIntervalSinceReferenceDate: 10000)
        var tab = TabInfo(id: "@1", name: "grok", active: false, program: "grok", lastOutputAt: now.addingTimeInterval(-5))
        #expect(tab.activity(now: now) == .running("grok"))
        tab.lastOutputAt = now.addingTimeInterval(-TabActivity.quietAfter)
        #expect(tab.activity(now: now) == .quiet(since: now.addingTimeInterval(-TabActivity.quietAfter)))
        tab.watchers = ["Grok Bot"]
        #expect(tab.activity(now: now) == .watching(["Grok Bot"]))
        tab.typingAuthor = TabAuthor(name: "Grok Bot", isYou: false)
        #expect(tab.activity(now: now) == .typing(TabAuthor(name: "Grok Bot", isYou: false)))
        tab.waiting = TabQuestion(text: "Run the tests?", secret: false)
        #expect(tab.activity(now: now) == .asking(TabQuestion(text: "Run the tests?", secret: false)))
        #expect(TabInfo(id: "@2", name: "shell", active: false).activity(now: now) == .idle)
    }

    @Test func aSecretQuestionCarriesNoText() {
        #expect(TabQuestion(text: "Password:", secret: true).text.isEmpty)
    }
}

/// The emulator's replies to a program's queries never reach the tab; what
/// the person types does (ADR 0020, R-0133 item 1).
@MainActor
struct QueryReplyTests {
    static let queries: [(String, String)] = [
        ("DA1", "\u{1b}[c"), ("DA2", "\u{1b}[>c"), ("DA3", "\u{1b}[=c"), ("DSR status", "\u{1b}[5n"),
        ("DSR cursor", "\u{1b}[6n"), ("DECXCPR", "\u{1b}[?6n"), ("DECRQSS", "\u{1b}P$qm\u{1b}\\"),
        ("DECRQM", "\u{1b}[?2004$p"), ("DECRQM ANSI", "\u{1b}[4$p"), ("XTWINOPS size", "\u{1b}[18t"),
        ("XTWINOPS pixels", "\u{1b}[14t"), ("XTWINOPS title", "\u{1b}[21t"), ("XTVERSION", "\u{1b}[>q"),
        ("XTGETTCAP", "\u{1b}P+q544e\u{1b}\\"), ("OSC 4", "\u{1b}]4;1;?\u{07}"), ("OSC 10", "\u{1b}]10;?\u{07}"),
        ("OSC 11", "\u{1b}]11;?\u{1b}\\"), ("OSC 52 query", "\u{1b}]52;c;?\u{07}"), ("DECREQTPARM", "\u{1b}[x"),
        ("ENQ", "\u{05}"),
    ]

    @Test func noQueryGetsAReplyIntoTheTab() async {
        _ = NSApplication.shared
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        let s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: []))
        for (_, query) in Self.queries { s.play(.output(Array(query.utf8))) }
        s.play(.output(Array("\r\nDONE".utf8)))
        func drawn() -> Bool {
            (0 ..< 24).contains { v.getTerminal().getLine(row: $0)?.translateToString(trimRight: true) == "DONE" }
        }
        for _ in 0 ..< 200 where !drawn() { try? await Task.sleep(for: .milliseconds(10)) }
        #expect(drawn())
        #expect(s.sent.isEmpty, "replies reached the tab: \(s.sent.map { String(decoding: $0, as: UTF8.self).debugDescription })")
        // The person's typing still goes.
        v.send(txt: "y")
        #expect(s.sent.flatMap { $0 } == Array("y".utf8))
    }
}
