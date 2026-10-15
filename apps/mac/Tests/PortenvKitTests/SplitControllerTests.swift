// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import Testing

@testable import PortenvKit

/// Split view's actions (PLAN 1.2b, R-0131): each pane is its own viewer,
/// reached only through PaneTerminals, so the terminal behind a pane can be
/// swapped (lane A's per-tab terminal) without touching this.
@MainActor
struct SplitControllerTests {
    let window = CGSize(width: 1400, height: 900)

    /// Records what the panes were asked to show.
    final class FakeTerminals: PaneTerminals {
        var calls: [String] = []
        var made = 0
        func show(tab: String, in pane: PaneID) async throws { calls.append("show \(tab) in \(pane.viewer)") }
        func newTab(name: String, in pane: PaneID) async throws -> TabInfo {
            made += 1
            calls.append("new \(name) in \(pane.viewer)")
            return TabInfo(id: "@new\(made)", name: name, active: false)
        }
        func closed(_ pane: PaneID) { calls.append("closed \(pane.viewer)") }
    }

    func tabs(_ ids: [String], active: String) -> [TabInfo] {
        ids.map { TabInfo(id: $0, name: $0, active: $0 == active) }
    }

    @Test func startsWithTheAppsTab() {
        let s = SplitController(terminals: FakeTerminals())
        s.tabsChanged(tabs(["claude", "grok"], active: "grok"))
        #expect(s.layout.tab(in: PaneID(1)) == "grok")
        #expect(s.activeTab == "grok")
    }

    /// Split Right shows the first tab no pane shows in a new pane, as its
    /// own viewer, and focuses it.
    @Test func splitShowsTheNextTabInANewViewer() async {
        let t = FakeTerminals()
        let s = SplitController(terminals: t)
        s.tabsChanged(tabs(["claude", "grok", "tests"], active: "claude"))
        await s.split(.right, in: window)
        #expect(t.calls == ["show grok in app-2"])
        #expect(s.layout.focused == PaneID(2))
        #expect(s.activeTab == "grok")
    }

    /// With every tab already in a pane, the new pane gets a new tab, made
    /// for its own viewer only (the other panes don't move).
    @Test func splitWithEveryTabShownMakesATab() async {
        let t = FakeTerminals()
        let s = SplitController(terminals: t)
        s.tabsChanged(tabs(["shell"], active: "shell"))
        await s.split(.down, in: window)
        #expect(t.calls == ["new shell 2 in app-2"])
        #expect(s.layout.tab(in: PaneID(2)) == "@new1")
    }

    /// A split that doesn't fit, or a fifth pane, does nothing.
    @Test func splitThatDoesntFit() async {
        let t = FakeTerminals()
        let s = SplitController(terminals: t)
        s.tabsChanged(tabs(["a", "b"], active: "a"))
        await s.split(.right, in: CGSize(width: 600, height: 900))
        #expect(t.calls.isEmpty)
        #expect(s.layout.panes.count == 1)
    }

    /// Choosing a tab shows it in the focused pane; a tab another pane
    /// shows moves the focus there instead (one tab, one pane).
    @Test func choosingATab() async {
        let t = FakeTerminals()
        let s = SplitController(terminals: t)
        s.tabsChanged(tabs(["claude", "grok", "tests"], active: "claude"))
        await s.split(.right, in: window) // grok in pane 2
        t.calls = []
        await s.show("tests")
        #expect(t.calls == ["show tests in app-2"])
        await s.show("claude")
        #expect(t.calls == ["show tests in app-2"]) // no call: focus moved
        #expect(s.layout.focused == PaneID(1))
    }

    /// Closing a pane never stops its tab; the last pane doesn't close.
    @Test func closingAPaneKeepsItsTab() async {
        let t = FakeTerminals()
        let s = SplitController(terminals: t)
        s.tabsChanged(tabs(["claude", "grok"], active: "claude"))
        await s.split(.right, in: window)
        t.calls = []
        #expect(s.closeFocusedPane())
        #expect(t.calls == ["closed app-2"])
        #expect(s.layout.panes == [PaneID(1)])
        #expect(!s.closeFocusedPane())
    }

    /// ⌘W closes the focused pane first, and closes tabs only when one pane
    /// is left.
    @Test func commandWClosesPanesFirst() async {
        let s = SplitController(terminals: FakeTerminals())
        s.tabsChanged(tabs(["claude", "grok"], active: "claude"))
        await s.split(.right, in: window)
        #expect(s.commandW() == .closedPane)
        #expect(s.commandW() == .closeTab)
    }

    /// ⌘1–⌘9 and the next and previous tab go through the focused pane.
    @Test func nextAndPreviousFromTheFocusedPane() async {
        let t = FakeTerminals()
        let s = SplitController(terminals: t)
        s.tabsChanged(tabs(["a", "b", "c", "d"], active: "a"))
        await s.split(.right, in: window) // b in pane 2
        t.calls = []
        await s.showNext()
        #expect(t.calls == ["show c in app-2"])
        await s.showNumber(4)
        #expect(t.calls == ["show c in app-2", "show d in app-2"])
        await s.showPrevious() // c, in no pane
        #expect(t.calls.last == "show c in app-2")
    }

    /// A tab closed elsewhere: its pane shows another one.
    @Test func aTabClosedElsewhere() async {
        let t = FakeTerminals()
        let s = SplitController(terminals: t)
        s.tabsChanged(tabs(["a", "b", "c"], active: "a"))
        await s.split(.right, in: window) // b
        t.calls = []
        s.tabsChanged(tabs(["a", "c"], active: "a"))
        await s.settled()
        #expect(s.layout.tab(in: PaneID(2)) == "c")
        #expect(t.calls == ["show c in app-2"])
    }

    /// The tab bar's glyphs: where each tab's pane is, when split.
    @Test func whereEachTabIs() async {
        let s = SplitController(terminals: FakeTerminals())
        s.tabsChanged(tabs(["a", "b", "c"], active: "a"))
        #expect(s.paneFrame(forTab: "a") == nil) // one pane: no glyphs
        await s.split(.right, in: window)
        #expect(s.paneFrame(forTab: "a") == CGRect(x: 0, y: 0, width: 0.5, height: 1))
        #expect(s.paneFrame(forTab: "c") == nil)
    }
}
