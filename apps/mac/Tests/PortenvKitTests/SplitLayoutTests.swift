// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation
import Testing

@testable import PortenvKit

/// Split view's layout (PLAN 1.2b, the split-view board, R-0131): up to four
/// panes, each its own viewer showing one tab; splitting, closing, focus,
/// resizing and moving between panes.
struct SplitLayoutTests {
    let window = CGSize(width: 1400, height: 900)

    /// The board: claude on the left, grok over tests on the right.
    func board() -> SplitLayout {
        var l = SplitLayout(tab: "claude")
        _ = l.split(.right, showing: "grok", in: window)
        _ = l.split(.down, showing: "tests", in: window)
        return l
    }

    @Test func oneTabOnePaneToStart() {
        let l = SplitLayout(tab: "claude")
        #expect(l.panes == [PaneID(1)])
        #expect(l.focused == PaneID(1))
        #expect(l.tab(in: PaneID(1)) == "claude")
        #expect(PaneID(1).viewer == "app") // the app's existing viewer
        #expect(PaneID(3).viewer == "app-3")
    }

    @Test func splittingFocusesTheNewPane() {
        let l = board()
        #expect(l.panes == [PaneID(1), PaneID(2), PaneID(3)])
        #expect(l.focused == PaneID(3))
        #expect(l.tab(in: PaneID(2)) == "grok")
        let f = l.frames(in: window)
        #expect(f[PaneID(1)] == CGRect(x: 0, y: 0, width: 700, height: 900))
        #expect(f[PaneID(2)] == CGRect(x: 700, y: 0, width: 700, height: 450))
        #expect(f[PaneID(3)] == CGRect(x: 700, y: 450, width: 700, height: 450))
    }

    @Test func atMostFourPanes() {
        var l = board()
        l.focus(PaneID(1))
        let r1 = l.split(.down, showing: "dev", in: window)
        #expect(r1 == PaneID(4))
        #expect(!l.canSplit(.right, in: window))
        let r2 = l.split(.right, showing: "x", in: window)
        #expect(r2 == nil)
        #expect(l.panes.count == 4)
    }

    /// A pane never gets narrower than the terminal's 340 pt (GUIDELINES
    /// §3), nor shorter than 120 pt.
    @Test func noSplitBelowTheMinimum() {
        var l = SplitLayout(tab: "a")
        let narrow = CGSize(width: 660, height: 900)
        #expect(!l.canSplit(.right, in: narrow))
        let r3 = l.split(.right, showing: "b", in: narrow)
        #expect(r3 == nil)
        #expect(l.canSplit(.down, in: narrow))
        let low = CGSize(width: 1400, height: 230)
        #expect(!l.canSplit(.down, in: low))
    }

    /// Closing a pane gives its space to its neighbour and focuses it; the
    /// last pane never closes (⌘W then closes the tab).
    @Test func closingGivesTheSpaceBack() {
        var l = board()
        let r4 = l.close(PaneID(3))
        #expect(r4)
        #expect(l.panes == [PaneID(1), PaneID(2)])
        #expect(l.focused == PaneID(2))
        #expect(l.frames(in: window)[PaneID(2)] == CGRect(x: 700, y: 0, width: 700, height: 900))
        let r5 = l.close(PaneID(1))
        #expect(r5)
        #expect(l.panes == [PaneID(2)])
        let r6 = l.close(PaneID(2))
        #expect(!r6)
        // The freed number is used again.
        _ = l.split(.right, showing: "c", in: window)
        #expect(l.panes == [PaneID(2), PaneID(1)])
    }

    /// ⌘⌥ and the arrows move between panes by where they are.
    @Test func arrowsMoveBetweenPanes() {
        var l = board() // focus: tests (bottom right)
        let r7 = l.moveFocus(.up, in: window)
        #expect(r7)
        #expect(l.focused == PaneID(2))
        let above = l.moveFocus(.up, in: window)
        #expect(!above) // nothing above
        let r8 = l.moveFocus(.left, in: window)
        #expect(r8)
        #expect(l.focused == PaneID(1))
        let r9 = l.moveFocus(.right, in: window)
        #expect(r9)
        #expect(l.focused == PaneID(2)) // the top one, nearest
        let r10 = l.moveFocus(.down, in: window)
        #expect(r10)
        #expect(l.focused == PaneID(3))
    }

    /// Dragging a divider resizes, never below the minimum on either side.
    @Test func resizingKeepsTheMinimum() {
        var l = board()
        l.resize(at: [], to: 0.3, in: window) // the vertical divider
        #expect(l.frames(in: window)[PaneID(1)]?.width == 420)
        l.resize(at: [], to: 0.05, in: window)
        #expect(l.frames(in: window)[PaneID(1)]?.width == 340)
        l.resize(at: [], to: 0.99, in: window)
        #expect(l.frames(in: window)[PaneID(2)]?.width == 340)
        l.resize(at: [1], to: 0.25, in: window) // between grok and tests
        #expect(l.frames(in: window)[PaneID(2)]?.height == 225)
    }

    /// A tab shows in at most one pane: which pane shows it, and the tab a
    /// new pane gets (the first one no pane shows).
    @Test func eachTabInOnePane() {
        let l = board()
        #expect(l.pane(showing: "grok") == PaneID(2))
        #expect(l.pane(showing: "dev") == nil)
        #expect(l.tabForNewPane(from: ["claude", "grok", "tests", "dev"]) == "dev")
        #expect(l.tabForNewPane(from: ["claude", "grok", "tests"]) == nil)
    }

    /// A tab that's gone: its pane shows the first tab no pane shows, or
    /// closes if every tab is shown (closing a pane never stops a tab, so
    /// this only follows tabs closed elsewhere).
    @Test func tabsThatAreGone() {
        var l = board()
        let changed = l.tabsChanged(["claude", "tests", "dev"])
        #expect(changed == [PaneID(2): "dev"])
        #expect(l.tab(in: PaneID(2)) == "dev")
        _ = l.tabsChanged(["claude", "dev"])
        #expect(l.panes == [PaneID(1), PaneID(2)])
    }

    /// The pane's glyph in the tab bar: where it is in the window, from 0 to 1.
    @Test func whereEachPaneIs() {
        let l = board()
        #expect(l.unitFrame(of: PaneID(1)) == CGRect(x: 0, y: 0, width: 0.5, height: 1))
        #expect(l.unitFrame(of: PaneID(3)) == CGRect(x: 0.5, y: 0.5, width: 0.5, height: 0.5))
    }

    /// VoiceOver reads each pane as "Pane: grok tab", then what it's doing.
    @Test func paneLabels() {
        #expect(SplitText.paneLabel(tab: "grok", status: "Grok Bot is typing") == "Pane: grok tab, Grok Bot is typing")
        #expect(SplitText.paneLabel(tab: "tests", status: nil) == "Pane: tests tab")
        #expect(SplitText.splitRight == "Split Right")
        #expect(SplitText.splitDown == "Split Down")
        #expect(SplitText.closePane == "Close Pane")
    }
}
