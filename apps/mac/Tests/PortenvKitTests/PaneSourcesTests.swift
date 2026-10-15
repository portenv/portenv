// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// Each pane's terminal source (ADR 0020, R-0171): a pane opens its own
/// source from the workspace's TabTerminalProvider; choosing another tab
/// opens a new one and closes the old; closing the pane closes its source
/// and never the tab.
@MainActor
struct PaneSourcesTests {
    final class Provider: TabTerminalProvider {
        var opened: [(String, TabTerminalSize)] = []
        var sources: [ScriptedTabSource] = []
        func open(tab: String, size: TabTerminalSize) -> any TabTerminalSource {
            opened.append((tab, size))
            let s = ScriptedTabSource(tab: tab)
            sources.append(s)
            return s
        }
    }

    @Test func eachPaneHasItsOwnSource() async throws {
        let p = Provider()
        let panes = PaneSources(provider: p, newTab: { name, _ in TabInfo(id: "@9", name: name, active: false) })
        try await panes.show(tab: "@1", in: PaneID(1))
        try await panes.show(tab: "@2", in: PaneID(2))
        #expect(panes.source(for: PaneID(1))?.tab == "@1")
        #expect(panes.source(for: PaneID(2))?.tab == "@2")
        #expect(p.opened.map(\.0) == ["@1", "@2"])
        #expect(p.opened[0].1 == PaneSources.startSize)
    }

    @Test func anotherTabOpensANewSourceAndClosesTheOld() async throws {
        let p = Provider()
        let panes = PaneSources(provider: p, newTab: { name, _ in TabInfo(id: "@9", name: name, active: false) })
        try await panes.show(tab: "@1", in: PaneID(1))
        try await panes.show(tab: "@3", in: PaneID(1))
        #expect(p.sources[0].closed)
        #expect(!p.sources[1].closed)
        #expect(panes.source(for: PaneID(1))?.tab == "@3")
        try await panes.show(tab: "@3", in: PaneID(1)) // the same tab: nothing new
        #expect(p.sources.count == 2)
    }

    @Test func closingAPaneClosesOnlyItsSource() async throws {
        let p = Provider()
        let panes = PaneSources(provider: p, newTab: { name, _ in TabInfo(id: "@9", name: name, active: false) })
        try await panes.show(tab: "@1", in: PaneID(1))
        try await panes.show(tab: "@2", in: PaneID(2))
        panes.closed(PaneID(2))
        #expect(p.sources[1].closed)
        #expect(!p.sources[0].closed)
        #expect(panes.source(for: PaneID(2)) == nil)
    }

    /// A new pane's own tab is made for that pane's viewer, then shown.
    @Test func aNewTabForAPane() async throws {
        let p = Provider()
        var asked: [String] = []
        let panes = PaneSources(provider: p, newTab: { name, pane in
            asked.append("\(name) for \(pane.viewer)")
            return TabInfo(id: "@7", name: name, active: false)
        })
        let t = try await panes.newTab(name: "shell 2", in: PaneID(2))
        #expect(t.id == "@7")
        #expect(asked == ["shell 2 for app-2"])
        #expect(panes.source(for: PaneID(2))?.tab == "@7")
    }
}
