// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A Keychain that blocks (macOS asking the person) until released.
final class BlockingKeychain: KeychainReading, @unchecked Sendable {
    private let gate = DispatchSemaphore(value: 0)
    private let blocks: Bool
    private let lock = NSLock()
    private var _reads: [String] = []
    var reads: [String] { lock.withLock { _reads } }
    private var _queues: [String] = []
    /// The dispatch queue each read ran on.
    var queues: [String] { lock.withLock { _queues } }
    init(blocks: Bool) { self.blocks = blocks }
    func release() { gate.signal() }

    func read(account: String) throws -> Data? {
        #expect(!Thread.isMainThread, "Keychain reads never run on the main thread")
        let label = String(cString: __dispatch_queue_get_label(nil))
        lock.withLock { _reads.append(account); _queues.append(label) }
        if blocks && reads.count == 1 { gate.wait() }
        return Data("key-\(account)".utf8)
    }
}

final class CountingNotifier: KeychainWaitNotifying, @unchecked Sendable {
    private let lock = NSLock()
    private var _count = 0
    var count: Int { lock.withLock { _count } }
    func keychainWaiting(box: String) async { lock.withLock { _count += 1 } }
    private var _saved: [String] = []
    var saved: [String] { lock.withLock { _saved } }
    func savedAfterQuit(box: String, at _: Date) async { lock.withLock { _saved.append(box) } }
}

final class Seen: @unchecked Sendable {
    private let lock = NSLock()
    private var _all: [String] = []
    var all: [String] { lock.withLock { _all } }
    func add(_ s: String) { lock.withLock { _all.append(s) } }
}

@MainActor
struct KeychainWaitTests {
    func controller(_ keychain: BlockingKeychain, _ cli: FakeDaemon, _ notifier: CountingNotifier) -> BoxController {
        cli.failingOnce["open"] = KeychainWait.daemonError
        return BoxController(box: "acme-api", daemon: cli, keychain: keychain, notifier: notifier, keychainThreshold: .milliseconds(200))
    }

    func waitUntil(_ cond: @MainActor () -> Bool) async {
        for _ in 0..<100 where !cond() { try? await Task.sleep(for: .milliseconds(20)) }
    }

    @Test func aBlockedReadSaysSoAndNotifiesOnce() async {
        let kc = BlockingKeychain(blocks: true), cli = FakeDaemon(), n = CountingNotifier()
        defer { kc.release() } // never leave a read blocked, even when the test fails
        let c = controller(kc, cli, n)
        let opening = Task { await c.open() }
        await waitUntil { c.waitingForKeychain }
        #expect(c.subtitle == "Waiting for Keychain access")
        #expect(c.progressDetail == "Check for a password prompt. It may be behind other windows.")
        #expect(c.symbol == "lock" && !c.symbolSpins)
        #expect(c.title.consistent)
        // The line changes first, then the notification goes out.
        await waitUntil { n.count > 0 }
        #expect(n.count == 1)
        kc.release()
        await opening.value
        #expect(!c.waitingForKeychain)
        #expect(c.location == .thisMac)
    }

    /// After the person allows the read, the window says the box is opening
    /// again (the wait line once stayed up, spinning, until the box opened).
    @Test func afterApprovalTheLineSaysOpening() async {
        let kc = BlockingKeychain(blocks: true), cli = FakeDaemon(), n = CountingNotifier()
        defer { kc.release() } // never leave a read blocked, even when the test fails
        let c = controller(kc, cli, n)
        let seen = Seen()
        cli.onRun = { args in
            guard args.count > 1, args[1] == "open" else { return }
            await MainActor.run { seen.add(c.subtitle) }
        }
        let opening = Task { await c.open() }
        await waitUntil { c.waitingForKeychain }
        kc.release()
        await opening.value
        #expect(seen.all == ["Opening…", "Opening…"], "the second open says Opening…, not the wait line")
    }

    @Test func cancelStopsTheOpenCleanly() async {
        let kc = BlockingKeychain(blocks: true), cli = FakeDaemon(), n = CountingNotifier()
        defer { kc.release() } // never leave a read blocked, even when the test fails
        let c = controller(kc, cli, n)
        let opening = Task { await c.open() }
        await waitUntil { c.waitingForKeychain }
        cli.state = #"{"state":"SAVE_STATE_CLOSED"}"# // what portenvd says: nothing was opened
        c.cancelKeychainWait()
        await opening.value
        let actions = cli.calls.map { $0.count > 1 ? $0[1] : "" }
        #expect(!actions.contains("provide-keys"), "nothing handed over after Cancel")
        #expect(actions.filter { $0 == "open" }.count == 1, "no second open after Cancel")
        #expect(c.location == .closed && !c.busy && !c.waitingForKeychain && c.error == nil)
        kc.release() // the prompt is answered later: its result is dropped
    }

    @Test func approvedKeysGoToPortenvd() async {
        let kc = BlockingKeychain(blocks: false), cli = FakeDaemon(), n = CountingNotifier()
        let c = controller(kc, cli, n)
        await c.open()
        #expect(kc.reads == ["box-1", "box-1-storage"])
        #expect(kc.queues.allSatisfy { $0 == "com.portenv.keychain" }, "reads run on their own queue, never the cooperative pool: \(kc.queues)")
        let provide = cli.calls.first { $0.count > 1 && $0[1] == "provide-keys" }
        #expect(provide == ["app", "provide-keys", "acme-api"])
        #expect(cli.provided.first?["box-1"] == Data("key-box-1".utf8), "the keys read, over portenvd's API")
        #expect(cli.calls.filter { $0.count > 1 && $0[1] == "open" }.count == 2)
        #expect(n.count == 0, "a quick read never notifies")
        #expect(c.location == .thisMac)
    }
}
