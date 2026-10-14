// SPDX-License-Identifier: Apache-2.0

import Testing

@testable import PortenvKit

/// The main window's terminal follows the current tab (ADR 0020).
@MainActor
struct TabTerminalShownTests {
    final class Provider: TabTerminalProvider {
        var opened: [(tab: String, size: TabTerminalSize)] = []
        var sources: [ScriptedTabSource] = []
        func open(tab: String, size: TabTerminalSize) -> any TabTerminalSource {
            opened.append((tab, size))
            let s = ScriptedTabSource(tab: tab)
            sources.append(s)
            return s
        }
    }

    @Test func showsTheCurrentTabAndSwitchesInPlace() {
        let p = Provider()
        let shown = TabTerminalShown(provider: p)
        shown.show(tab: "@0", generation: 1)
        shown.show(tab: "@0", generation: 1) // the same: nothing new
        #expect(p.opened.map(\.tab) == ["@0"])
        #expect(shown.connection == .connecting)
        shown.reset(TabTerminalSize(cols: 100, rows: 30))
        #expect(shown.connection == .live)
        // Another tab: its own stream at the size the view had, the old one closed.
        shown.show(tab: "@4", generation: 1)
        #expect(p.opened.map(\.tab) == ["@0", "@4"])
        #expect(p.opened.last?.size == TabTerminalSize(cols: 100, rows: 30))
        #expect(p.sources[0].closed)
        // The box opened again: the same tab, a new stream.
        shown.show(tab: "@4", generation: 2)
        #expect(p.opened.count == 3)
        #expect(p.sources[1].closed)
        shown.stop()
        #expect(p.sources[2].closed)
        #expect(shown.source == nil)
    }

    @Test func saysReconnectingThenDisconnectedAndReconnects() {
        let p = Provider()
        let shown = TabTerminalShown(provider: p)
        shown.show(tab: "@1", generation: 1)
        shown.reset(TabTerminalSize(cols: 80, rows: 24))
        shown.ended(.lost(retrying: true))
        #expect(shown.connection == .reconnecting)
        shown.reset(TabTerminalSize(cols: 80, rows: 24))
        #expect(shown.connection == .live)
        shown.ended(.lost(retrying: false))
        #expect(shown.connection == .disconnected(nil))
        shown.reconnect()
        #expect(p.opened.map(\.tab) == ["@1", "@1"])
        #expect(p.sources[0].closed)
        #expect(shown.connection == .connecting)
        // A closed tab or a stopped box leaves it to the window.
        shown.ended(.tabClosed)
        shown.ended(.boxStopped)
        #expect(shown.connection == .connecting)
    }
}
