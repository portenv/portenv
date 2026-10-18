// SPDX-License-Identifier: Apache-2.0

import AppKit
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// Split view's panes in the window (PLAN 1.2b): four panes, each with its
/// header and its own tab's terminal, never ask for more room than the
/// window's minimum gives them, so a split never makes the window grow.
@MainActor
@Suite(.serialized)
struct SplitPanesLayoutTests {
    @Test func fourPanesFitTheWindowMinimum() async {
        _ = NSApplication.shared
        let ts = ["claude", "grok", "tests", "dev"].enumerated().map { TabInfo(id: "@\($0.offset)", name: $0.element, active: $0.offset == 0) }
        let tabs = TabsController(box: "acme-api", daemon: SampleDaemon(savedAt: Date()))
        tabs.receive(ts)
        let split = SplitController(tabs: tabs)
        split.size = CGSize(width: 1280, height: 800)
        split.tabsChanged()
        await split.split(.right)
        await split.split(.down)
        split.focus(PaneID(1))
        await split.split(.down)
        #expect(split.layout.panes.count == 4)
        let provider = ScriptedTabProvider(seeds: ["@0": "$ claude --continue"])
        let host = NSHostingController(rootView: SplitPanes(split: split, provider: provider, generation: 0, box: "acme-api"))
        // The window's minimum, less the toolbar and the tab bar.
        let minimum = CGSize(width: 640, height: 440 - 52 - 30)
        let fit = host.sizeThatFits(in: minimum)
        #expect(fit.width <= minimum.width && fit.height <= minimum.height, "the panes want \(fit)")
    }
}
