// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// The terminal's right-click menu (basics audit, R-0384): Copy, Paste,
/// Select All and Find…, as in Terminal. Nothing in it sends anything to the
/// tab but Paste, which takes the view's own cleaned, bracketed paste path.
@MainActor
struct TerminalContextMenuTests {
    func view() -> (MarkedTerminalView, ScriptedTabSource) {
        _ = NSApplication.shared
        let v = TerminalViews.tab()
        v.frame = NSRect(x: 0, y: 0, width: 800, height: 480)
        let s = ScriptedTabSource(tab: "@1")
        v.attach(s)
        return (v, s)
    }

    func rightClick(_ v: MarkedTerminalView) -> NSEvent {
        NSEvent.mouseEvent(with: .rightMouseDown, location: NSPoint(x: 20, y: 20), modifierFlags: [],
                           timestamp: 0, windowNumber: 0, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)!
    }

    @Test func theItemsInOrder() throws {
        let (v, _) = view()
        let menu = try #require(v.menu(for: rightClick(v)))
        #expect(menu.items.map { $0.isSeparatorItem ? "-" : $0.title } == ["Copy", "Paste", "-", "Select All", "-", "Find…"])
        // Each item goes to this view, whichever pane has the keys.
        for item in menu.items where !item.isSeparatorItem { #expect(item.target === v) }
        #expect(menu.items[0].action == #selector(TerminalView.copy(_:)))
        #expect(menu.items[1].action == #selector(TerminalView.paste(_:)))
        #expect(menu.items[3].action == #selector(NSResponder.selectAll(_:)))
        #expect(menu.items[5].action == #selector(NSResponder.performTextFinderAction(_:)))
        #expect(menu.items[5].tag == NSTextFinder.Action.showFindInterface.rawValue)
    }

    @Test func copyNeedsASelection() throws {
        let (v, _) = view()
        v.ingest(Array("hello from the tab\r\n".utf8))
        var menu = try #require(v.menu(for: rightClick(v)))
        #expect(!menu.items[0].isEnabled) // nothing selected
        #expect(menu.items[1].isEnabled)
        #expect(menu.items[3].isEnabled)
        #expect(menu.items[5].isEnabled)
        v.selectAll(nil)
        menu = try #require(v.menu(for: rightClick(v)))
        #expect(menu.items[0].isEnabled)
    }

    @Test func findOpensTheFindBar() throws {
        let (v, _) = view()
        let menu = try #require(v.menu(for: rightClick(v)))
        let find = menu.items[5]
        #expect(!Self.findBarShown(v))
        NSApp.sendAction(try #require(find.action), to: find.target, from: find)
        #expect(Self.findBarShown(v))
    }

    /// Select All sends nothing to the tab; only Paste does, cleaned.
    @Test func onlyPasteSends() throws {
        let (v, s) = view()
        v.ingest(Array("text\r\n".utf8))
        let menu = try #require(v.menu(for: rightClick(v)))
        let all = menu.items[3]
        NSApp.sendAction(try #require(all.action), to: all.target, from: all)
        #expect(s.sent.isEmpty)
    }

    static func findBarShown(_ v: NSView) -> Bool {
        v.subviews.contains { String(describing: type(of: $0)) == "TerminalFindBarView" && !$0.isHidden }
    }
}
