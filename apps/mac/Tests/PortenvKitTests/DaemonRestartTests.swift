// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var _n = 0
    var n: Int { lock.withLock { _n } }
    func add() { lock.withLock { _n += 1 } }
}

/// portenvd restarts while the window has a box open (a crash, an update, a
/// login item relaunch). Keys live only in its memory, so the new one has
/// none: the app starts it again, sends the keys again and reopens the box,
/// and meanwhile the state line shows the box's real state ("Not saved
/// since …"), never a frozen line, "is closed" or "Opening…".
@MainActor
struct DaemonRestartTests {
    let savedAt = "2026-10-09T05:56:40Z"
    var notSavedSince: String {
        BoxState(save: .notSaved, savedAt: ISO8601DateFormatter().date(from: savedAt)).line()
    }

    @Test func theAppRestartsPortenvdAndSendsTheKeysAgain() async {
        let cli = FakeDaemon(), restarts = Counter()
        let c = BoxController(box: "acme-api", daemon: cli, keychain: BlockingKeychain(blocks: false),
                              ensureDaemon: { restarts.add(); return nil })
        await c.open()
        cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"\#(savedAt)"}"#
        await c.refresh()

        // portenvd ends.
        cli.failing["state"] = "portenvd is not running"
        cli.failing["ping"] = "portenvd is not running"
        await c.refresh()
        #expect(restarts.n == 1, "the app makes sure launchd runs portenvd")
        #expect(c.subtitle == notSavedSince, "the real state, not the last line frozen")
        #expect(c.location == .thisMac, "the box isn't closed")

        // The new portenvd answers: it hasn't opened the box and has no keys.
        cli.failing = [:]
        cli.state = #"{"state":"SAVE_STATE_NOT_SAVED","saved_at":"\#(savedAt)","interrupted":true}"#
        cli.failingOnce["open"] = KeychainWait.daemonError
        let seen = Seen()
        let opens = Counter()
        cli.onRun = { args in
            guard args.count > 1 else { return }
            await MainActor.run { seen.add("\(c.subtitle) | \(c.location)") }
            if args[1] == "open" {
                opens.add()
                // The reopen that succeeds: the box is open again.
                if opens.n == 2 { cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"\#(savedAt)"}"# }
            }
        }
        await c.refresh()
        let actions = cli.calls.map { $0.count > 1 ? $0[1] : "" }
        #expect(actions.contains("provide-keys"), "the keys are sent again")
        #expect(opens.n == 2, "reopened (after handing the keys over)")
        #expect(!seen.all.contains { $0.contains("Opening…") || $0.contains("closed") }, "\(seen.all)")
        #expect(seen.all.allSatisfy { $0.hasPrefix(notSavedSince) }, "\(seen.all)")
        #expect(c.location == .thisMac && c.state?.save == .saved)
        #expect(restarts.n == 1)
    }

    /// A box closed in this window is not reopened behind the person's back.
    @Test func aClosedWindowIsNotReopened() async {
        let cli = FakeDaemon(), restarts = Counter()
        let c = BoxController(box: "acme-api", daemon: cli, ensureDaemon: { restarts.add(); return nil })
        cli.state = #"{"state":"SAVE_STATE_NOT_SAVED","saved_at":"\#(savedAt)","interrupted":true}"#
        await c.refresh()
        #expect(!cli.calls.contains { $0.count > 1 && $0[1] == "open" })
        cli.failing["state"] = "portenvd is not running"
        cli.failing["ping"] = "portenvd is not running"
        await c.refresh()
        #expect(restarts.n == 0)
    }

    /// One reopen per restart: a reopen that fails is not retried every 3 s
    /// (it would read the Keychain again and again).
    @Test func aFailedReopenIsNotRepeated() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli, keychain: BlockingKeychain(blocks: false), ensureDaemon: { nil })
        await c.open()
        cli.state = #"{"state":"SAVE_STATE_NOT_SAVED","saved_at":"\#(savedAt)","interrupted":true}"#
        cli.failing["open"] = "the box failed to start"
        await c.refresh()
        await c.refresh()
        await c.refresh()
        #expect(cli.calls.filter { $0.count > 1 && $0[1] == "open" }.count == 2, "the first open, then one reopen")
    }

    /// Relaunching for an update: portenvd has been asked to leave, and the
    /// app must not start another one before it quits (that one, a child of
    /// the old app, would close the box when the app exits).
    @Test func noRestartWhileRelaunching() async {
        let cli = FakeDaemon(), restarts = Counter()
        let c = BoxController(box: "acme-api", daemon: cli, ensureDaemon: { restarts.add(); return nil })
        await c.open()
        c.relaunchingForUpdate()
        cli.failing["state"] = "portenvd is not running"
        cli.failing["ping"] = "portenvd is not running"
        await c.refresh()
        #expect(restarts.n == 0)
    }

    /// launchd brings portenvd back; until then the line shows the real
    /// state. Past the deadline the window says so plainly, and the message
    /// goes once portenvd answers again.
    @Test func pastTheDeadlineTheWindowSaysSo() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli, ensureDaemon: { nil }, daemonDeadline: .milliseconds(150))
        await c.open()
        cli.failing["state"] = "portenvd is not running"
        cli.failing["ping"] = "portenvd is not running"
        await c.refresh()
        #expect(c.error == nil, "not yet: launchd is starting it")
        #expect(c.state?.save == .notSaved)
        try? await Task.sleep(for: .milliseconds(200))
        await c.refresh()
        #expect(c.error == BoxController.daemonGone)
        cli.failing = [:]
        cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"\#(savedAt)"}"#
        await c.refresh()
        #expect(c.error == nil, "portenvd is back")
    }

    /// The login item needs the person's approval: said at once, in their
    /// words (Login Items), not after a timeout.
    @Test func aLoginItemNeedingApprovalIsSaidAtOnce() async {
        let cli = FakeDaemon()
        let approval = "Open System Settings › General › Login Items and allow Portenv."
        let c = BoxController(box: "acme-api", daemon: cli, ensureDaemon: { approval })
        cli.failing["ping"] = "portenvd is not running"
        let waiting = Task { await c.waitForDaemon() }
        for _ in 0..<50 where c.error == nil { try? await Task.sleep(for: .milliseconds(20)) }
        #expect(c.error == approval)
        cli.failing = [:]
        await waiting.value
    }

    /// At launch the app waits for launchd's portenvd, never starting one.
    @Test func atLaunchTheAppWaitsWithADeadline() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli, ensureDaemon: { nil }, daemonDeadline: .milliseconds(150))
        cli.failing["ping"] = "portenvd is not running"
        await c.waitForDaemon()
        #expect(c.error == BoxController.daemonGone)
    }
}
