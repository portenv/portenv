// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import Testing

@testable import PortenvKit

/// Split view's actions (PLAN 1.2b) over the window's tabs: up to four
/// panes, a tab in at most one pane, ⌘W closing panes before tabs, focus
/// moving between panes, and the tab bar's current tab always the focused
/// pane's. Each pane streams its own tab (MainTabTerminal), so this only
/// decides which tab each pane shows.
@MainActor
@Suite(.serialized)
struct SplitControllerTests {
    let window = CGSize(width: 1400, height: 900)

    /// A window's split over tabs named after their IDs, `active` current.
    func make(_ ids: [String], active: String, size: CGSize? = nil) -> (SplitController, TabsController, FakeDaemon) {
        let daemon = FakeDaemon()
        daemon.setTabs(ids.map { TabInfo(id: $0, name: $0, active: $0 == active) })
        let tabs = TabsController(box: "acme-api", daemon: daemon)
        tabs.receive(ids.map { TabInfo(id: $0, name: $0, active: $0 == active) })
        let split = SplitController(tabs: tabs)
        split.size = size ?? window
        split.tabsChanged()
        return (split, tabs, daemon)
    }

    func selects(_ d: FakeDaemon) -> [String] { d.calls.filter { $0.count > 3 && $0[1] == "select-tab" }.map { $0[3] } }

    @Test func startsWithTheCurrentTab() {
        let (s, _, _) = make(["claude", "grok"], active: "grok")
        #expect(s.layout.panes == [PaneID(1)])
        #expect(s.activeTab == "grok")
        #expect(!s.isSplit)
    }

    /// Split Right shows the first tab no pane shows in a new pane, which
    /// takes the focus; the tab bar's current tab follows it.
    @Test func splitShowsTheNextFreeTab() async {
        let (s, tabs, d) = make(["claude", "grok", "tests"], active: "claude")
        await s.split(.right)
        await s.settled()
        #expect(s.layout.panes == [PaneID(1), PaneID(2)])
        #expect(s.layout.focused == PaneID(2))
        #expect(s.activeTab == "grok")
        #expect(tabs.current?.id == "grok")
        #expect(selects(d) == ["grok"])
        #expect(s.layout.tab(in: PaneID(1)) == "claude") // unmoved
    }

    /// With every tab already in a pane, the new pane gets a new shell tab
    /// (as `+` makes, owned by you).
    @Test func splitWithEveryTabShownMakesAShellTab() async {
        let (s, tabs, d) = make(["@0"], active: "@0")
        await s.split(.down)
        await s.settled()
        #expect(d.calls.contains(["app", "new-tab", "acme-api", "shell"]))
        #expect(s.layout.panes.count == 2)
        let made = s.layout.tab(in: PaneID(2))
        #expect(made != nil && made != "@0")
        #expect(tabs.current?.id == made)
        #expect(s.layout.tab(in: PaneID(1)) == "@0")
    }

    /// With You selected in the Team sidebar, a new pane takes the next of
    /// your tabs, never an agent's.
    @Test func splitTakesTheNextTabTheTabBarShows() async {
        let daemon = FakeDaemon()
        let ts = [TabInfo(id: "a", name: "a", active: true), TabInfo(id: "c", name: "c", active: false, agentID: "grok"),
                  TabInfo(id: "b", name: "b", active: false)]
        daemon.setTabs(ts)
        let tabs = TabsController(box: "acme-api", daemon: daemon)
        tabs.receive(ts)
        let s = SplitController(tabs: tabs)
        s.size = window
        s.tabsChanged()
        tabs.view(.you)
        await s.split(.right)
        #expect(s.layout.tab(in: PaneID(2)) == "b")
    }

    /// Agents split their own panes (owner, 2026-10-21, R-0410): with an
    /// agent's view in front the owner can't split, from the menu, its
    /// shortcut or a button; back on You they can.
    @Test func noSplitInAnAgentsView() async {
        let daemon = FakeDaemon()
        let ts = [TabInfo(id: "a", name: "a", active: true), TabInfo(id: "c", name: "c", active: false, agentID: "grok"),
                  TabInfo(id: "d", name: "d", active: false, agentID: "grok")]
        daemon.setTabs(ts)
        let tabs = TabsController(box: "acme-api", daemon: daemon)
        tabs.receive(ts)
        let s = SplitController(tabs: tabs)
        s.size = window
        s.tabsChanged()
        tabs.view(.agent("grok"))
        #expect(!s.canSplit(.right) && !s.canSplit(.down))
        await s.split(.right)
        await s.split(.down)
        #expect(s.layout.panes.count == 1)
        #expect(!daemon.calls.contains { $0.count > 1 && $0[1] == "new-tab" })
        tabs.view(.you)
        #expect(s.canSplit(.right) && s.canSplit(.down))
    }

    /// Four panes at most: a fifth split does nothing, from the menu or a
    /// button.
    @Test func atMostFourPanes() async {
        let (s, _, d) = make(["a", "b", "c", "d", "e"], active: "a")
        await s.split(.right)
        await s.split(.down)
        s.focus(PaneID(1))
        await s.split(.down)
        await s.settled()
        #expect(s.layout.panes.count == 4)
        #expect(!s.canSplit(.right) && !s.canSplit(.down))
        await s.split(.right)
        #expect(s.layout.panes.count == 4)
        #expect(!d.calls.contains { $0.count > 1 && $0[1] == "new-tab" })
        // Four panes, four different tabs.
        #expect(Set(s.layout.panes.compactMap { s.layout.tab(in: $0) }).count == 4)
    }

    /// Choosing a tab (the tab bar, ⌘1–9, ⌃Tab) shows it in the focused
    /// pane; a tab another pane shows focuses that pane instead, so a tab is
    /// never in two panes.
    @Test func choosingATabInTheTabBar() async {
        let (s, tabs, _) = make(["claude", "grok", "tests"], active: "claude")
        await s.split(.right) // grok in pane 2, focused
        await s.settled()
        await tabs.select("tests")
        s.tabsChanged()
        #expect(s.layout.tab(in: PaneID(2)) == "tests")
        #expect(s.layout.focused == PaneID(2))
        await tabs.select("claude")
        s.tabsChanged()
        #expect(s.layout.focused == PaneID(1)) // focus moved, nothing replaced
        #expect(s.layout.tab(in: PaneID(2)) == "tests")
        #expect(s.layout.pane(showing: "grok") == nil)
    }

    /// ⌘⇧] and ⌘⇧[ go from the focused pane's tab.
    @Test func nextTabFromTheFocusedPane() async {
        let (s, tabs, _) = make(["a", "b", "c", "d"], active: "a")
        await s.split(.right) // b in pane 2
        await s.settled()
        await tabs.selectNext()
        s.tabsChanged()
        #expect(s.layout.tab(in: PaneID(2)) == "c")
        #expect(s.layout.tab(in: PaneID(1)) == "a")
    }

    /// The pane header's picker lists every tab but those other panes show,
    /// and choosing one shows it there and focuses that pane.
    @Test func pickerContents() async {
        let (s, tabs, _) = make(["claude", "grok", "tests", "dev"], active: "claude")
        await s.split(.right) // grok
        await s.split(.down) // tests
        await s.settled()
        #expect(s.pickerTabs(for: PaneID(1)).map(\.id) == ["claude", "dev"])
        #expect(s.pickerTabs(for: PaneID(2)).map(\.id) == ["grok", "dev"])
        s.show("dev", in: PaneID(1))
        await s.settled()
        #expect(s.layout.tab(in: PaneID(1)) == "dev")
        #expect(s.layout.focused == PaneID(1))
        #expect(tabs.current?.id == "dev")
        #expect(s.pickerTabs(for: PaneID(3)).map(\.id) == ["claude", "tests"])
    }

    /// Closing a pane never stops its tab: no close reaches portenvd, and
    /// the tab is still listed. The last pane doesn't close.
    @Test func closingAPaneKeepsItsTab() async {
        let (s, tabs, d) = make(["claude", "grok"], active: "claude")
        await s.split(.right)
        await s.settled()
        #expect(s.closePane(PaneID(2)))
        await s.settled()
        #expect(s.layout.panes == [PaneID(1)])
        #expect(s.activeTab == "claude")
        #expect(tabs.current?.id == "claude")
        #expect(tabs.tabs.map(\.id) == ["claude", "grok"])
        #expect(!d.calls.contains { $0.count > 1 && $0[1] == "close-tab" })
        #expect(!s.closePane())
    }

    /// ⌘W closes the focused pane first, and closes tabs only when one pane
    /// is left.
    @Test func commandWClosesPanesFirst() async {
        let (s, _, _) = make(["claude", "grok", "tests"], active: "claude")
        await s.split(.right)
        await s.split(.down)
        #expect(s.commandW() == .closedPane)
        #expect(s.commandW() == .closedPane)
        #expect(s.commandW() == .closeTab)
        #expect(s.layout.panes.count == 1)
    }

    /// ⌘⌥ and the arrows move the focus by position, and the tab bar
    /// follows; with nowhere to go, nothing moves.
    @Test func focusMovement() async {
        let (s, tabs, _) = make(["claude", "grok", "tests"], active: "claude")
        await s.split(.right) // grok, right
        await s.split(.down) // tests, below grok
        await s.settled()
        #expect(s.layout.focused == PaneID(3))
        #expect(s.moveFocus(.up))
        #expect(s.layout.focused == PaneID(2))
        #expect(s.moveFocus(.left))
        #expect(s.layout.focused == PaneID(1))
        #expect(!s.moveFocus(.left))
        #expect(s.layout.focused == PaneID(1))
        await s.settled()
        #expect(tabs.current?.id == "claude")
        #expect(s.moveFocus(.right))
        await s.settled()
        #expect(tabs.current?.id == "grok")
    }

    /// A stale tab list arriving before portenvd has the new current tab
    /// doesn't pull the focus back.
    @Test func aStaleListDoesntMoveTheFocus() async {
        let (s, tabs, d) = make(["claude", "grok"], active: "claude")
        await s.split(.right)
        await s.settled()
        d.onRun = { args in if args.count > 1, args[1] == "select-tab" { try? await Task.sleep(for: .milliseconds(50)) } }
        s.focus(PaneID(1))
        tabs.receive([TabInfo(id: "claude", name: "claude", active: false), TabInfo(id: "grok", name: "grok", active: true)])
        s.tabsChanged()
        #expect(s.layout.focused == PaneID(1))
        d.onRun = nil
    }

    /// A tab closed elsewhere: its pane shows the next free tab, or closes
    /// when there's none.
    @Test func aTabClosedElsewhere() async {
        let (s, tabs, _) = make(["a", "b", "c"], active: "a")
        await s.split(.right) // b
        await s.settled()
        tabs.receive([TabInfo(id: "a", name: "a", active: false), TabInfo(id: "c", name: "c", active: true)])
        s.tabsChanged()
        #expect(s.layout.tab(in: PaneID(2)) == "c")
        tabs.receive([TabInfo(id: "a", name: "a", active: true)])
        s.tabsChanged()
        #expect(s.layout.panes == [PaneID(1)])
        #expect(s.activeTab == "a")
    }

    /// The tab bar's glyphs: where each tab's pane is, when split.
    @Test func whereEachTabIs() async {
        let (s, _, _) = make(["a", "b", "c"], active: "a")
        #expect(s.paneFrame(forTab: "a") == nil) // one pane: no glyphs
        await s.split(.right)
        #expect(s.paneFrame(forTab: "a") == CGRect(x: 0, y: 0, width: 0.5, height: 1))
        #expect(s.paneFrame(forTab: "c") == nil)
    }

    /// At the window's minimum (640 × 440 pt, less the toolbar and the tab
    /// bar), panes keep the terminal's 340 pt: no Split Right, one Split
    /// Down. Four panes need about 680 × 240 pt.
    @Test func layoutAtTheWindowMinimum() async {
        let minimum = CGSize(width: 640, height: 440 - 52 - 30)
        let (s, _, _) = make(["a", "b", "c", "d"], active: "a", size: minimum)
        #expect(!s.canSplit(.right))
        #expect(s.canSplit(.down))
        await s.split(.down)
        #expect(s.layout.panes.count == 2)
        #expect(!s.canSplit(.down))
        // A window made smaller after splitting: the panes still tile it.
        let (big, _, _) = make(["a", "b", "c", "d"], active: "a")
        await big.split(.right)
        await big.split(.down)
        big.focus(PaneID(1))
        await big.split(.down)
        #expect(big.layout.panes.count == 4)
        let frames = big.layout.frames(in: minimum)
        #expect(frames.count == 4)
        let area = frames.values.reduce(0) { $0 + $1.width * $1.height }
        #expect(area == minimum.width * minimum.height)
        for f in frames.values {
            #expect(f.minX >= 0 && f.minY >= 0 && f.maxX <= minimum.width && f.maxY <= minimum.height)
        }
    }
}
