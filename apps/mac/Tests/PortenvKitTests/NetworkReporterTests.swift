// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing

@testable import PortenvKit

/// A clock the test moves by hand.
final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var t = ContinuousClock.now
    var now: ContinuousClock.Instant { lock.withLock { t } }
    func advance(_ d: Duration) { lock.withLock { t += d } }
}

struct NetworkReporterTests {
    func networkCalls(_ cli: FakeDaemon) -> [String] {
        cli.calls.filter { $0.count > 1 && $0[1] == "network" }.map { $0[2] }
    }

    /// "up" goes out on changes only; "down" again every 10 s while it lasts,
    /// so portenvd's 30 s limit never expires it while the app runs.
    @Test func downIsRefreshedUpIsNot() async {
        let cli = FakeDaemon()
        let clock = TestClock()
        let r = NetworkReporter(daemon: cli, now: { clock.now })
        await r.report(usable: true)
        await r.report(usable: true)
        #expect(networkCalls(cli) == ["up"])
        await r.report(usable: false)
        clock.advance(.seconds(5))
        await r.refresh()
        #expect(networkCalls(cli) == ["up", "down"])
        clock.advance(.seconds(6))
        await r.refresh()
        #expect(networkCalls(cli) == ["up", "down", "down"])
        await r.report(usable: true)
        clock.advance(.seconds(60))
        await r.refresh()
        #expect(networkCalls(cli) == ["up", "down", "down", "up"])
    }

    /// A failed report is retried on the next call.
    @Test func failedReportIsRetried() async {
        let cli = FakeDaemon()
        cli.failing["network"] = "portenvd is not running"
        let r = NetworkReporter(daemon: cli)
        #expect(await r.report(usable: false) == false)
        cli.failing = [:]
        #expect(await r.report(usable: false) == true)
        #expect(networkCalls(cli) == ["down", "down"])
    }
}
