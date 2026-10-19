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
        #expect(shown.showsReconnecting(serviceComing: true)) // the service is coming back: not Disconnected (until the deadline)
        #expect(!shown.showsReconnecting(serviceComing: false)) // it isn't: Disconnected, with Reconnect
        #expect(shown.overlay(serviceComing: false) == .disconnected(nil))
        #expect(TabTerminalShown.reconnectingLine == "Reconnecting to Portenv\u{2019}s background service\u{2026}")
    }

    @Test func notOpenYetAfterARestartIsRetried() {
        let e = TabTerminalStreamError(rpc: RPCError(code: .failedPrecondition, message: "test-119 is not open on this Mac"))
        #expect(!e.permanent)
        let tooOld = TabTerminalStreamError(rpc: RPCError(code: .failedPrecondition, message: "This workspace's tmux is too old for the app's terminal (needs 3.4 or later)."))
        #expect(tooOld.permanent)
    }
}

/// Timers the test fires by hand, so the deadline tests don't depend on
/// how busy the main actor is (the full suite runs 600 tests at once).
@MainActor
final class ManualTimers {
    private var waiting: [(Duration, CheckedContinuation<Void, Never>)] = []

    nonisolated var sleep: @Sendable (Duration) async -> Void {
        { [self] d in await self.wait(d) }
    }

    private func wait(_ d: Duration) async {
        await withCheckedContinuation { waiting.append((d, $0)) }
    }

    /// How many timers of this length are waiting.
    func pending(_ d: Duration) -> Int { waiting.filter { $0.0 == d }.count }

    /// Ends every wait of this length (cancelled ones see their
    /// cancellation and do nothing).
    func fire(_ d: Duration) {
        let hit = waiting.filter { $0.0 == d }
        waiting.removeAll { $0.0 == d }
        for (_, c) in hit { c.resume() }
    }
}

/// The outage gaps from the owner's restart test of #119 (R-0294): the
/// Disconnected card past the deadline, however the contact comes and goes.
@MainActor
struct DisconnectedDeadlineTests {
    static let deadline: Duration = .seconds(25)
    static let settle: Duration = .seconds(2)

    func make() -> (TabTerminalShown, TabTerminalShownTests.Provider, ManualTimers) {
        let p = TabTerminalShownTests.Provider()
        let timers = ManualTimers()
        let shown = TabTerminalShown(provider: p, deadline: Self.deadline, settle: Self.settle, sleep: timers.sleep)
        shown.show(tab: "@1", generation: 1)
        shown.reset(TabTerminalSize(cols: 80, rows: 24))
        return (shown, p, timers)
    }

    /// Counts turns, not the clock (ControlModeTabsTests.waitUntil).
    func waitUntil(_ cond: @MainActor () -> Bool) async {
        for _ in 0..<1000 where !cond() { try? await Task.sleep(for: .milliseconds(5)) }
    }

    /// Step 7a: portenvd killed again within a second of each restart. The
    /// blips (a reset, then lost again before it settles) never start the
    /// clock over, so the card shows at the deadline from the first loss,
    /// saying nothing is being saved, even while the service is said to be
    /// coming back.
    @Test func intermittentContactStillShowsTheCard() async {
        let (shown, _, timers) = make()
        shown.ended(.lost(retrying: true))
        #expect(shown.overlay(serviceComing: true) == .reconnecting)
        await waitUntil { timers.pending(Self.deadline) == 1 }
        for _ in 0..<5 {
            shown.reset(TabTerminalSize(cols: 80, rows: 24)) // back for a moment
            await waitUntil { timers.pending(Self.settle) >= 1 }
            shown.ended(.lost(retrying: true)) // and gone again before it settles
        }
        timers.fire(Self.settle) // the cancelled settles: nothing
        #expect(timers.pending(Self.deadline) == 1, "one deadline, from the first loss")
        timers.fire(Self.deadline)
        await waitUntil { shown.overdue }
        #expect(shown.overdue)
        #expect(shown.overlay(serviceComing: true) == .disconnected(TabTerminalShown.serviceAwayLine))
        #expect(TabTerminalShown.serviceAwayLine.contains("nothing is being saved"))
        #expect(shown.overlay(serviceComing: false) == .disconnected(TabTerminalShown.notAnsweringLine))
        // It comes back and stays: the card goes once it has settled.
        shown.reset(TabTerminalSize(cols: 80, rows: 24))
        #expect(shown.overdue, "not before it has stayed up")
        await waitUntil { timers.pending(Self.settle) == 1 }
        timers.fire(Self.settle)
        await waitUntil { !shown.overdue }
        #expect(!shown.overdue)
        #expect(shown.overlay(serviceComing: false) == .none)
    }

    /// The card waits for the deadline: the capsule before it.
    @Test func theCapsuleUntilTheDeadline() async {
        let (shown, _, timers) = make()
        shown.ended(.lost(retrying: true))
        await waitUntil { timers.pending(Self.deadline) == 1 }
        #expect(!shown.overdue)
        #expect(shown.overlay(serviceComing: false) == .reconnecting)
        timers.fire(Self.deadline)
        await waitUntil { shown.overdue }
        #expect(shown.overlay(serviceComing: false) == .disconnected(TabTerminalShown.notAnsweringLine))
    }

    /// A reconnect that stays up for `settle` ends the trouble: the next
    /// loss starts a fresh deadline, and the old one does nothing.
    @Test func aSettledReconnectStartsTheClockOver() async {
        let (shown, _, timers) = make()
        shown.ended(.lost(retrying: true))
        await waitUntil { timers.pending(Self.deadline) == 1 }
        shown.reset(TabTerminalSize(cols: 80, rows: 24))
        await waitUntil { timers.pending(Self.settle) == 1 }
        timers.fire(Self.settle)
        await waitUntil { !shown.inTrouble }
        #expect(!shown.inTrouble)
        timers.fire(Self.deadline) // the first loss's deadline: cancelled
        shown.ended(.lost(retrying: true))
        await waitUntil { timers.pending(Self.deadline) == 1 }
        try? await Task.sleep(for: .milliseconds(20))
        #expect(!shown.overdue)
        #expect(shown.overlay(serviceComing: true) == .reconnecting)
    }

    /// Reconnect on the card: the card goes, a new stream opens, and the
    /// deadline starts again.
    @Test func reconnectClearsTheCard() async {
        let (shown, p, timers) = make()
        shown.ended(.lost(retrying: false))
        await waitUntil { timers.pending(Self.deadline) == 1 }
        timers.fire(Self.deadline)
        await waitUntil { shown.overdue }
        #expect(shown.overlay(serviceComing: true) == .disconnected(TabTerminalShown.serviceAwayLine))
        shown.reconnect()
        #expect(!shown.overdue)
        #expect(p.opened.count == 2)
        #expect(shown.overlay(serviceComing: true) == .none) // connecting
        await waitUntil { timers.pending(Self.deadline) == 1 }
        timers.fire(Self.deadline)
        await waitUntil { shown.overdue }
        #expect(shown.overdue, "no reset after Reconnect: the card again")
    }
}
