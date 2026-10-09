// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// The state line follows what portenvd pushes (WatchBoxState, 1.1), not a
/// poll; when the stream ends, portenvd has gone (item 1g).
@MainActor
struct FollowTests {
    func waitUntil(_ cond: @MainActor () -> Bool) async {
        for _ in 0..<100 where !cond() { try? await Task.sleep(for: .milliseconds(20)) }
    }

    @Test func pushedStatesReachTheLineWithoutPolling() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli)
        await c.open()
        let following = Task { await c.follow() }
        defer { following.cancel() }
        await waitUntil { cli.calls.contains { $0.count > 1 && $0[1] == "watch" } }
        try? await Task.sleep(for: .milliseconds(50)) // the watcher is registered
        let polls = cli.calls.filter { $0.count > 1 && $0[1] == "state" }.count
        cli.push(#"{"state":"SAVE_STATE_SAVING"}"#)
        await waitUntil { c.state?.save == .saving }
        #expect(c.subtitle == "This Mac · Saving…")
        cli.push(#"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-09T08:00:00Z"}"#)
        await waitUntil { c.state?.save == .saved }
        #expect(c.state?.save == .saved)
        #expect(cli.calls.filter { $0.count > 1 && $0[1] == "state" }.count == polls, "no poll: the states were pushed")
    }

    @Test func whenTheStreamEndsPortenvdIsStartedAgain() async {
        let cli = FakeDaemon(), restarts = Counter()
        let c = BoxController(box: "acme-api", daemon: cli, restartDaemon: { restarts.add() }, followRetry: .milliseconds(50))
        await c.open()
        cli.state = #"{"state":"SAVE_STATE_SAVED","saved_at":"2026-10-09T08:00:00Z"}"#
        let following = Task { await c.follow() }
        defer { following.cancel() }
        await waitUntil { c.state?.save == .saved }
        // portenvd goes away: the stream ends and it doesn't answer.
        cli.failing["ping"] = "portenvd is not running"
        cli.failing["watch"] = "portenvd is not running"
        cli.endWatches()
        await waitUntil { restarts.n > 0 }
        #expect(restarts.n >= 1)
        #expect(c.state?.save == .notSaved && c.location == .thisMac, "the real state, and the box isn't closed")
        // The new portenvd answers, with the box interrupted: it's reopened.
        cli.failing = [:]
        cli.state = #"{"state":"SAVE_STATE_NOT_SAVED","saved_at":"2026-10-09T08:00:00Z","interrupted":true}"#
        let opens = cli.calls.filter { $0.count > 1 && $0[1] == "open" }.count
        await waitUntil { cli.calls.filter { $0.count > 1 && $0[1] == "open" }.count > opens }
        #expect(cli.calls.filter { $0.count > 1 && $0[1] == "open" }.count == opens + 1)
    }

    /// A state pushed while an action runs only updates the line; where the
    /// box is comes from the action (it once flashed "is closed").
    @Test func aPushDuringAnActionDoesntMoveTheBox() async {
        let cli = FakeDaemon()
        let c = BoxController(box: "acme-api", daemon: cli)
        await c.open()
        let following = Task { await c.follow() }
        defer { following.cancel() }
        await waitUntil { cli.calls.contains { $0.count > 1 && $0[1] == "watch" } }
        try? await Task.sleep(for: .milliseconds(50))
        let seen = Seen()
        cli.onRun = { args in
            guard args.count > 1, args[1] == "point" else { return }
            cli.push(#"{"state":"SAVE_STATE_CLOSED"}"#)
            try? await Task.sleep(for: .milliseconds(150))
            await MainActor.run { seen.add("\(c.location)") }
        }
        await c.makeSavePoint()
        #expect(seen.all == ["thisMac"], "during the action: \(seen.all)")
        #expect(c.location == .thisMac)
    }
}
