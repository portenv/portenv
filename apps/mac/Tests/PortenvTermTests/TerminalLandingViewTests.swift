// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvKit
@testable import PortenvTerm
import SwiftTerm
import Testing

/// The owner switched from one agent to another in the sidebar and the
/// terminal showed an older scroll position (2026-10-22): the pane's view
/// is shared by the tabs it shows, and a scroll-up in one carried into the
/// next. Each switch now lands at the tab's prompt, unless the owner had
/// scrolled up in that tab and nothing new arrived there since.
@MainActor
struct TerminalLandingViewTests {
    func view() -> MarkedTerminalView {
        _ = NSApplication.shared
        let v = MarkedTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        v.getTerminal().resize(cols: 80, rows: 24)
        return v
    }

    /// A tab's history: `n` numbered lines, then its prompt.
    func seed(_ name: String, lines n: Int) -> [UInt8] {
        Array(((1 ... n).map { "\(name) line \($0)" }.joined(separator: "\r\n") + "\r\n\(name)$ ").utf8)
    }

    func settle(until done: () -> Bool) async {
        for _ in 0 ..< 200 where !done() {
            try? await Task.sleep(for: .milliseconds(10))
        }
    }

    func atBottom(_ v: MarkedTerminalView) -> Bool { v.scrollPosition >= 1 }

    /// Shows `tab` with `seed`, as a switch does, and waits for it.
    func show(_ v: MarkedTerminalView, _ tab: String, _ seed: [UInt8]) async -> ScriptedTabSource {
        let s = ScriptedTabSource(tab: tab)
        v.attach(s)
        s.play(.reset(TabTerminalSize(cols: 80, rows: 24), seed: seed))
        await settle { v.canScroll && v.source === s && v.lastLanding != nil }
        return s
    }

    @Test func switchingToAnotherTabLandsAtItsPrompt() async {
        let v = view()
        _ = await show(v, "lead:@1", seed("lead", lines: 200))
        #expect(atBottom(v))
        v.scrollTo(row: 20) // the owner reads back in lead's tab
        #expect(!atBottom(v))
        _ = await show(v, "writer:@1", seed("writer", lines: 200))
        #expect(atBottom(v), "writer's tab shows its prompt")
    }

    @Test func goingBackKeepsTheOwnersPlaceIfNothingNewArrived() async {
        let v = view()
        let leadSeed = seed("lead", lines: 200)
        _ = await show(v, "lead:@1", leadSeed)
        v.scrollTo(row: 20)
        let place = v.getTerminal().buffer.yDisp
        _ = await show(v, "writer:@1", seed("writer", lines: 200))
        _ = await show(v, "lead:@1", leadSeed)
        #expect(!atBottom(v) && v.getTerminal().buffer.yDisp == place)
    }

    @Test func goingBackLandsAtThePromptIfSomethingNewArrived() async {
        let v = view()
        _ = await show(v, "lead:@1", seed("lead", lines: 200))
        v.scrollTo(row: 20)
        _ = await show(v, "writer:@1", seed("writer", lines: 200))
        _ = await show(v, "lead:@1", seed("lead", lines: 201))
        #expect(atBottom(v))
    }

    /// The sidebar's selection changed but the tab on screen didn't (All
    /// tabs → You on the owner's tab): the same rule, for the tab as it is.
    @Test func aSelectionChangeOnTheSameTabFollowsTheRuleToo() async {
        let v = view()
        let s = await show(v, "@1", seed("you", lines: 200))
        v.scrollTo(row: 20)
        v.shownBySelection()
        #expect(!atBottom(v), "nothing new: the owner's place")
        s.play(.output(Array("\r\nnew line\r\nyou$ ".utf8)))
        await settle { v.getTerminal().getLine(row: 23)?.translateToString(trimRight: true) != nil && v.outputWhileScrolledUp }
        #expect(!atBottom(v), "output alone never pulls the view down")
        v.shownBySelection()
        #expect(atBottom(v), "something new arrived: the prompt")
    }
}
