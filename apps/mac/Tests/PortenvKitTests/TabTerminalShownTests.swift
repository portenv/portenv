// SPDX-License-Identifier: Apache-2.0

import GRPCCore
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

/// While portenvd restarts, the tab view says it's reconnecting, never
/// "Disconnected" with a Reconnect that can't work yet (R-0242).
@MainActor
struct ReconnectingToServiceTests {
    @Test func theLineShowsWhileTheServiceComesBack() {
        let shown = TabTerminalShown(provider: TabTerminalShownTests.Provider())
        shown.show(tab: "@1", generation: 1)
        shown.reset(TabTerminalSize(cols: 80, rows: 24))
        #expect(!shown.showsReconnecting(serviceComing: true)) // live: nothing over the terminal
        shown.ended(.lost(retrying: true))
        #expect(shown.showsReconnecting(serviceComing: false))
        shown.ended(.lost(retrying: false))
        #expect(shown.showsReconnecting(serviceComing: true)) // the service is coming back: not Disconnected
        #expect(!shown.showsReconnecting(serviceComing: false)) // it isn't: Disconnected, with Reconnect
        #expect(TabTerminalShown.reconnectingLine == "Reconnecting to Portenv\u{2019}s background service\u{2026}")
    }

    @Test func notOpenYetAfterARestartIsRetried() {
        let e = TabTerminalStreamError(rpc: RPCError(code: .failedPrecondition, message: "test-119 is not open on this Mac"))
        #expect(!e.permanent)
        let tooOld = TabTerminalStreamError(rpc: RPCError(code: .failedPrecondition, message: "This workspace's tmux is too old for the app's terminal (needs 3.4 or later)."))
        #expect(tooOld.permanent)
    }
}
