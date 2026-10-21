// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// The text size on the real tab view (R-0362): a new size reflows the grid
/// and reaches the tab as a resize, the way a window resize does; ⌘= and
/// ⌘⇧= reach Bigger from the focused terminal, and nothing else is taken.
@MainActor
struct TerminalTextSizeViewTests {
    func view() -> MarkedTerminalView {
        _ = NSApplication.shared
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        v.getTerminal().resize(cols: 80, rows: 24)
        return v
    }

    @Test func startsAtTheDefaultSize() {
        #expect(view().textSize == TerminalTextSize.defaultPoints)
        #expect(view().font.pointSize == TerminalTextSize.defaultPoints)
    }

    @Test func aBiggerSizeReflowsAndResizesTheTab() {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        let t = v.getTerminal()
        v.textSize = 20
        #expect(v.font.pointSize == 20)
        let big = TabTerminalSize(cols: t.cols, rows: t.rows)
        // The same path as a window resize: SizeRelay to the source.
        #expect(s.sizes.last == big)
        #expect(v.fitted == big)
        v.textSize = 13
        let small = TabTerminalSize(cols: t.cols, rows: t.rows)
        #expect(big.cols < small.cols && big.rows < small.rows)
        #expect(s.sizes.last == small)
        #expect(v.fitted == small)
    }

    @Test func aFullScreenProgramKeepsItsModes() {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        // Cursor keys in application mode, as vim and less set them:
        // SwiftTerm's font setter would reset them, and the arrow keys
        // would stop working in the program.
        v.ingest(Array("\u{1b}[?1h".utf8))
        let t = v.getTerminal()
        #expect(t.applicationCursor)
        v.textSize = 20
        #expect(t.applicationCursor)
        #expect(s.sizes.last == TabTerminalSize(cols: t.cols, rows: t.rows))
    }

    @Test func theSameSizeSendsNothing() {
        let v = view(), s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        v.textSize = 20
        let count = s.sizes.count
        v.textSize = 20
        #expect(s.sizes.count == count)
    }

    func key(_ chars: String, _ flags: NSEvent.ModifierFlags, code: UInt16 = 24) -> NSEvent {
        NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags, timestamp: 0, windowNumber: 0,
                         context: nil, characters: chars, charactersIgnoringModifiers: chars,
                         isARepeat: false, keyCode: code)!
    }

    func focused() -> (MarkedTerminalView, NSWindow) {
        let v = view()
        let w = NSWindow(contentRect: v.frame, styleMask: [.borderless], backing: .buffered, defer: false)
        w.contentView = v
        w.makeFirstResponder(v)
        return (v, w)
    }

    @Test func commandEqualsAndShiftEqualsAskForBigger() {
        let (v, w) = focused()
        var steps: [TerminalTextSize.Step] = []
        v.onTextSizeKey = { steps.append($0) }
        #expect(v.performKeyEquivalent(with: key("=", [.command])))
        #expect(v.performKeyEquivalent(with: key("+", [.command, .shift])))
        #expect(v.performKeyEquivalent(with: key("+", [.command])))
        #expect(steps == [.bigger, .bigger, .bigger])
        _ = w
    }

    @Test func otherKeysGoOnAsBefore() {
        let (v, w) = focused()
        var steps: [TerminalTextSize.Step] = []
        v.onTextSizeKey = { steps.append($0) }
        // ⌘− and ⌘0 are the menu's (Smaller, Show Sidebar); ⌥⌘= and plain =
        // aren't text size at all.
        #expect(!v.performKeyEquivalent(with: key("-", [.command], code: 27)))
        #expect(!v.performKeyEquivalent(with: key("0", [.command], code: 29)))
        #expect(!v.performKeyEquivalent(with: key("=", [.command, .option])))
        #expect(!v.performKeyEquivalent(with: key("=", [])))
        #expect(steps.isEmpty)
        _ = w
    }

    @Test func onlyTheFocusedTerminalTakesThem() {
        // Every view in a window sees a key equivalent; the pane that isn't
        // focused lets it pass.
        let v = view()
        var steps: [TerminalTextSize.Step] = []
        v.onTextSizeKey = { steps.append($0) }
        #expect(!v.performKeyEquivalent(with: key("=", [.command])))
        #expect(steps.isEmpty)
    }
}
