// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A transport whose streams the test plays, recording what's opened and
/// sent.
final class FakeTabTransport: TabTerminalTransport, @unchecked Sendable {
    struct Opened: Equatable {
        var box, session, tab: String
        var size: TabTerminalSize
    }

    private let lock = NSLock()
    private var _opened: [Opened] = []
    private var _sent: [TabTerminalRequest] = []
    private var _closes = 0
    private var conts: [AsyncThrowingStream<TabTerminalMessage, Error>.Continuation] = []

    var opened: [Opened] { lock.withLock { _opened } }
    var sent: [TabTerminalRequest] { lock.withLock { _sent } }
    var closes: Int { lock.withLock { _closes } }

    func tabTerminal(box: String, session: String, tab: String, size: TabTerminalSize) -> TabTerminalConnection {
        let (stream, cont) = AsyncThrowingStream.makeStream(of: TabTerminalMessage.self)
        lock.withLock {
            _opened.append(Opened(box: box, session: session, tab: tab, size: size))
            conts.append(cont)
        }
        return TabTerminalConnection(messages: stream,
                                     send: { [weak self] r in self?.lock.withLock { self?._sent.append(r) } },
                                     close: { [weak self] in self?.lock.withLock { self?._closes += 1 }; cont.finish() })
    }

    /// Plays a message on the latest stream.
    func play(_ m: TabTerminalMessage) { lock.withLock { conts.last }?.yield(m) }
    /// Ends the latest stream, as a lost connection would.
    func lose(_ error: Error? = nil) {
        let c = lock.withLock { conts.last }
        if let error { c?.finish(throwing: error) } else { c?.finish() }
    }
}

@MainActor
struct ControlModeTabsTests {
    static let fast = ControlModeTabSource.Retry(first: .milliseconds(10), longest: .milliseconds(40), attempts: 3)
    let size = TabTerminalSize(cols: 80, rows: 24)

    func waitUntil(_ cond: @MainActor () -> Bool) async {
        for _ in 0..<200 where !cond() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    /// Collects a source's events as they come.
    final class Events {
        var all: [TabTerminalEvent] = []
        var done = false
    }

    func collect(_ s: any TabTerminalSource) -> Events {
        let e = Events()
        Task { @MainActor in
            for await ev in s.events { e.all.append(ev) }
            e.done = true
        }
        return e
    }

    @Test func opensTheTabAndPlaysItsStream() async {
        let t = FakeTabTransport()
        let p = ControlModeTabProvider(box: "acme-api", transport: t, retry: Self.fast)
        let s = p.open(tab: "@3", size: size)
        let e = collect(s)
        await waitUntil { t.opened.count == 1 }
        #expect(t.opened == [.init(box: "acme-api", session: "main", tab: "@3", size: size)])
        // Nothing goes out before the reset: the tab's state isn't drawn yet.
        s.send(Array("early".utf8))
        t.play(.reset(TabTerminalSize(cols: 100, rows: 30), seed: Array("seed".utf8), skipped: false))
        t.play(.output(Array("out".utf8)))
        t.play(.resized(TabTerminalSize(cols: 90, rows: 20)))
        t.play(.author(line: 3, name: "Retnan", isYou: true, agentID: ""))
        await waitUntil { e.all.count == 3 }
        s.send(Array("ls\r".utf8))
        s.resize(TabTerminalSize(cols: 120, rows: 40))
        await waitUntil { t.sent.count == 2 }
        #expect(e.all == [
            .reset(TabTerminalSize(cols: 100, rows: 30), seed: Array("seed".utf8)),
            .output(Array("out".utf8)),
            .resized(TabTerminalSize(cols: 90, rows: 20)),
        ])
        #expect(t.sent == [.input(Array("ls\r".utf8)), .resize(TabTerminalSize(cols: 120, rows: 40))])
        t.play(.tabClosed)
        await waitUntil { e.done }
        #expect(e.all.last == .ended(.tabClosed))
        #expect(t.closes >= 1)
    }

    @Test func aLostStreamReconnectsAtTheLatestSizeAndResets() async {
        let t = FakeTabTransport()
        let s = ControlModeTabProvider(box: "b", transport: t, retry: Self.fast).open(tab: "@1", size: size)
        let e = collect(s)
        await waitUntil { t.opened.count == 1 }
        t.play(.reset(size, seed: [], skipped: false))
        await waitUntil { e.all.count == 1 }
        s.resize(TabTerminalSize(cols: 70, rows: 20))
        t.lose(TabTerminalStreamError("portenvd went away", permanent: false))
        await waitUntil { t.opened.count == 2 }
        #expect(e.all.last == .ended(.lost(retrying: true)))
        #expect(t.opened.last?.size == TabTerminalSize(cols: 70, rows: 20))
        // Typed while reconnecting: not sent, then or later.
        s.send(Array("x".utf8))
        t.play(.reset(TabTerminalSize(cols: 70, rows: 20), seed: Array("again".utf8), skipped: true))
        await waitUntil { e.all.count == 4 }
        // Output was skipped: the reset, then the notice.
        #expect(Array(e.all.suffix(2)) == [.reset(TabTerminalSize(cols: 70, rows: 20), seed: Array("again".utf8)), .skipped])
        #expect((s as? ControlModeTabSource)?.lastResetSkipped == true)
        #expect(!t.sent.contains(.input(Array("x".utf8))))
        #expect(!e.done)
        s.close()
    }

    @Test func givesUpAfterItsAttemptsOrAPermanentFailure() async {
        let t = FakeTabTransport()
        let s = ControlModeTabProvider(box: "b", transport: t, retry: Self.fast).open(tab: "@1", size: size)
        let e = collect(s)
        for n in 1...4 {
            await waitUntil { t.opened.count == n }
            t.lose()
        }
        await waitUntil { e.done }
        #expect(t.opened.count == 4) // the first try and three more
        #expect(e.all.filter { $0 == .ended(.lost(retrying: true)) }.count == 3)
        #expect(e.all.last == .ended(.lost(retrying: false)))

        let t2 = FakeTabTransport()
        let s2 = ControlModeTabProvider(box: "b", transport: t2, retry: Self.fast).open(tab: "@1", size: size)
        let e2 = collect(s2)
        await waitUntil { t2.opened.count == 1 }
        t2.lose(TabTerminalStreamError("This workspace's tmux is too old for the app's terminal (needs 3.4 or later).", permanent: true))
        await waitUntil { e2.done }
        #expect(t2.opened.count == 1)
        #expect(e2.all == [.ended(.lost(retrying: false))])
    }

    @Test func closingEndsTheStreamAndNeverReconnects() async {
        let t = FakeTabTransport()
        let s = ControlModeTabProvider(box: "b", transport: t, retry: Self.fast).open(tab: "@1", size: size)
        let e = collect(s)
        await waitUntil { t.opened.count == 1 }
        t.play(.reset(size, seed: [], skipped: false))
        await waitUntil { e.all.count == 1 }
        s.close()
        await waitUntil { e.done }
        try? await Task.sleep(for: .milliseconds(100))
        #expect(t.opened.count == 1)
        #expect(t.closes == 1)
        #expect(e.all == [.reset(size, seed: [])])
    }

    @Test func aStoppingWorkspaceEndsAsBoxStopped() async {
        let t = FakeTabTransport()
        let s = ControlModeTabProvider(box: "b", transport: t, retry: Self.fast).open(tab: "@1", size: size)
        let e = collect(s)
        await waitUntil { t.opened.count == 1 }
        t.play(.reset(size, seed: [], skipped: false))
        t.play(.boxStopping)
        await waitUntil { e.done }
        #expect(e.all.last == .ended(.boxStopped))
        #expect(t.opened.count == 1)
    }
}
