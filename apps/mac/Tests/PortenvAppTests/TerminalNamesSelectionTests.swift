// SPDX-License-Identifier: Apache-2.0

import AppKit
import PortenvTerm
import SwiftUI
import Testing

@testable import Portenv
@testable import PortenvKit

/// What VoiceOver reads on the terminal itself (R-0314): with Code Writer
/// selected in the sidebar, the terminal view in the window says "Terminal:
/// Code Writer · writer", the same agent and tab the sidebar and the tab
/// bar name; never Lead's.
@MainActor
@Suite(.serialized)
struct TerminalNamesSelectionTests {
    let team = [TeamAgent(id: "lead", name: "Lead", status: .running),
                TeamAgent(id: "writer", name: "Code Writer", status: .running)]

    func terminals(in view: NSView) -> [TabTerminalHost] {
        (view as? TabTerminalHost).map { [$0] } ?? view.subviews.flatMap(terminals)
    }

    /// The terminal views' VoiceOver names once the window has drawn.
    func labels(_ split: SplitController) -> [String] {
        let host = NSHostingView(rootView: SplitPanes(split: split, provider: ScriptedTabProvider(), generation: 0,
                                                      box: "acme-api", team: team))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 900, height: 600), styleMask: [.titled],
                              backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = host
        host.layoutSubtreeIfNeeded()
        defer { window.close() }
        return terminals(in: host).compactMap { $0.terminal.accessibilityLabel() }
    }

    @Test func theTerminalNamesTheSelectedAgent() async {
        _ = NSApplication.shared
        let ts = [TabInfo(id: "@1", name: "shell", active: false),
                  TabInfo(id: "lead:@1", name: "lead", active: true, agentID: "lead"),
                  TabInfo(id: "writer:@1", name: "writer", active: false, agentID: "writer")]
        let tabs = TabsController(box: "acme-api", daemon: SampleDaemon(savedAt: Date()))
        tabs.receive(ts)
        let split = SplitController(tabs: tabs)
        split.size = CGSize(width: 900, height: 600)
        split.tabsChanged()
        #expect(labels(split) == ["Terminal: Lead \u{00B7} lead"])
        // The owner selects Code Writer (MainWindow.choose).
        await tabs.view(.agent("writer"))?.value
        split.tabsChanged()
        #expect(labels(split) == ["Terminal: Code Writer \u{00B7} writer"])
        // All tabs: back to the owner's tab.
        await tabs.view(.allTabs)?.value
        split.tabsChanged()
        #expect(labels(split) == ["Terminal: You \u{00B7} shell"])
    }
}
